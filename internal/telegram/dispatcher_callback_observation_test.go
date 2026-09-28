package telegram

import (
	"context"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type answeringNativeInteraction struct {
	answerer callbackQueryAnswerer
	text     string
}

func (n *answeringNativeInteraction) HandleCallback(ctx context.Context, event *core.CallbackQueryEvent) (bool, error) {
	if event == nil {
		return false, nil
	}
	if n.answerer != nil {
		if err := n.answerer.AnswerCallbackQuery(ctx, event.QueryID, n.text, false); err != nil {
			return true, err
		}
	}
	return true, nil
}

func newCallbackObservationDispatcher(t *testing.T, native NativeInteractionDispatcher) (*Dispatcher, *callbackRecordingService, <-chan *core.CallbackQueryEvent) {
	t.Helper()
	d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
	svc := newCallbackRecordingService()
	d.SetService(svc)
	if native != nil {
		d.SetNativeInteractions(native)
	}
	bus := core.NewEventBus()
	if err := bus.Start(context.Background()); err != nil {
		t.Fatalf("start event bus: %v", err)
	}
	t.Cleanup(func() { _ = bus.Close() })
	d.SetEventBus(bus)
	observed := make(chan *core.CallbackQueryEvent, 1)
	sub := bus.Subscribe(core.EventTypeCallbackQuery, func(event core.Event) {
		if callback, ok := event.(*core.CallbackQueryEvent); ok {
			select {
			case observed <- callback:
			default:
			}
		}
	})
	t.Cleanup(sub)
	return d, svc, observed
}

func waitCallbackObservation(t *testing.T, observed <-chan *core.CallbackQueryEvent, queryID int64) {
	t.Helper()
	select {
	case event := <-observed:
		if event == nil || event.QueryID != queryID {
			t.Fatalf("observed callback=%+v, want query %d", event, queryID)
		}
	case <-time.After(time.Second):
		t.Fatalf("callback query %d was not published to observation bus", queryID)
	}
}

func TestDispatcherCallbackEventBusIsObservationOnlyForUnknownAndNoop(t *testing.T) {
	for _, tc := range []struct {
		name       string
		inline     bool
		queryID    int64
		data       []byte
		answerText string
	}{
		{name: "message_unknown", queryID: 4101, data: []byte("unknown"), answerText: unknownCallbackExpiredText},
		{name: "message_noop", queryID: 4102, data: []byte("noop"), answerText: ""},
		{name: "inline_unknown", inline: true, queryID: 4103, data: []byte("unknown"), answerText: unknownCallbackExpiredText},
		{name: "inline_noop", inline: true, queryID: 4104, data: []byte("noop"), answerText: ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d, svc, observed := newCallbackObservationDispatcher(t, nil)
			if tc.inline {
				err := d.OnInlineBotCallbackQuery(context.Background(), tg.Entities{}, &tg.UpdateInlineBotCallbackQuery{
					QueryID: tc.queryID,
					UserID:  42,
					MsgID:   &tg.InputBotInlineMessageID64{DCID: 1, ID: tc.queryID, AccessHash: 7},
					Data:    tc.data,
				})
				if err != nil {
					t.Fatalf("inline callback: %v", err)
				}
			} else {
				err := d.OnBotCallbackQuery(context.Background(), tg.Entities{}, &tg.UpdateBotCallbackQuery{
					QueryID: tc.queryID,
					UserID:  42,
					Peer:    &tg.PeerChat{ChatID: 10},
					MsgID:   20,
					Data:    tc.data,
				})
				if err != nil {
					t.Fatalf("message callback: %v", err)
				}
			}
			waitCallbackObservation(t, observed, tc.queryID)
			if got := svc.callCount.Load(); got != 1 {
				t.Fatalf("callback answer calls=%d, want exactly 1 with observer present", got)
			}
			svc.mu.Lock()
			answer := svc.answered[tc.queryID]
			svc.mu.Unlock()
			if answer != tc.answerText {
				t.Fatalf("callback answer=%q, want %q", answer, tc.answerText)
			}
		})
	}
}

func TestDispatcherCallbackEventBusDoesNotStealA2AnswerOwnership(t *testing.T) {
	for _, tc := range []struct {
		name    string
		inline  bool
		queryID int64
	}{
		{name: "message", queryID: 4201},
		{name: "inline", inline: true, queryID: 4202},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := NewDispatcher(core.NewRouter("."), core.NewPermissions(1, nil), nil, zap.NewNop())
			svc := newCallbackRecordingService()
			d.SetService(svc)
			d.SetNativeInteractions(&answeringNativeInteraction{answerer: svc, text: "native"})
			bus := core.NewEventBus()
			if err := bus.Start(context.Background()); err != nil {
				t.Fatalf("start event bus: %v", err)
			}
			defer bus.Close()
			d.SetEventBus(bus)
			observed := make(chan *core.CallbackQueryEvent, 1)
			sub := bus.Subscribe(core.EventTypeCallbackQuery, func(event core.Event) {
				if callback, ok := event.(*core.CallbackQueryEvent); ok {
					observed <- callback
				}
			})
			defer sub()

			data := []byte("a2:next:AAAAAAAAAAAAAAAAAAAAAA.1")
			if tc.inline {
				err := d.OnInlineBotCallbackQuery(context.Background(), tg.Entities{}, &tg.UpdateInlineBotCallbackQuery{
					QueryID: tc.queryID,
					UserID:  42,
					MsgID:   &tg.InputBotInlineMessageID64{DCID: 1, ID: tc.queryID, AccessHash: 7},
					Data:    data,
				})
				if err != nil {
					t.Fatalf("inline callback: %v", err)
				}
			} else {
				err := d.OnBotCallbackQuery(context.Background(), tg.Entities{}, &tg.UpdateBotCallbackQuery{
					QueryID: tc.queryID,
					UserID:  42,
					Peer:    &tg.PeerChat{ChatID: 10},
					MsgID:   20,
					Data:    data,
				})
				if err != nil {
					t.Fatalf("message callback: %v", err)
				}
			}
			waitCallbackObservation(t, observed, tc.queryID)
			if got := svc.callCount.Load(); got != 1 {
				t.Fatalf("callback answer calls=%d, want exactly 1 native-owned answer", got)
			}
			svc.mu.Lock()
			answer := svc.answered[tc.queryID]
			svc.mu.Unlock()
			if answer != "native" {
				t.Fatalf("callback answer=%q, want native owner response", answer)
			}
		})
	}
}
