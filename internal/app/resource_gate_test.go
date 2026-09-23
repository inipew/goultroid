package app

import (
	"context"
	"errors"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	asstcmd "github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/config"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/scheduler"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

type gateTicket struct {
	id   tasks.TaskID
	res  tasks.TaskResult
	done chan struct{}
}

func (t *gateTicket) TaskID() tasks.TaskID                           { return t.id }
func (t *gateTicket) State() tasks.TaskState                         { return tasks.StateCompleted }
func (t *gateTicket) Done() <-chan struct{}                          { return t.done }
func (t *gateTicket) Result() (tasks.TaskResult, bool)               { return t.res, true }
func (t *gateTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.res, nil }

type gateTaskClient struct {
	mu          sync.Mutex
	submitted   []tasks.WorkSpec
	execute     bool
	returnError error
}

func (c *gateTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.mu.Lock()
	if c.returnError != nil {
		err := c.returnError
		c.mu.Unlock()
		return nil, err
	}
	c.submitted = append(c.submitted, spec)
	execute := c.execute
	c.mu.Unlock()

	res := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if execute && spec.Handler != nil {
		if err := spec.Handler(ctx); err != nil {
			res.Outcome = tasks.OutcomeFailed
			res.Failure.Message = err.Error()
		}
	}
	done := make(chan struct{})
	close(done)
	if spec.OnComplete != nil {
		spec.OnComplete(res)
	}
	return &gateTicket{id: spec.ID, res: res, done: done}, nil
}

func (c *gateTaskClient) Cancel(id tasks.TaskID, cause tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Accepted: true, Reason: cause}, nil
}
func (c *gateTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *gateTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func (c *gateTaskClient) LastSpec() (tasks.WorkSpec, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.submitted) == 0 {
		return tasks.WorkSpec{}, false
	}
	return c.submitted[len(c.submitted)-1], true
}

func (c *gateTaskClient) Count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.submitted)
}

func (c *gateTaskClient) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.submitted = nil
}

type gateTelegramService struct {
	core.TelegramServicer
}

func (s *gateTelegramService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}
func (s *gateTelegramService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (s *gateTelegramService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return &tg.Message{ID: msgID}, nil
}
func (s *gateTelegramService) IsBotSent(id int) bool { return false }

type gateInteraction struct{}

func (g *gateInteraction) Answer(ctx context.Context, queryID int64, text string, alert bool) error {
	return nil
}
func (g *gateInteraction) Edit(ctx context.Context, target interaction.MessageTarget, text string, markup tg.ReplyMarkupClass) error {
	return nil
}
func (g *gateInteraction) EditMarkup(ctx context.Context, target interaction.MessageTarget, markup tg.ReplyMarkupClass) error {
	return nil
}
func (g *gateInteraction) Delete(ctx context.Context, target interaction.MessageTarget) error {
	return nil
}
func (g *gateInteraction) GetMessage(ctx context.Context, target interaction.MessageTarget) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}
func (g *gateInteraction) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}
func (g *gateInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	return &tg.Message{ID: 100}, nil
}

