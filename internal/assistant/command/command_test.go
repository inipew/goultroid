package command_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/command"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
)

type fakeInteraction struct {
	lastSentText     string
	lastSentMarkup   tg.ReplyMarkupClass
	lastEditedText   string
	lastDeletedIDs   []int
	lastMediaType    string
	lastMediaPath    string
	lastMediaCaption string
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
func (f *fakeInteraction) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if _, err := os.Stat(filePath); err != nil {
		return nil, err
	}
	f.lastMediaType = mediaType
	f.lastMediaPath = filePath
	f.lastMediaCaption = caption
	return &tg.Message{ID: 101}, nil
}

type savedResponseResolver struct {
	response savedresponse.Response
}

func (r *savedResponseResolver) ResolveSavedResponse(context.Context, savedresponse.Reference) (savedresponse.Response, bool, error) {
	return r.response, true, nil
}

type immediateTaskTicket struct {
	id     tasks.TaskID
	result tasks.TaskResult
	done   chan struct{}
}

func (t *immediateTaskTicket) TaskID() tasks.TaskID { return t.id }
func (t *immediateTaskTicket) State() tasks.TaskState {
	if t.result.IsSuccess() {
		return tasks.StateCompleted
	}
	return tasks.StateFailed
}
func (t *immediateTaskTicket) Done() <-chan struct{} { return t.done }
func (t *immediateTaskTicket) Result() (tasks.TaskResult, bool) {
	return t.result, true
}
func (t *immediateTaskTicket) Wait(ctx context.Context) (tasks.TaskResult, error) {
	select {
	case <-ctx.Done():
		return tasks.TaskResult{}, ctx.Err()
	case <-t.done:
		return t.result, nil
	}
}

type immediateTaskClient struct {
	last        tasks.WorkSpec
	submitCount int
	submitErr   error
	beforeRun   func(tasks.WorkSpec)
}

func (c *immediateTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.last = spec
	c.submitCount++
	if c.submitErr != nil {
		return nil, c.submitErr
	}
	if err := spec.Validate(); err != nil {
		return nil, err
	}
	if c.beforeRun != nil {
		c.beforeRun(spec)
	}
	started := time.Now()
	runErr := spec.Handler(ctx)
	result := tasks.TaskResult{
		TaskID:     spec.ID,
		Outcome:    tasks.OutcomeCompleted,
		Cause:      tasks.CauseNone,
		StartedAt:  started,
		FinishedAt: time.Now(),
	}
	if runErr != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure.Message = runErr.Error()
	}
	done := make(chan struct{})
	close(done)
	return &immediateTaskTicket{id: spec.ID, result: result, done: done}, nil
}

func (c *immediateTaskClient) Cancel(id tasks.TaskID, reason tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{TaskID: id, Reason: reason}, nil
}
func (c *immediateTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (c *immediateTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func newSavedResponseCommandFixture(
	t *testing.T,
	alias string,
	response savedresponse.Response,
) (*savedresponse.BindingService, *savedresponse.Registry, *savedresponse.Registration, tasks.ScopeIdentity) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, savedresponse.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}

	registry := savedresponse.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1}
	registration, err := registry.Register("notes", scope, &savedResponseResolver{response: response})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(registration.Close)

	service := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		registry,
	)
	if _, err := service.Create(context.Background(), savedresponse.SurfaceBinding{
		Surface:   savedresponse.SurfaceAssistantCommand,
		Alias:     alias,
		Reference: savedresponse.Reference{Provider: "notes", ScopeID: 42, Key: alias},
		Enabled:   true,
	}); err != nil {
		t.Fatal(err)
	}
	return service, registry, registration, scope
}

func configureSavedResponseRouter(
	r *command.Router,
	bindings *savedresponse.BindingService,
	tasksClient tasks.Client,
) {
	r.SetSavedResponseBindings(
		bindings,
		savedresponse.NewResponseDelivery(savedresponse.NewService(nil)),
	)
	if tasksClient != nil {
		r.SetTasks(tasksClient)
	}
}

