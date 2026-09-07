package command_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/assistant/presentation"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"go.uber.org/zap"
)

type fakeInteraction struct {
	lastSentText   string
	lastSentMarkup tg.ReplyMarkupClass
	lastEditedText string
	lastDeletedIDs []int
}

func (f *fakeInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}
func (f *fakeInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	f.lastEditedText = text
	f.lastSentMarkup = markup
	return nil
}
func (f *fakeInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	f.lastSentMarkup = markup
	return nil
}
func (f *fakeInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	f.lastDeletedIDs = append(f.lastDeletedIDs, target.MessageID())
	return nil
}
func (f *fakeInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: target.MessageID()}, nil
}
func (f *fakeInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	f.lastSentText = text
	f.lastSentMarkup = markup
	return &tg.Message{ID: 100}, nil
}

func TestCommandRouter_Dispatch(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	startTime := time.Now().Add(-10 * time.Minute)
	command.AttachDefaultCommands(r, func() string { return "TestBot" }, func() time.Time { return startTime }, presentation.RenderScreen)

	coreRouter := core.NewRouter(".")
	_ = coreRouter.RegisterBatch([]core.Command{
		{
			Name:     "ping",
			Surfaces: execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				return c.Reply("🏓 Pong!")
			},
		},
		{
			Name:     "alive",
			Surfaces: execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				return c.Reply("🟢 Online")
			},
		},
	})
	r.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	// 1. /start with bot suffix
	err := r.Dispatch(ctx, 12345, peer, "/start@TestBot", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /start: %v", err)
	}
	if fake.lastSentMarkup == nil {
		t.Fatalf("expected markup returned on /start")
	}

	// 2. /ping
	err = r.Dispatch(ctx, 12345, peer, "/ping", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /ping: %v", err)
	}
	if fake.lastSentText == "" {
		t.Fatalf("expected text reply on /ping")
	}

	// 3. /alive
	err = r.Dispatch(ctx, 12345, peer, "/alive", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching /alive: %v", err)
	}
	if fake.lastSentText == "" {
		t.Fatalf("expected text reply on /alive")
	}

	// 4. Unknown command
	err = r.Dispatch(ctx, 12345, peer, "/unknown_cmd", fake)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("expected ErrUnknownCommand, got %v", err)
	}

	// 5. Non-command text should be silently ignored (return nil)
	err = r.Dispatch(ctx, 12345, peer, "hello bot", fake)
	if err != nil {
		t.Fatalf("expected non-command to return nil, got %v", err)
	}
}

func TestCommandRouter_CoreRouterDispatch(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	fake := &fakeInteraction{}
	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	called := false
	coreRouter := core.NewRouter(".")
	_ = coreRouter.Register(core.Command{
		Name:     "customplugin",
		Surfaces: execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			called = true
			return nil
		},
	})
	_ = coreRouter.Register(core.Command{
		Name:     "useronly",
		Surfaces: execution.SurfaceUserbot,
		Handler: func(c *core.Context) error {
			return nil
		},
	})
	r.SetCoreRouter(coreRouter)

	err := r.Dispatch(ctx, 12345, peer, "/customplugin arg1", fake)
	if err != nil {
		t.Fatalf("unexpected error dispatching command: %v", err)
	}
	if !called {
		t.Fatalf("expected command handler to be called")
	}

	// Verify command not available on assistant surface
	err = r.Dispatch(ctx, 12345, peer, "/useronly", fake)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("expected ErrUnknownCommand for user-only command on assistant, got %v", err)
	}
}

func TestCommandRouter_CoreRouterPrecedenceOverLocal(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	fake := &fakeInteraction{}
	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	localCalled := false
	r.Register("/ping", func(c *command.Context) error {
		localCalled = true
		return nil
	})

	pluginCalled := false
	coreRouter := core.NewRouter(".")
	_ = coreRouter.Register(core.Command{
		Name:     "ping",
		Surfaces: execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			pluginCalled = true
			return nil
		},
	})
	r.SetCoreRouter(coreRouter)

	err := r.Dispatch(ctx, 12345, peer, "/ping", fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !pluginCalled {
		t.Fatal("expected canonical command to be called")
	}
	if localCalled {
		t.Fatal("expected local command NOT to be called when canonical command is present")
	}
}