func createTestApp(t *testing.T) *App {
	t.Helper()
	tmpDir := t.TempDir()
	cfg := &config.Config{
		OwnerID:      1001,
		AppID:        123456,
		AppHash:      "hash123",
		Phone:        "+628123456789",
		BotToken:     "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11",
		SessionFile:  filepath.Join(tmpDir, "session.json"),
		DatabasePath: filepath.Join(tmpDir, "test.db"),
		Prefix:       ".",
		LogLevel:     "error",
	}
	app, err := New(cfg)
	if err != nil {
		t.Fatalf("failed to create test app: %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })
	return app
}

// TestCommandResourceTaskEngineGate_AllBuiltinCommands asserts that every single
// command across builtin modules with Resources != nil reaches TaskEngine with
// its declared resources on all permitted surfaces before execution, and fails
// closed if TaskEngine is unconfigured.
func TestCommandResourceTaskEngineGate_AllBuiltinCommands(t *testing.T) {
	app := createTestApp(t)

	commands := app.router.All()
	var resourceCommands []core.Command
	for _, cmd := range commands {
		if len(cmd.Resources) > 0 {
			resourceCommands = append(resourceCommands, cmd)
		}
	}

	if len(resourceCommands) == 0 {
		t.Fatal("expected at least one command declaring Resources in builtinModules")
	}
	t.Logf("found %d resource-bearing commands to verify across surfaces", len(resourceCommands))

	// This architecture gate verifies admission and WorkSpec propagation only.
	// Handlers are deliberately withheld so command-specific argument/business
	// validation cannot affect the cross-surface TaskEngine contract.
	client := &gateTaskClient{execute: false}
	tgSvc := &gateTelegramService{}
	perms := core.NewPermissions(1001, []int64{1001})

	// 1. Userbot surface dispatcher
	dispDeps := telegram.DispatcherDeps{
		Router:      app.router,
		Permissions: perms,
		Service:     tgSvc,
		Logger:      zap.NewNop(),
		Tasks:       client,
	}
	disp, err := telegram.NewDispatcherWithDeps(dispDeps)
	if err != nil {
		t.Fatalf("failed to create userbot dispatcher: %v", err)
	}
	disp.SetSelfID(1001)

	// 2. Assistant surface router
	asstRouter := asstcmd.NewRouter(zap.NewNop())
	asstRouter.SetCoreRouter(app.router)
	asstRouter.SetOwner(1001, func() []int64 { return []int64{1001} })
	asstRouter.SetTasks(client)

	// 3. Scheduled action handler
	schedHandler := scheduledActionHandler{
		router:   app.router,
		executor: core.NewCommandExecutor(zap.NewNop(), core.NewCooldownTracker(), 30*time.Second),
		perms:    perms,
		service:  func() core.TelegramServicer { return tgSvc },
		tasks:    client,
	}

	for commandIndex, cmd := range resourceCommands {
		cmdName := cmd.Name

		// Test 1: Surface Userbot
		if cmd.IsAvailableOn(execution.SourceUserbot) {
			t.Run("Userbot/"+cmdName, func(t *testing.T) {
				client.Clear()
				disp.SetTasks(client)

				entities := tg.Entities{
					Users: map[int64]*tg.User{
						1001: {ID: 1001, FirstName: "Owner", Username: "owner"},
					},
				}
				newUpdate := func(id int) *tg.UpdateNewMessage {
					return &tg.UpdateNewMessage{
						Message: &tg.Message{
							ID:      id,
							Message: "." + cmdName,
							PeerID:  &tg.PeerUser{UserID: 1001},
							FromID:  &tg.PeerUser{UserID: 1001},
							Date:    int(time.Now().Unix()),
							Out:     true,
						},
					}
				}
				update := newUpdate(10 + commandIndex*2)

				if err := disp.OnNewMessage(context.Background(), entities, update); err != nil {
					t.Fatalf("unexpected error in OnNewMessage: %v", err)
				}

				spec, ok := client.LastSpec()
				if !ok {
					t.Fatalf("command %q did NOT submit work to TaskEngine on Userbot surface", cmdName)
				}
				if !reflect.DeepEqual(spec.Resources, cmd.Resources) {
					t.Errorf("command %q resources mismatch on Userbot: got %+v, want %+v", cmdName, spec.Resources, cmd.Resources)
				}
				if spec.Pool != "interactive" || spec.Class != tasks.PriorityInteractive {
					t.Errorf("command %q execution class mismatch on Userbot: pool=%s, class=%s", cmdName, spec.Pool, spec.Class)
				}

				// Fail-closed verification
				disp.SetTasks(nil)
				client.Clear()
				_ = disp.OnNewMessage(context.Background(), entities, newUpdate(11+commandIndex*2))
				if client.Count() != 0 {
					t.Fatalf("command %q submitted task even when task client was nil", cmdName)
				}
			})
		}

		// Test 2: Surface Assistant
		if cmd.IsAvailableOn(execution.SourceAssistant) {
			t.Run("Assistant/"+cmdName, func(t *testing.T) {
				client.Clear()
				asstRouter.SetTasks(client)

				err := asstRouter.DispatchMessageContext(context.Background(), 1001, &tg.InputPeerUser{UserID: 1001}, "/"+cmdName, asstcmd.MessageContext{Chat: core.Chat{ID: 1001, Type: string(core.ChatKindPrivate)}}, &gateInteraction{})
				if err != nil {
					t.Fatalf("unexpected dispatch error: %v", err)
				}

				spec, ok := client.LastSpec()
				if !ok {
					t.Fatalf("command %q did NOT submit work to TaskEngine on Assistant surface", cmdName)
				}
				if !reflect.DeepEqual(spec.Resources, cmd.Resources) {
					t.Errorf("command %q resources mismatch on Assistant: got %+v, want %+v", cmdName, spec.Resources, cmd.Resources)
				}
				if spec.Pool != "interactive" || spec.Class != tasks.PriorityInteractive {
					t.Errorf("command %q execution class mismatch on Assistant: pool=%s, class=%s", cmdName, spec.Pool, spec.Class)
				}

				// Fail-closed verification
				asstRouter.SetTasks(nil)
				client.Clear()
				fcErr := asstRouter.DispatchMessageContext(context.Background(), 1001, &tg.InputPeerUser{UserID: 1001}, "/"+cmdName, asstcmd.MessageContext{Chat: core.Chat{ID: 1001, Type: string(core.ChatKindPrivate)}}, &gateInteraction{})
				if !errors.Is(fcErr, asstcmd.ErrTasksNotConfigured) {
					t.Fatalf("command %q failed to fail-closed on Assistant without TaskEngine: %v", cmdName, fcErr)
				}
			})
		}

		// Test 3: Surface Scheduled
		t.Run("Scheduled/"+cmdName, func(t *testing.T) {
			client.Clear()
			schedHandler.tasks = client

			job := scheduler.ScheduledJob{
				ID:         100,
				ActionType: scheduler.ActionCommand,
				Payload:    "." + cmdName,
				ChatID:     1001,
				PeerType:   "user",
				CreatedBy:  1001,
			}
			if err := schedHandler.executeCommand(context.Background(), job); err != nil {
				t.Fatalf("unexpected error executing scheduled command: %v", err)
			}

			spec, ok := client.LastSpec()
			if !ok {
				t.Fatalf("command %q did NOT submit work to TaskEngine on Scheduled surface", cmdName)
			}
			if !reflect.DeepEqual(spec.Resources, cmd.Resources) {
				t.Errorf("command %q resources mismatch on Scheduled: got %+v, want %+v", cmdName, spec.Resources, cmd.Resources)
			}
			if spec.Class != tasks.PriorityMaintenance {
				t.Errorf("command %q expected priority maintenance on Scheduled, got %s", cmdName, spec.Class)
			}
			expectedPool := scheduledCommandPool(cmd.Resources)
			if spec.Pool != expectedPool {
				t.Errorf("command %q expected pool %s on Scheduled, got %s", cmdName, expectedPool, spec.Pool)
			}

			// Fail-closed verification
			schedHandler.tasks = nil
			client.Clear()
			fcErr := schedHandler.executeCommand(context.Background(), job)
			if fcErr == nil {
				t.Fatalf("command %q failed to fail-closed on Scheduled without TaskEngine", cmdName)
			}
		})
	}
}

// TestCommandResourceTaskEngineGate_HandlerRunsStrictlyInsideTaskEngine verifies
// that when a command declares resources, the underlying command Handler NEVER runs
// unless TaskEngine admits it and runs spec.Handler.
func TestCommandResourceTaskEngineGate_HandlerRunsStrictlyInsideTaskEngine(t *testing.T) {
	perms := core.NewPermissions(1001, []int64{1001})
	tgSvc := &gateTelegramService{}

	handlerRan := false
	probeCmd := core.Command{
		Name:      "proberesource",
		Surfaces:  execution.SurfaceUserbot | execution.SurfaceAssistant,
		Resources: []tasks.ResourceRequirement{{Name: "media", Amount: 1}},
		Handler: func(ctx *core.Context) error {
			handlerRan = true
			return nil
		},
	}

	testRouter := core.NewRouter(".")
	if err := testRouter.Register(probeCmd); err != nil {
		t.Fatal(err)
	}

	client := &gateTaskClient{execute: false} // TaskEngine does NOT run spec.Handler

	disp, err := telegram.NewDispatcherWithDeps(telegram.DispatcherDeps{
		Router:      testRouter,
		Permissions: perms,
		Service:     tgSvc,
		Logger:      zap.NewNop(),
		Tasks:       client,
	})
	if err != nil {
		t.Fatal(err)
	}
	disp.SetSelfID(1001)

	asstRouter := asstcmd.NewRouter(zap.NewNop())
	asstRouter.SetCoreRouter(testRouter)
	asstRouter.SetOwner(1001, func() []int64 { return []int64{1001} })
	asstRouter.SetTasks(client)

	schedHandler := scheduledActionHandler{
		router:   testRouter,
		executor: core.NewCommandExecutor(zap.NewNop(), core.NewCooldownTracker(), 30*time.Second),
		perms:    perms,
		service:  func() core.TelegramServicer { return tgSvc },
		tasks:    client,
	}

	// 1. Userbot: client intercepts and does not run Handler
	handlerRan = false
	entities := tg.Entities{Users: map[int64]*tg.User{1001: {ID: 1001}}}
	update := &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID: 20, Message: ".proberesource", PeerID: &tg.PeerUser{UserID: 1001},
			FromID: &tg.PeerUser{UserID: 1001}, Date: int(time.Now().Unix()), Out: true,
		},
	}
	_ = disp.OnNewMessage(context.Background(), entities, update)
	if handlerRan {
		t.Fatal("handler ran on Userbot despite TaskEngine withholding execution!")
	}

	// 2. Assistant: client intercepts and does not run Handler
	handlerRan = false
	_ = asstRouter.DispatchMessageContext(context.Background(), 1001, &tg.InputPeerUser{UserID: 1001}, "/proberesource", asstcmd.MessageContext{Chat: core.Chat{ID: 1001, Type: string(core.ChatKindPrivate)}}, &gateInteraction{})
	if handlerRan {
		t.Fatal("handler ran on Assistant despite TaskEngine withholding execution!")
	}

	// 3. Scheduled: client intercepts and does not run Handler
	handlerRan = false
	_ = schedHandler.executeCommand(context.Background(), scheduler.ScheduledJob{ID: 200, Payload: ".proberesource", CreatedBy: 1001})
	if handlerRan {
		t.Fatal("handler ran on Scheduled despite TaskEngine withholding execution!")
	}

	// 4. Verification when TaskEngine client returns admission rejection error
	client.returnError = errors.New("admission quota exceeded")

	// Userbot rejection
	handlerRan = false
	_ = disp.OnNewMessage(context.Background(), entities, update)
	if handlerRan {
		t.Fatal("handler ran on Userbot despite TaskEngine admission rejection error!")
	}

	// Assistant rejection
	handlerRan = false
	err = asstRouter.DispatchMessageContext(context.Background(), 1001, &tg.InputPeerUser{UserID: 1001}, "/proberesource", asstcmd.MessageContext{Chat: core.Chat{ID: 1001, Type: string(core.ChatKindPrivate)}}, &gateInteraction{})
	if err == nil || !errors.Is(err, client.returnError) {
		t.Fatalf("expected admission error on Assistant, got %v", err)
	}
	if handlerRan {
		t.Fatal("handler ran on Assistant despite TaskEngine admission rejection error!")
	}

	// Scheduled rejection
	handlerRan = false
	err = schedHandler.executeCommand(context.Background(), scheduler.ScheduledJob{ID: 201, Payload: ".proberesource", CreatedBy: 1001})
	if err == nil || !errors.Is(err, client.returnError) {
		t.Fatalf("expected admission error on Scheduled, got %v", err)
	}
	if handlerRan {
		t.Fatal("handler ran on Scheduled despite TaskEngine admission rejection error!")
	}
}

