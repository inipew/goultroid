package telegram

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestDispatcher_OnNewMessage(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, []int64{2001})

	var executedCommand string
	var wg sync.WaitGroup
	wg.Add(1)

	cmd := core.Command{
		Name: "test",
		Handler: func(ctx *core.Context) error {
			defer wg.Done()
			executedCommand = ctx.Command
			if !ctx.IsPrivate() {
				t.Errorf("expected private chat")
			}
			if ctx.Sender.ID != 1001 {
				t.Errorf("expected sender ID 1001, got %d", ctx.Sender.ID)
			}
			if ctx.Message.ReplyToID != 99 {
				t.Errorf("expected reply to ID 99, got %d", ctx.Message.ReplyToID)
			}
			return nil
		},
	}
	if err := router.Register(cmd); err != nil {
		t.Fatalf("failed to register test command: %v", err)
	}

	dispatcher := NewDispatcher(router, perms, nil, logger)
	dispatcher.SetSelfID(1001)

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			1001: {ID: 1001, FirstName: "Alice", Username: "alice"},
		},
	}

	update := &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      1,
			Message: ".test",
			PeerID:  &tg.PeerUser{UserID: 1001},
			FromID:  &tg.PeerUser{UserID: 1001},
			ReplyTo: &tg.MessageReplyHeader{ReplyToMsgID: 99},
			Date:    1700000000,
		},
	}

	err := dispatcher.OnNewMessage(context.Background(), entities, update)
	if err != nil {
		t.Fatalf("unexpected error in OnNewMessage: %v", err)
	}

	// Wait for goroutine command execution
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		if executedCommand != "test" {
			t.Errorf("expected executed command 'test', got %q", executedCommand)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for command execution")
	}
}

func TestDispatcher_IgnoredUpdates(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	// Non-message update
	err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.MessageEmpty{},
	})
	if err != nil {
		t.Errorf("expected nil error for empty message")
	}

	// Message without command prefix
	err = dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: "regular text"},
	})
	if err != nil {
		t.Errorf("expected nil error for plain message")
	}

	// Unregistered command
	err = dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: ".unknown"},
	})
	if err != nil {
		t.Errorf("expected nil error for unregistered command")
	}
}

func TestDispatcher_OnNewChannelMessage(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	var wg sync.WaitGroup
	wg.Add(1)

	_ = router.Register(core.Command{
		Name: "channelcmd",
		Handler: func(ctx *core.Context) error {
			defer wg.Done()
			if !ctx.IsGroup() {
				t.Errorf("expected supergroup to report IsGroup")
			}
			return nil
		},
	})

	dispatcher := NewDispatcher(router, nil, nil, logger)

	entities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			555: {ID: 555, Title: "SuperGroup", Megagroup: true},
		},
	}

	update := &tg.UpdateNewChannelMessage{
		Message: &tg.Message{
			ID:      2,
			Message: ".channelcmd",
			PeerID:  &tg.PeerChannel{ChannelID: 555},
		},
	}

	err := dispatcher.OnNewChannelMessage(context.Background(), entities, update)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wg.Wait()
}

func TestNewClient_Validation(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	// Nil config
	if _, err := NewClient(nil, dispatcher, nil, logger); err == nil {
		t.Errorf("expected error with nil config")
	}

	// Valid config
	tmpDir := t.TempDir()
	sessionPath := filepath.Join(tmpDir, "sub", "session.json")
	cfg := &config.Config{
		AppID:       12345,
		AppHash:     "hash",
		Phone:       "+123",
		SessionFile: sessionPath,
	}

	client, err := NewClient(cfg, dispatcher, nil, logger)
	if err != nil {
		t.Fatalf("unexpected error creating client: %v", err)
	}
	if client == nil {
		t.Fatal("expected client instance, got nil")
	}

	// Check that session directory was created
	if _, err := os.Stat(filepath.Dir(sessionPath)); os.IsNotExist(err) {
		t.Errorf("expected session directory to be created")
	}
}

