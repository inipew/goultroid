package afk

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
	"github.com/inipew/goultroid/internal/tasks"
)

type a5AFKNativeTaskClient struct{}

func (*a5AFKNativeTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
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
func (*a5AFKNativeTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*a5AFKNativeTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*a5AFKNativeTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

type a5AFKNativeTelegram struct {
	core.MockTelegramServicer
	markup tg.ReplyMarkupClass
}

func (m *a5AFKNativeTelegram) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, _ string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.markup = markup
	return &tg.Message{ID: 100}, nil
}
func (m *a5AFKNativeTelegram) EditMessageMarkup(_ context.Context, _ tg.InputPeerClass, _ int, _ string, markup tg.ReplyMarkupClass) error {
	m.markup = markup
	return nil
}

func TestA5AFKNativeA2OwnerAndLifecycle(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	const ownerID int64 = 1001
	tgSvc := &a5AFKNativeTelegram{}
	p := New(NewSQLiteRepository(db), ownerID, func() core.TelegramServicer { return tgSvc })
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}

	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:afk", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()
	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	adapter, err := nativeinteraction.New(registry, sessions, rootinteraction.NewDispatcher(sessions),
		&a5AFKNativeTaskClient{}, func() presentationtelegram.BridgeService { return tgSvc }, core.NewPermissions(ownerID, nil))
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindNative(nativeinteraction.DriverRuntime{Interactions: adapter, Catalog: registry, Scope: scope})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if p.currentNativeAFK().Interactions != nil {
			cleanup()
		}
	}()

	cmd := &core.Context{
		Ctx: context.Background(), Source: core.ExecutionInteractive,
		Message: &core.Message{ID: 1, SenderID: ownerID, IsOutgoing: true},
		Sender:  &core.User{ID: ownerID},
		Chat:    &core.Chat{ID: ownerID, Type: "private"},
		PeerID:  &tg.InputPeerUser{UserID: ownerID, AccessHash: 99},
		Args:    []string{"menu"},
	}
	if err := p.handleAFKCommand(cmd); err != nil {
		t.Fatal(err)
	}
	markup, ok := tgSvc.markup.(*tg.ReplyInlineMarkup)
	if !ok {
		t.Fatalf("missing a2 menu: %T", tgSvc.markup)
	}
	var enable []byte
	for _, row := range markup.Rows {
		for _, button := range row.Buttons {
			cb, ok := button.(*tg.KeyboardButtonCallback)
			if !ok {
				continue
			}
			if !rootinteraction.OwnsCallbackData(cb.Data) {
				t.Fatalf("not a2: %q", cb.Data)
			}
			if _, err := rootinteraction.ParseCallbackToken(cb.Data); err != nil {
				t.Fatal(err)
			}
			if strings.Contains(cb.Text, "Enable") {
				enable = append([]byte(nil), cb.Data...)
			}
		}
	}
	if len(enable) == 0 {
		t.Fatal("enable button missing")
	}
	event := &core.CallbackQueryEvent{
		QueryID: 721, UserID: ownerID, ChatID: ownerID, MsgID: 100,
		Data: enable, Origin: core.CallbackOriginMessage,
		Target: core.CallbackTarget{Origin: core.CallbackOriginMessage, Peer: &tg.InputPeerUser{UserID: ownerID, AccessHash: 99}, MessageID: 100},
	}
	handled, err := adapter.HandleCallback(context.Background(), event)
	if !handled || err != nil {
		t.Fatalf("enable handled=%v err=%v", handled, err)
	}
	if st := p.state.Load(); st == nil || !st.isAFK {
		t.Fatalf("native enable did not persist AFK state: %+v", st)
	}
	stale := *event
	stale.QueryID++
	handled, err = adapter.HandleCallback(context.Background(), &stale)
	if !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale action handled=%v err=%v", handled, err)
	}
	if count := sessions.CancelScope(scope); count != 1 {
		t.Fatalf("active session count %d", count)
	}
	cleanup()
	if rt := p.currentNativeAFK(); rt.Interactions != nil {
		t.Fatal("native scope not detached after cleanup")
	}
}

func TestA5AFKMenuRequiresOwner(t *testing.T) {
	p := New(nil, 1001, nil)
	rt := p.currentNativeAFK()
	if rt.Interactions != nil {
		t.Fatal("unexpected a2 runtime")
	}
	if opened, err := p.openNativeAFK(&core.Context{}); opened || err != nil {
		t.Fatalf("missing native adapter must offer fallback: opened=%v err=%v", opened, err)
	}
}
