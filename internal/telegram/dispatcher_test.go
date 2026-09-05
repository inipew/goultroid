package telegram

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/gotd/td/tg"
	"go.uber.org/zap/zaptest"
)

func TestDispatcher_OnNewMessage(t *testing.T) {
	logger := zaptest.NewLogger(t)
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
	logger := zaptest.NewLogger(t)
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
	logger := zaptest.NewLogger(t)
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
	logger := zaptest.NewLogger(t)
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, nil, nil, logger)

	// Nil config
	if _, err := NewClient(nil, dispatcher, logger); err == nil {
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

	client, err := NewClient(cfg, dispatcher, logger)
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
	logger := zaptest.NewLogger(t)
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