func TestDispatcher_MessageHandler(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	called := false
	var gotCmd bool
	var gotCmdName string

	dispatcher.AddMessageHandler(func(ctx context.Context, e tg.Entities, msg *tg.Message, isCommand bool, cmdName string) error {
		called = true
		gotCmd = isCommand
		gotCmdName = cmdName
		return nil
	})

	// Non-command message
	err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: "hello world"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called || gotCmd {
		t.Errorf("expected non-command handler called")
	}

	// Command message
	called = false
	var wg sync.WaitGroup
	wg.Add(1)
	_ = router.Register(core.Command{Name: "ping", Handler: func(ctx *core.Context) error {
		defer wg.Done()
		return nil
	}})
	err = dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: ".ping"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wg.Wait()
	if !called || !gotCmd || gotCmdName != "ping" {
		t.Errorf("expected command handler called with ping, got called=%v cmd=%v name=%s", called, gotCmd, gotCmdName)
	}
}

func TestDispatcher_InterceptorPanicIsolation(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	panickingRan := false
	subsequentRan := false

	// Interceptor 1: panics
	dispatcher.AddMessageHandler(func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
		panickingRan = true
		panic("interceptor fatal bug")
	})

	// Interceptor 2: must still run despite panic in interceptor 1
	dispatcher.AddMessageHandler(func(ctx context.Context, e tg.Entities, msg *tg.Message, isCmd bool, cmdName string) error {
		subsequentRan = true
		return nil
	})

	err := dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: "test message"},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !panickingRan {
		t.Errorf("expected panicking interceptor to run")
	}
	if !subsequentRan {
		t.Errorf("expected subsequent interceptor to run despite previous panic")
	}
}

func TestDispatcher_RootContextCancellation(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	rootCtx, rootCancel := context.WithCancel(context.Background())
	dispatcher.SetRootContext(rootCtx)

	cmdStarted := make(chan struct{})
	cmdCanceled := make(chan struct{})

	_ = router.Register(core.Command{
		Name: "longcmd",
		Handler: func(c *core.Context) error {
			close(cmdStarted)
			<-c.Ctx.Done()
			close(cmdCanceled)
			return c.Ctx.Err()
		},
	})

	_ = dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{Message: ".longcmd"},
	})

	select {
	case <-cmdStarted:
	case <-time.After(1 * time.Second):
		t.Fatal("timed out waiting for command to start")
	}

	// Cancel root application context
	rootCancel()

	select {
	case <-cmdCanceled:
		// Success! Command context was canceled when rootCtx was canceled
	case <-time.After(1 * time.Second):
		t.Fatal("command context was not canceled when rootCtx was canceled")
	}
}

func TestDispatcher_OnBotCallbackQuery_MessageTarget(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	bus := core.NewEventBus()
	defer bus.Close()
	dispatcher.SetEventBus(bus)

	var receivedEvt *core.CallbackQueryEvent
	var wg sync.WaitGroup
	wg.Add(1)

	unsub := bus.Subscribe(core.EventTypeCallbackQuery, func(e core.Event) {
		if evt, ok := e.(*core.CallbackQueryEvent); ok {
			receivedEvt = evt
			wg.Done()
		}
	})
	defer unsub()

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			1001: {ID: 1001, AccessHash: 99999},
		},
	}

	update := &tg.UpdateBotCallbackQuery{
		QueryID:      777,
		UserID:       1001,
		Peer:         &tg.PeerUser{UserID: 1001},
		MsgID:        42,
		ChatInstance: 8888,
		Data:         []byte("noop"),
	}

	err := dispatcher.OnBotCallbackQuery(context.Background(), entities, update)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wg.Wait()

	if receivedEvt == nil {
		t.Fatal("expected CallbackQueryEvent to be published")
	}
	if receivedEvt.Origin != core.CallbackOriginMessage {
		t.Errorf("expected Origin Message, got %v", receivedEvt.Origin)
	}
	if receivedEvt.Target.Origin != core.CallbackOriginMessage {
		t.Errorf("expected Target Origin Message, got %v", receivedEvt.Target.Origin)
	}
	if receivedEvt.Target.MessageID != 42 {
		t.Errorf("expected MessageID 42, got %d", receivedEvt.Target.MessageID)
	}
	if receivedEvt.Target.ChatInstance != 8888 {
		t.Errorf("expected ChatInstance 8888, got %d", receivedEvt.Target.ChatInstance)
	}
	ipu, ok := receivedEvt.Target.Peer.(*tg.InputPeerUser)
	if !ok || ipu.UserID != 1001 || ipu.AccessHash != 99999 {
		t.Errorf("expected InputPeerUser with access hash 99999, got %+v", receivedEvt.Target.Peer)
	}
}

