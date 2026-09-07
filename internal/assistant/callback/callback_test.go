package callback_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/callback"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"go.uber.org/zap"
)

func TestPayload_EncodeAndParse(t *testing.T) {
	// Standard v2 encode
	data, err := callback.Encode("assistant", "start")
	if err != nil {
		t.Fatalf("unexpected encode error: %v", err)
	}
	if data != "a1:assistant:start" {
		t.Fatalf("expected a1:assistant:start, got %s", data)
	}

	// State encode
	dataWithState, err := callback.EncodeWithState("assistant", "page", "2")
	if err != nil {
		t.Fatalf("unexpected encode with state error: %v", err)
	}
	if dataWithState != "a1:assistant:page:2" {
		t.Fatalf("expected a1:assistant:page:2, got %s", dataWithState)
	}

	// Parse v2
	parsed, err := callback.Parse([]byte(dataWithState))
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	if parsed.Version != "a1" || parsed.Namespace != "assistant" || parsed.Action != "page" || parsed.State != "2" {
		t.Fatalf("parsed values mismatch: %+v", parsed)
	}

	// Parse legacy v1 format for backward compatibility
	legacyParsed, err := callback.Parse([]byte("v1:settings:general:noop"))
	if err != nil {
		t.Fatalf("unexpected error parsing legacy payload: %v", err)
	}
	if legacyParsed.Version != "v1" || legacyParsed.Namespace != "settings" || legacyParsed.Action != "general" || legacyParsed.State != "noop" {
		t.Fatalf("legacy parsed values mismatch: %+v", legacyParsed)
	}

	// Too long payload
	longStr := "a1:ns:act:"
	for len(longStr) < 65 {
		longStr += "x"
	}
	_, err = callback.Parse([]byte(longStr))
	if !errors.Is(err, callback.ErrPayloadTooLong) {
		t.Fatalf("expected ErrPayloadTooLong, got %v", err)
	}

	// Malformed payload
	_, err = callback.Parse([]byte("bad_payload_format"))
	if !errors.Is(err, callback.ErrMalformedPayload) {
		t.Fatalf("expected ErrMalformedPayload, got %v", err)
	}
}

type fakeInteraction struct {
	answerCount int32
	lastAnswer  string
	lastAlert   bool
}

func (f *fakeInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	atomic.AddInt32(&f.answerCount, 1)
	f.lastAnswer = text
	f.lastAlert = alert
	return nil
}

func (f *fakeInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	return nil
}
func (f *fakeInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}
func (f *fakeInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}
func (f *fakeInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}
func (f *fakeInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 1}, nil
}

func TestTransaction_SingleFlightAnswer(t *testing.T) {
	fake := &fakeInteraction{}
	tx := callback.NewTransaction(
		1001,
		42,
		callback.ParsedPayload{Namespace: "assistant", Action: "ping"},
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 42}, 10, 42, 1),
		fake,
	)

	ctx := context.Background()

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = tx.Answer(ctx, "Pong!", true)
		}()
	}
	wg.Wait()

	if count := atomic.LoadInt32(&fake.answerCount); count != 1 {
		t.Fatalf("expected exactly 1 answer call across concurrent goroutines, got %d", count)
	}
	if !tx.IsAnswered() {
		t.Fatalf("expected tx.IsAnswered() to be true")
	}
}

func TestRouter_Dispatch_Authorization(t *testing.T) {
	fake := &fakeInteraction{}
	r := callback.NewRouter(zap.NewNop())

	ownerAuthorizer := callback.NewOwnerAuthorizer(999, func() []int64 { return []int64{888} })
	r.SetAuthorizer(ownerAuthorizer)

	executed := false
	r.Register("assistant", "close", func(ctx context.Context, tx *callback.Transaction) error {
		executed = true
		return nil
	})

	ctx := context.Background()

	// Unauthorized caller (user 777)
	txUnauthorized := callback.NewTransaction(
		1, 777,
		callback.ParsedPayload{Namespace: "assistant", Action: "close"},
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 777}, 1, 777, 1),
		fake,
	)
	err := r.Dispatch(ctx, txUnauthorized)
	if !errors.Is(err, callback.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized for user 777, got %v", err)
	}
	if executed {
		t.Fatalf("handler executed for unauthorized user")
	}
	if txUnauthorized.State() != callback.StateFailed {
		t.Fatalf("expected StateFailed on unauthorized transaction")
	}

	// Authorized owner caller (user 999)
	txOwner := callback.NewTransaction(
		2, 999,
		callback.ParsedPayload{Namespace: "assistant", Action: "close"},
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 999}, 1, 999, 1),
		fake,
	)
	err = r.Dispatch(ctx, txOwner)
	if err != nil {
		t.Fatalf("unexpected error for owner: %v", err)
	}
	if !executed {
		t.Fatalf("handler not executed for owner")
	}
	if txOwner.State() != callback.StateCompleted {
		t.Fatalf("expected StateCompleted on successful transaction, got %v", txOwner.State())
	}
}

func TestOwnerAuthorizer_FailClosed(t *testing.T) {
	// When ownerID is 0, any request must be rejected (fail-closed)
	auth := callback.NewOwnerAuthorizer(0, nil)
	ctx := context.Background()

	err := auth.Authorize(ctx, callback.Actor{UserID: 12345}, "start")
	if !errors.Is(err, callback.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized when ownerID=0, got %v", err)
	}

	err = auth.Authorize(ctx, callback.Actor{UserID: 0}, "start")
	if !errors.Is(err, callback.ErrUnauthorized) {
		t.Fatalf("expected ErrUnauthorized when ownerID=0 even if actorID=0, got %v", err)
	}
}

func TestRouter_Dispatch_WildcardAndUnknown(t *testing.T) {
	fake := &fakeInteraction{}
	r := callback.NewRouter(zap.NewNop())

	var capturedAction string
	r.Register("wildcard", "*", func(ctx context.Context, tx *callback.Transaction) error {
		capturedAction = tx.Payload.Action
		return nil
	})

	ctx := context.Background()

	txWildcard := callback.NewTransaction(
		1, 100,
		callback.ParsedPayload{Namespace: "wildcard", Action: "dynamic_tab"},
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 1, 100, 1),
		fake,
	)
	if err := r.Dispatch(ctx, txWildcard); err != nil {
		t.Fatalf("unexpected error on wildcard dispatch: %v", err)
	}
	if capturedAction != "dynamic_tab" {
		t.Fatalf("expected dynamic_tab captured, got %s", capturedAction)
	}

	// Unknown namespace/action
	txUnknown := callback.NewTransaction(
		2, 100,
		callback.ParsedPayload{Namespace: "unknown_ns", Action: "unknown_act"},
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 1, 100, 1),
		fake,
	)
	err := r.Dispatch(ctx, txUnknown)
	if !errors.Is(err, callback.ErrUnknownAction) {
		t.Fatalf("expected ErrUnknownAction, got %v", err)
	}
}