// TestDownloaderCommandResourceStaticAndDynamicInvariants verifies that the
// interactive .download command is planning-only. Heavy download/process
// resources belong to the continuation task; downloader package tests cover
// the exact continuation resource set for direct HTTP and extractor providers.
func TestDownloaderCommandResourceStaticAndDynamicInvariants(t *testing.T) {
	app := createTestApp(t)

	cmd, found := app.router.Find("download")
	if !found {
		t.Fatal("expected .download command in app router")
	}
	if len(cmd.Resources) != 0 {
		t.Fatalf(".download planning command must not statically reserve heavy resources: %+v", cmd.Resources)
	}

	// Verify provider selection that drives continuation resource planning.
	if app.downloadRegistry == nil {
		t.Fatal("expected downloadRegistry configured on app")
	}
	directProv := app.downloadRegistry.Resolve("https://example.com/file.mp4")
	if directProv == nil || directProv.Name() == "extractor" {
		t.Fatalf("direct HTTP URL should resolve to non-extractor provider, got: %v", directProv)
	}

	ytProv := app.downloadRegistry.Resolve("https://www.youtube.com/watch?v=dQw4w9WgXcQ")
	if ytProv == nil || ytProv.Name() != "extractor" {
		t.Fatalf("youtube URL should resolve to extractor provider, got: %v", ytProv)
	}
}