func TestCommandRouter_Permissions(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	ownerID := int64(1001)
	sudoID := int64(2002)
	regularID := int64(3003)

	r.SetOwner(ownerID, func() []int64 {
		return []int64{sudoID}
	})

	adminCalled := false
	sudoCalled := false
	everyoneCalled := false

	coreRouter := core.NewRouter(".")
	_ = coreRouter.RegisterBatch([]core.Command{
		{
			Name:       "admincmd",
			Permission: core.PermissionOwner,
			Surfaces:   execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				adminCalled = true
				return nil
			},
		},
		{
			Name:       "sudocmd",
			Permission: core.PermissionSudo,
			Surfaces:   execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				sudoCalled = true
				return nil
			},
		},
		{
			Name:       "allcmd",
			Permission: core.PermissionEveryone,
			Surfaces:   execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				everyoneCalled = true
				return nil
			},
		},
	})
	r.SetCoreRouter(coreRouter)

	ctx := context.Background()
	fake := &fakeInteraction{}

	// 1. Regular user calling owner-only command -> denied
	peerRegular := &tg.InputPeerUser{UserID: regularID}
	_ = r.Dispatch(ctx, regularID, peerRegular, "/admincmd", fake)
	if adminCalled {
		t.Fatal("expected regular user to be blocked from admincmd")
	}
	if fake.lastSentText == "" {
		t.Fatal("expected rejection message for admincmd")
	}

	// 2. Regular user calling sudo command -> denied
	fake.lastSentText = ""
	_ = r.Dispatch(ctx, regularID, peerRegular, "/sudocmd", fake)
	if sudoCalled {
		t.Fatal("expected regular user to be blocked from sudocmd")
	}
	if fake.lastSentText == "" {
		t.Fatal("expected rejection message for sudocmd")
	}

	// 3. Regular user calling everyone command -> allowed
	_ = r.Dispatch(ctx, regularID, peerRegular, "/allcmd", fake)
	if !everyoneCalled {
		t.Fatal("expected regular user to be allowed on allcmd")
	}

	// 4. Sudo user calling sudocmd -> allowed
	peerSudo := &tg.InputPeerUser{UserID: sudoID}
	_ = r.Dispatch(ctx, sudoID, peerSudo, "/sudocmd", fake)
	if !sudoCalled {
		t.Fatal("expected sudo user to be allowed on sudocmd")
	}

	// 5. Owner calling admincmd -> allowed
	peerOwner := &tg.InputPeerUser{UserID: ownerID}
	_ = r.Dispatch(ctx, ownerID, peerOwner, "/admincmd", fake)
	if !adminCalled {
		t.Fatal("expected owner to be allowed on admincmd")
	}
}

func TestCommandRouter_ContextMessaging(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	ownerID := int64(1001)
	r.SetOwner(ownerID, nil)

	var recordedSource core.ExecutionSource
	var isAssistant bool
	var recordedSenderID int64
	var recordedChatID int64

	coreRouter := core.NewRouter(".")
	_ = coreRouter.Register(core.Command{
		Name:       "echotest",
		Permission: core.PermissionEveryone,
		Surfaces:   execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			recordedSource = c.Source
			isAssistant = c.IsAssistant()
			recordedSenderID = c.SenderID()
			recordedChatID = c.ChatID()

			// Test Reply
			if err := c.Reply("step 1: replying"); err != nil {
				return err
			}
			// Test EditOrReply (which should edit step 1)
			if err := c.EditOrReply("step 2: edited"); err != nil {
				return err
			}
			return nil
		},
	})
	r.SetCoreRouter(coreRouter)

	ctx := context.Background()
	fake := &fakeInteraction{}
	peer := &tg.InputPeerUser{UserID: ownerID}

	err := r.Dispatch(ctx, ownerID, peer, "/echotest some args", fake)
	if err != nil {
		t.Fatalf("unexpected dispatch error: %v", err)
	}

	if recordedSource != core.ExecutionAssistant {
		t.Errorf("expected ExecutionAssistant source, got %v", recordedSource)
	}
	if !isAssistant {
		t.Errorf("expected c.IsAssistant() to be true")
	}
	if recordedSenderID != ownerID {
		t.Errorf("expected senderID %d, got %d", ownerID, recordedSenderID)
	}
	if recordedChatID != ownerID {
		t.Errorf("expected chatID %d, got %d", ownerID, recordedChatID)
	}
	if fake.lastSentText != "step 1: replying" {
		t.Errorf("expected lastSentText 'step 1: replying', got %q", fake.lastSentText)
	}
	if fake.lastEditedText != "step 2: edited" {
		t.Errorf("expected lastEditedText 'step 2: edited', got %q", fake.lastEditedText)
	}
}

func TestCommandRouter_CoreRouterDirect(t *testing.T) {
	coreRouter := core.NewRouter(".")
	err := coreRouter.Register(core.Command{
		Name:     "coreping",
		Surfaces: execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			return c.Reply("pong from core")
		},
	})
	if err != nil {
		t.Fatalf("failed to register core command: %v", err)
	}

	r := command.NewRouter(zap.NewNop())
	r.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	ctx := context.Background()
	peer := &tg.InputPeerUser{UserID: 12345}

	err = r.Dispatch(ctx, 12345, peer, "/coreping", fake)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if fake.lastSentText != "pong from core" {
		t.Fatalf("expected 'pong from core', got %q", fake.lastSentText)
	}
}