func TestCommandRouter_Dispatch(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	r.Register("/start", func(c *command.Context) error {
		_, err := c.Reply("start", nil)
		return err
	})

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
	if fake.lastSentText != "start" {
		t.Fatalf("unexpected /start response: %q", fake.lastSentText)
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

type testDelayedScheduler struct{}

func (*testDelayedScheduler) Schedule(context.Context, time.Duration, int64, func(context.Context) error) error {
	return nil
}

func TestCommandRouter_InjectsDelayedActionOwner(t *testing.T) {
	r := command.NewRouter(zap.NewNop())
	scheduler := &testDelayedScheduler{}
	r.SetDelayedActions(scheduler)

	coreRouter := core.NewRouter(".")
	err := coreRouter.Register(core.Command{
		Name:     "delayowner",
		Surfaces: execution.SurfaceAssistant,
		Handler: func(c *core.Context) error {
			if c.DelayedActions != scheduler {
				t.Fatalf("expected assistant core context to receive delayed action owner")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("register command: %v", err)
	}
	r.SetCoreRouter(coreRouter)

	err = r.Dispatch(context.Background(), 12345, &tg.InputPeerUser{UserID: 12345}, "/delayowner", &fakeInteraction{})
	if err != nil {
		t.Fatalf("dispatch: %v", err)
	}
}

func TestCommandRouter_DynamicSavedResponseDispatch(t *testing.T) {
	bindings, _, _, scope := newSavedResponseCommandFixture(
		t,
		"hello",
		savedresponse.NewText("hello {id}"),
	)
	taskClient := &immediateTaskClient{}
	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, taskClient)

	fake := &fakeInteraction{}
	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/hello ignored-args",
		fake,
	)
	if err != nil {
		t.Fatalf("Dispatch(dynamic) error = %v", err)
	}
	if fake.lastSentText != "hello 12345" {
		t.Fatalf("dynamic response text = %q, want %q", fake.lastSentText, "hello 12345")
	}
	if taskClient.submitCount != 1 {
		t.Fatalf("dynamic submit count = %d, want 1", taskClient.submitCount)
	}
	if taskClient.last.Scope != scope {
		t.Fatalf("dynamic task scope = %+v, want %+v", taskClient.last.Scope, scope)
	}
	if taskClient.last.Pool != "interactive" || taskClient.last.Class != tasks.PriorityInteractive {
		t.Fatalf("dynamic task admission = pool %q class %q", taskClient.last.Pool, taskClient.last.Class)
	}
	if len(taskClient.last.Resources) != 0 {
		t.Fatalf("text dynamic response reserved unexpected resources: %+v", taskClient.last.Resources)
	}
}

func TestCommandRouter_DynamicSavedResponseRequiresTaskEngine(t *testing.T) {
	bindings, _, _, _ := newSavedResponseCommandFixture(
		t,
		"hello",
		savedresponse.NewText("hello"),
	)
	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, nil)

	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/hello",
		&fakeInteraction{},
	)
	if !errors.Is(err, command.ErrTasksNotConfigured) {
		t.Fatalf("Dispatch(dynamic without TaskEngine) error = %v, want %v", err, command.ErrTasksNotConfigured)
	}
}

func TestCommandRouter_DynamicSavedResponseReservesMediaResource(t *testing.T) {
	response := savedresponse.Response{
		Text: "caption",
		Media: &savedresponse.MediaRef{
			AssetID:   "asset-1",
			MediaType: "photo",
			Name:      "photo.jpg",
			MIMEType:  "image/jpeg",
		},
	}
	bindings, _, _, scope := newSavedResponseCommandFixture(t, "photo", response)
	admissionErr := errors.New("stop after admission capture")
	taskClient := &immediateTaskClient{submitErr: admissionErr}
	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, taskClient)

	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/photo",
		&fakeInteraction{},
	)
	if !errors.Is(err, admissionErr) {
		t.Fatalf("Dispatch(media admission) error = %v, want %v", err, admissionErr)
	}
	if taskClient.last.Scope != scope {
		t.Fatalf("media task scope = %+v, want %+v", taskClient.last.Scope, scope)
	}
	if len(taskClient.last.Resources) != 1 ||
		taskClient.last.Resources[0].Name != "media" ||
		taskClient.last.Resources[0].Amount != 1 {
		t.Fatalf("media task resources = %+v, want media:1", taskClient.last.Resources)
	}
}

