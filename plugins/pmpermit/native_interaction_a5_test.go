package pmpermit

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	pmpermitsvc "github.com/inipew/goultroid/internal/services/pmpermit"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type a5NativeTaskClient struct{}

func (*a5NativeTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	err := spec.Handler(ctx)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if err != nil {
		result.Outcome = tasks.OutcomeFailed
	}
	if spec.OnComplete != nil {
		spec.OnComplete(result)
	}
	return nil, nil
}
func (*a5NativeTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*a5NativeTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*a5NativeTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type a5NativeTelegram struct {
	core.MockTelegramServicer
	markup tg.ReplyMarkupClass
}

func (m *a5NativeTelegram) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, _ string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.markup = markup
	return &tg.Message{ID: 100}, nil
}

func (m *a5NativeTelegram) EditMessageMarkup(_ context.Context, _ tg.InputPeerClass, _ int, _ string, markup tg.ReplyMarkupClass) error {
	m.markup = markup
	return nil
}

func TestA5NativePMPermitA2OwnerMutationAndStaleCallback(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const ownerID int64 = 12345
	svc := pmpermitsvc.NewService(NewSQLiteRepository(db), nil, ownerID, core.NewPermissions(ownerID, nil), zap.NewNop())
	p := New(svc)
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:pmpermit", Generation: 1}
	reg, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer reg.Close()
	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	tgSvc := &a5NativeTelegram{}
	adapter, err := nativeinteraction.New(registry, sessions, rootinteraction.NewDispatcher(sessions),
		&a5NativeTaskClient{}, func() presentationtelegram.BridgeService { return tgSvc }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter, Catalog: registry, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	command := &core.Context{
		Ctx: context.Background(), Source: core.ExecutionInteractive,
		Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		PeerID:  &tg.InputPeerUser{UserID: ownerID, AccessHash: 99},
	}
	if opened, err := p.openNativePMPermit(command); err != nil || !opened {
		t.Fatalf("open dashboard opened=%v err=%v", opened, err)
	}
	markup, ok := tgSvc.markup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("dashboard missing a2 buttons: %T", tgSvc.markup)
	}
	var data []byte
	for _, row := range markup.Rows {
		for _, btn := range row.Buttons {
			callback, ok := btn.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if !rootinteraction.OwnsCallbackData(callback.Data) {
				t.Fatalf("non-a2 button %q", callback.Data)
			}
			if _, err := rootinteraction.ParseCallbackToken(callback.Data); err != nil {
				t.Fatalf("malformed a2 callback: %v", err)
			}
			if strings.Contains(callback.Text, "Disable") {
				data = append([]byte(nil), callback.Data...)
			}
		}
	}
	if len(data) == 0 {
		t.Fatal("toggle button missing")
	}
	event := &core.CallbackQueryEvent{
		QueryID: 701, UserID: ownerID, ChatID: ownerID, MsgID: 100,
		Data: data, Origin: core.CallbackOriginMessage,
		Target: core.CallbackTarget{Origin: core.CallbackOriginMessage,
			Peer: &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}, MessageID: 100},
	}
	handled, err := adapter.HandleCallback(context.Background(), event)
	if !handled || err != nil {
		t.Fatalf("toggle handled=%v err=%v", handled, err)
	}
	if svc.IsEnabled() {
		t.Fatal("owner a2 toggle did not disable PM Permit")
	}
	stale := *event
	stale.QueryID++
	handled, err = adapter.HandleCallback(context.Background(), &stale)
	if !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale callback handled=%v err=%v", handled, err)
	}
	if svc.IsEnabled() {
		t.Fatal("stale callback changed protection")
	}
	if got := sessions.CancelScope(scope); got != 1 {
		t.Fatalf("expected one active native session; got %d", got)
	}
}

func TestA5NativePMPermitFailsClosedForOtherActor(t *testing.T) {
	svc := pmpermitsvc.NewService(nil, nil, 12345, nil, nil)
	p := New(svc)
	if err := p.setEnabledForActor(context.Background(), 333, false); err == nil {
		t.Fatal("non-owner modified PM protection")
	}
	if !svc.IsEnabled() {
		t.Fatal("non-owner disabled PM protection")
	}
}
