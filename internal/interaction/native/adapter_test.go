package native

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/tasks"
)

type immediateTasks struct {
	mu        sync.Mutex
	submitted int
}

func (c *immediateTasks) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	c.submitted++
	c.mu.Unlock()
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

func (c *immediateTasks) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (c *immediateTasks) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *immediateTasks) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}
func (c *immediateTasks) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.submitted
}

type testTelegramService struct {
	core.MockTelegramServicer

	mu      sync.Mutex
	markup  tg.ReplyMarkupClass
	answers []string
}

func (s *testTelegramService) SendMessageWithMarkup(_ context.Context, _ tg.InputPeerClass, _ string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	s.mu.Lock()
	s.markup = markup
	s.mu.Unlock()
	return &tg.Message{ID: 77}, nil
}

func (s *testTelegramService) AnswerCallbackQuery(_ context.Context, _ int64, text string, _ bool) error {
	s.mu.Lock()
	s.answers = append(s.answers, text)
	s.mu.Unlock()
	return nil
}

func TestNativeAdapterUsesSharedA2RuntimeWithoutAssistant(t *testing.T) {
	registry := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:demo", Generation: 1}
	registration, err := registry.Register(feature.Owner{ID: "demo", Scope: scope}, feature.Spec{
		ID:   "demo",
		Name: "Demo",
		Interactions: []feature.Interaction{
			{
				ID:       "home",
				Kind:     feature.InteractionScreen,
				Surfaces: execution.SurfaceUserbot,
				Policy:   feature.OwnerPolicy(execution.SurfaceUserbot),
			},
			{
				ID:       "next",
				Kind:     feature.InteractionAction,
				Surfaces: execution.SurfaceUserbot,
				Policy:   feature.OwnerPolicy(execution.SurfaceUserbot),
			},
		},
	})
	if err != nil {
		t.Fatalf("Register() error = %v", err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(registry, rootinteraction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error = %v", err)
	}
	defer sessions.Close()
	actions := rootinteraction.NewDispatcher(sessions)
	taskClient := &immediateTasks{}
	service := &testTelegramService{}
	adapter, err := New(registry, sessions, actions, taskClient, func() core.TelegramServicer { return service }, core.NewPermissions(1, nil))
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	called := 0
	actionRegistration, err := adapter.RegisterAction(scope, "demo", "next", func(ctx *orchestration.Context) error {
		called++
		return ctx.Answer("ok", false)
	})
	if err != nil {
		t.Fatalf("RegisterAction() error = %v", err)
	}
	defer actionRegistration.Close()

	peer := &tg.InputPeerUser{UserID: 1, AccessHash: 9}
	interactionCtx, err := adapter.Begin(&core.Context{
		Ctx:    context.Background(),
		Sender: &core.User{ID: 1},
		Chat:   &core.Chat{ID: 99, Type: "private"},
		PeerID: peer,
	}, BeginRequest{
		FeatureID: "demo",
		ScreenID:  "home",
		TTL:       time.Minute,
		View: presentation.View{
			Text: "Demo",
			Rows: []presentation.Row{{presentation.ActionButton("Next", "next")}},
		},
	})
	if err != nil {
		t.Fatalf("Begin() error = %v", err)
	}

	data := callbackData(t, service)
	event := callbackEvent(data, 100, 1, 99, 77, peer)
	handled, err := adapter.HandleCallback(context.Background(), event)
	if err != nil || !handled {
		t.Fatalf("HandleCallback() = handled %v, err %v", handled, err)
	}
	if called != 1 || taskClient.count() != 1 {
		t.Fatalf("called=%d submitted=%d, want 1/1", called, taskClient.count())
	}
	service.mu.Lock()
	answers := append([]string(nil), service.answers...)
	service.mu.Unlock()
	if len(answers) != 1 || answers[0] != "ok" {
		t.Fatalf("answers=%q, want [ok]", answers)
	}

	wrongActor := callbackEvent(data, 101, 2, 99, 77, peer)
	if handled, err := adapter.HandleCallback(context.Background(), wrongActor); !handled || err == nil {
		t.Fatalf("wrong actor = handled %v, err %v", handled, err)
	}
	wrongTarget := callbackEvent(data, 102, 1, 99, 78, peer)
	if handled, err := adapter.HandleCallback(context.Background(), wrongTarget); !handled || err == nil {
		t.Fatalf("wrong target = handled %v, err %v", handled, err)
	}
	if taskClient.count() != 1 {
		t.Fatalf("rejected callbacks reached TaskEngine: %d", taskClient.count())
	}

	if err := interactionCtx.UpdateState([]byte("next revision"), time.Minute); err != nil {
		t.Fatalf("UpdateState() error = %v", err)
	}
	stale := callbackEvent(data, 103, 1, 99, 77, peer)
	if handled, err := adapter.HandleCallback(context.Background(), stale); !handled || !errors.Is(err, rootinteraction.ErrStaleToken) {
		t.Fatalf("stale callback = handled %v, err %v", handled, err)
	}
}

func TestNativeAdapterDoesNotClaimLegacyCallbackData(t *testing.T) {
	var adapter *Adapter
	handled, err := adapter.HandleCallback(context.Background(), &core.CallbackQueryEvent{Data: []byte("v1:settings:nav:noop")})
	if err != nil || handled {
		t.Fatalf("legacy callback = handled %v, err %v", handled, err)
	}
}

func callbackData(t *testing.T, service *testTelegramService) []byte {
	t.Helper()
	service.mu.Lock()
	defer service.mu.Unlock()
	markup, ok := service.markup.(*tg.ReplyInlineMarkup)
	if !ok || len(markup.Rows) != 1 || len(markup.Rows[0].Buttons) != 1 {
		t.Fatalf("markup=%T %+v", service.markup, service.markup)
	}
	button, ok := markup.Rows[0].Buttons[0].(*tg.KeyboardButtonCallback)
	if !ok {
		t.Fatalf("button=%T %+v", markup.Rows[0].Buttons[0], markup.Rows[0].Buttons[0])
	}
	return append([]byte(nil), button.Data...)
}

func callbackEvent(data []byte, queryID, userID, chatID int64, messageID int, peer tg.InputPeerClass) *core.CallbackQueryEvent {
	return &core.CallbackQueryEvent{
		Data:    append([]byte(nil), data...),
		QueryID: queryID,
		UserID:  userID,
		ChatID:  chatID,
		MsgID:   messageID,
		Origin:  core.CallbackOriginMessage,
		Target: core.CallbackTarget{
			Origin:    core.CallbackOriginMessage,
			Peer:      peer,
			MessageID: messageID,
		},
	}
}