func TestCommandRouter_DynamicSavedResponseDeliversMedia(t *testing.T) {
	ctx := context.Background()
	store := storage.NewMemoryStorage()
	asset, err := store.Put(ctx, bytes.NewBufferString("image-bytes"), storage.Metadata{
		Name: "photo.jpg",
		MIME: "image/jpeg",
	})
	if err != nil {
		t.Fatal(err)
	}
	response := savedresponse.Response{
		Text: "caption {id}",
		Media: &savedresponse.MediaRef{
			AssetID:   asset.ID,
			MediaType: "photo",
			Name:      asset.Name,
			MIMEType:  asset.MIME,
		},
	}
	bindings, _, _, _ := newSavedResponseCommandFixture(t, "photo-live", response)

	files, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	responseService := savedresponse.NewService(store)
	responseService.SetFiles(files.ForOwner("assistant-savedresponse-test"))

	taskClient := &immediateTaskClient{}
	r := command.NewRouter(zap.NewNop())
	r.SetSavedResponseBindings(bindings, savedresponse.NewResponseDelivery(responseService))
	r.SetTasks(taskClient)

	fake := &fakeInteraction{}
	if err := r.Dispatch(
		ctx,
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/photo-live",
		fake,
	); err != nil {
		t.Fatalf("Dispatch(media) error = %v", err)
	}
	if fake.lastMediaType != "photo" || fake.lastMediaCaption != "caption 12345" {
		t.Fatalf("media delivery = type %q caption %q", fake.lastMediaType, fake.lastMediaCaption)
	}
	if fake.lastMediaPath == "" {
		t.Fatal("media delivery did not receive a materialized path")
	}
	if _, err := os.Stat(fake.lastMediaPath); !os.IsNotExist(err) {
		t.Fatalf("materialized media path was not cleaned up: stat error = %v", err)
	}
	if len(taskClient.last.Resources) != 1 ||
		taskClient.last.Resources[0].Name != "media" ||
		taskClient.last.Resources[0].Amount != 1 {
		t.Fatalf("media task resources = %+v, want media:1", taskClient.last.Resources)
	}
}

func TestCommandRouter_CanonicalAndPresentationPrecedeDynamicBindings(t *testing.T) {
	t.Run("canonical", func(t *testing.T) {
		bindings, _, _, _ := newSavedResponseCommandFixture(t, "ping", savedresponse.NewText("dynamic"))
		taskClient := &immediateTaskClient{}
		r := command.NewRouter(zap.NewNop())
		configureSavedResponseRouter(r, bindings, taskClient)

		coreRouter := core.NewRouter(".")
		if err := coreRouter.Register(core.Command{
			Name:     "ping",
			Surfaces: execution.SurfaceAssistant,
			Handler: func(c *core.Context) error {
				return c.Reply("canonical")
			},
		}); err != nil {
			t.Fatal(err)
		}
		r.SetCoreRouter(coreRouter)

		fake := &fakeInteraction{}
		if err := r.Dispatch(
			context.Background(),
			12345,
			&tg.InputPeerUser{UserID: 12345},
			"/ping",
			fake,
		); err != nil {
			t.Fatal(err)
		}
		if fake.lastSentText != "canonical" {
			t.Fatalf("response = %q, want canonical", fake.lastSentText)
		}
	})

	t.Run("presentation", func(t *testing.T) {
		bindings, _, _, _ := newSavedResponseCommandFixture(t, "start", savedresponse.NewText("dynamic"))
		taskClient := &immediateTaskClient{}
		r := command.NewRouter(zap.NewNop())
		configureSavedResponseRouter(r, bindings, taskClient)
		r.Register("/start", func(c *command.Context) error {
			_, err := c.Reply("presentation", nil)
			return err
		})

		fake := &fakeInteraction{}
		if err := r.Dispatch(
			context.Background(),
			12345,
			&tg.InputPeerUser{UserID: 12345},
			"/start",
			fake,
		); err != nil {
			t.Fatal(err)
		}
		if fake.lastSentText != "presentation" {
			t.Fatalf("response = %q, want presentation", fake.lastSentText)
		}
		if taskClient.submitCount != 0 {
			t.Fatalf("presentation collision submitted %d dynamic task(s)", taskClient.submitCount)
		}
	})
}