func TestDispatcher_OnInlineBotCallbackQuery_InlineTarget(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	bus := core.NewEventBus()
	defer bus.Close()
	dispatcher.SetEventBus(bus)

	var receivedEvt *core.CallbackQueryEvent
	var wg sync.WaitGroup
	wg.Add(1)

	unsub := bus.Subscribe(core.EventTypeCallbackQuery, func(e core.Event) {
		if evt, ok := e.(*core.CallbackQueryEvent); ok {
			receivedEvt = evt
			wg.Done()
		}
	})
	defer unsub()

	inlineID := &tg.InputBotInlineMessageID64{DCID: 2, ID: 1002, AccessHash: 55555}
	update := &tg.UpdateInlineBotCallbackQuery{
		QueryID:      888,
		UserID:       2002,
		MsgID:        inlineID,
		ChatInstance: 9999,
		Data:         []byte("noop"),
	}

	err := dispatcher.OnInlineBotCallbackQuery(context.Background(), tg.Entities{}, update)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wg.Wait()

	if receivedEvt == nil {
		t.Fatal("expected CallbackQueryEvent to be published")
	}
	if receivedEvt.Origin != core.CallbackOriginInline {
		t.Errorf("expected Origin Inline, got %v", receivedEvt.Origin)
	}
	if receivedEvt.Target.Origin != core.CallbackOriginInline {
		t.Errorf("expected Target Origin Inline, got %v", receivedEvt.Target.Origin)
	}
	if receivedEvt.Target.InlineID == nil {
		t.Fatalf("expected Target InlineID to be preserved")
	}
	if receivedEvt.Target.ChatInstance != 9999 {
		t.Errorf("expected ChatInstance 9999, got %d", receivedEvt.Target.ChatInstance)
	}
}

func TestDispatcher_OnBotInlineSend_FeedbackEvent(t *testing.T) {
	logger := zap.NewNop()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	bus := core.NewEventBus()
	defer bus.Close()
	dispatcher.SetEventBus(bus)

	var receivedEvt *core.InlineResultChosenEvent
	var wg sync.WaitGroup
	wg.Add(1)

	unsub := bus.Subscribe(core.EventTypeInlineChosen, func(e core.Event) {
		if evt, ok := e.(*core.InlineResultChosenEvent); ok {
			receivedEvt = evt
			wg.Done()
		}
	})
	defer unsub()

	inlineID := &tg.InputBotInlineMessageID64{DCID: 1, ID: 5005, AccessHash: 12345}
	update := &tg.UpdateBotInlineSend{
		UserID: 3003,
		Query:  "ping",
		ID:     "result-ping-1",
		MsgID:  inlineID,
	}

	err := dispatcher.OnBotInlineSend(context.Background(), tg.Entities{}, update)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	wg.Wait()

	if receivedEvt == nil {
		t.Fatal("expected InlineResultChosenEvent to be published")
	}
	if receivedEvt.UserID != 3003 || receivedEvt.Query != "ping" || receivedEvt.ResultID != "result-ping-1" {
		t.Errorf("unexpected received feedback event: %+v", receivedEvt)
	}
	if receivedEvt.InlineID == nil {
		t.Errorf("expected InlineID to be preserved")
	}
}