func TestCommandRouter_DynamicSavedResponseFailsClosedAcrossProviderReload(t *testing.T) {
	bindings, registry, registration, _ := newSavedResponseCommandFixture(
		t,
		"reload",
		savedresponse.NewText("old"),
	)
	taskClient := &immediateTaskClient{}
	taskClient.beforeRun = func(tasks.WorkSpec) {
		registration.Close()
		reloaded, err := registry.Register(
			"notes",
			tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 2},
			&savedResponseResolver{response: savedresponse.NewText("new")},
		)
		if err != nil {
			t.Fatalf("reload provider: %v", err)
		}
		t.Cleanup(reloaded.Close)
	}

	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, taskClient)
	fake := &fakeInteraction{}
	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/reload",
		fake,
	)
	if err == nil || err.Error() != savedresponse.ErrBindingStale.Error() {
		t.Fatalf("Dispatch(after provider reload) error = %v, want stale binding", err)
	}
	if fake.lastSentText != "" {
		t.Fatalf("stale dynamic binding sent response %q", fake.lastSentText)
	}
}

func TestCommandRouter_UnknownCommandStillReturnsUnknownAfterDynamicLookup(t *testing.T) {
	bindings, _, _, _ := newSavedResponseCommandFixture(
		t,
		"known",
		savedresponse.NewText("known"),
	)
	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, &immediateTaskClient{})

	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/missing",
		&fakeInteraction{},
	)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("Dispatch(missing dynamic alias) error = %v, want %v", err, command.ErrUnknownCommand)
	}
}


func TestCommandRouter_DynamicSavedResponseFailsClosedWithoutDelivery(t *testing.T) {
	bindings, _, _, _ := newSavedResponseCommandFixture(
		t,
		"misconfigured",
		savedresponse.NewText("must not send"),
	)
	r := command.NewRouter(zap.NewNop())
	r.SetSavedResponseBindings(bindings, nil)
	r.SetTasks(&immediateTaskClient{})

	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/misconfigured",
		&fakeInteraction{},
	)
	if !errors.Is(err, savedresponse.ErrResponseDeliveryUnavailable) {
		t.Fatalf("Dispatch(missing delivery) error = %v, want %v", err, savedresponse.ErrResponseDeliveryUnavailable)
	}
}

func TestCommandRouter_CanonicalNamespaceBlocksDynamicFallback(t *testing.T) {
	bindings, _, _, _ := newSavedResponseCommandFixture(
		t,
		"useronly",
		savedresponse.NewText("dynamic must not shadow canonical"),
	)
	taskClient := &immediateTaskClient{}
	r := command.NewRouter(zap.NewNop())
	configureSavedResponseRouter(r, bindings, taskClient)

	coreRouter := core.NewRouter(".")
	if err := coreRouter.Register(core.Command{
		Name:     "useronly",
		Surfaces: execution.SurfaceUserbot,
		Handler:  func(*core.Context) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	r.SetCoreRouter(coreRouter)

	fake := &fakeInteraction{}
	err := r.Dispatch(
		context.Background(),
		12345,
		&tg.InputPeerUser{UserID: 12345},
		"/useronly",
		fake,
	)
	if !errors.Is(err, command.ErrUnknownCommand) {
		t.Fatalf("Dispatch(canonical namespace collision) error = %v, want %v", err, command.ErrUnknownCommand)
	}
	if taskClient.submitCount != 0 {
		t.Fatalf("canonical namespace collision submitted %d dynamic task(s)", taskClient.submitCount)
	}
	if fake.lastSentText != "" {
		t.Fatalf("canonical namespace collision sent dynamic response %q", fake.lastSentText)
	}
}
