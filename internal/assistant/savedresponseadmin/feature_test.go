package savedresponseadmin

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

type adminResolver struct{}

func (*adminResolver) ResolveSavedResponse(context.Context, savedresponse.Reference) (savedresponse.Response, bool, error) {
	return savedresponse.NewText("authoritative"), true, nil
}

type adminPort struct {
	sent   presentation.CompiledView
	edited presentation.CompiledView
}

func (p *adminPort) Send(_ context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	p.sent = view
	t := target.(presentationtelegram.MessageTarget)
	t.MessageID = 77
	return t, nil
}
func (p *adminPort) Edit(_ context.Context, _ presentation.Target, view presentation.CompiledView) error {
	p.edited = view
	return nil
}
func (*adminPort) Answer(context.Context, presentation.Answer) error { return nil }

type adminFixture struct {
	feature  *Feature
	bindings *savedresponse.BindingService
	engine   *orchestration.Engine
	port     *adminPort
	target   presentationtelegram.MessageTarget
}

func newAdminFixture(t *testing.T) *adminFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(ctx, db, savedresponse.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}

	responses := savedresponse.NewRegistry()
	providerReg, err := responses.Register(
		"notes",
		tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 1},
		&adminResolver{},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(providerReg.Close)

	bindings := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		responses,
	)
	admin := New(bindings)
	spec, err := feature.BindCanonicalCommands(admin.FeatureSpec(), admin.Commands())
	if err != nil {
		t.Fatal(err)
	}
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:" + FeatureID, Generation: 1}
	featureReg, err := catalog.Register(feature.Owner{ID: FeatureID, Scope: scope}, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(featureReg.Close)

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	port := &adminPort{}
	engine, err := orchestration.New(sessions, rootinteraction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := admin.BindAssistant(assistantinteraction.DriverRuntime{
		Engine: engine,
		Catalog: catalog,
		Admit: func(string, feature.InteractionKind, string, int64, presentation.Target) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	return &adminFixture{
		feature: admin,
		bindings: bindings,
		engine: engine,
		port: port,
		target: presentationtelegram.MessageTarget{
			Peer: &tg.InputPeerUser{UserID: 1},
			ChatID: 42,
			MessageID: 77,
		},
	}
}

func callbackForAction(t *testing.T, view presentation.CompiledView, actionID string) []byte {
	t.Helper()
	for _, row := range view.Rows {
		for _, button := range row {
			token, err := rootinteraction.ParseCallbackToken(button.Data)
			if err == nil && token.ActionID == actionID {
				return append([]byte(nil), button.Data...)
			}
		}
	}
	t.Fatalf("action %q not found in view %+v", actionID, view)
	return nil
}

func (f *adminFixture) dispatch(t *testing.T, view presentation.CompiledView, actionID string, queryID int64) {
	t.Helper()
	err := f.engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: callbackForAction(t, view, actionID),
		ActorID: 1,
		QueryID: queryID,
		Target: f.target,
	})
	if err != nil {
		t.Fatalf("dispatch %s: %v", actionID, err)
	}
}

func (f *adminFixture) input(t *testing.T, value string) {
	t.Helper()
	inputCtx, handled, err := f.engine.TakeInput(context.Background(), 1, 42)
	if err != nil {
		t.Fatalf("TakeInput() error=%v", err)
	}
	if !handled {
		t.Fatal("expected pending input claim")
	}
	if err := inputCtx.SetTarget(f.target); err != nil {
		t.Fatal(err)
	}
	if err := f.feature.HandleAssistantInput(inputCtx, value); err != nil {
		t.Fatalf("HandleAssistantInput(%q) error=%v", value, err)
	}
}

func TestA2ControlSurfaceCRUDLifecycle(t *testing.T) {
	f := newAdminFixture(t)
	coreCtx := &core.Context{
		Ctx: context.Background(),
		PeerID: f.target.Peer,
		Chat: &core.Chat{ID: 42, Type: "private"},
		Sender: &core.User{ID: 1},
	}
	if err := f.feature.openCommand(coreCtx); err != nil {
		t.Fatalf("openCommand() error=%v", err)
	}

	f.dispatch(t, f.port.sent, actionSurfaceInline, 101)
	f.dispatch(t, f.port.edited, actionCreate, 102)
	f.input(t, "hello notes 7 greeting")

	created, err := f.bindings.Get(context.Background(), savedresponse.SurfaceInline, "hello")
	if err != nil || created == nil {
		t.Fatalf("created binding=%+v err=%v", created, err)
	}
	if !created.Enabled || created.Reference.Provider != "notes" ||
		created.Reference.ScopeID != 7 || created.Reference.Key != "greeting" {
		t.Fatalf("unexpected created binding: %+v", created)
	}

	f.dispatch(t, f.port.edited, actionEdit, 103)
	f.input(t, "notes 8 replacement")
	updated, err := f.bindings.Get(context.Background(), savedresponse.SurfaceInline, "hello")
	if err != nil || updated == nil {
		t.Fatalf("updated binding=%+v err=%v", updated, err)
	}
	if updated.Reference.ScopeID != 8 || updated.Reference.Key != "replacement" ||
		updated.Revision <= created.Revision {
		t.Fatalf("unexpected updated binding: %+v", updated)
	}

	f.dispatch(t, f.port.edited, actionToggle, 104)
	disabled, err := f.bindings.Get(context.Background(), savedresponse.SurfaceInline, "hello")
	if err != nil || disabled == nil {
		t.Fatalf("disabled binding=%+v err=%v", disabled, err)
	}
	if disabled.Enabled {
		t.Fatalf("toggle did not disable binding: %+v", disabled)
	}

	f.dispatch(t, f.port.edited, actionDelete, 105)
	f.dispatch(t, f.port.edited, actionDeleteConfirm, 106)
	deleted, err := f.bindings.Get(context.Background(), savedresponse.SurfaceInline, "hello")
	if err != nil {
		t.Fatal(err)
	}
	if deleted != nil {
		t.Fatalf("binding still exists after a2 delete: %+v", deleted)
	}
}

func TestA2ControlSurfaceStaleMutationFailsClosed(t *testing.T) {
	f := newAdminFixture(t)
	created, err := f.bindings.Create(context.Background(), savedresponse.SurfaceBinding{
		Surface: savedresponse.SurfaceCallback,
		Alias: "stale",
		Reference: savedresponse.Reference{Provider: "notes", ScopeID: 1, Key: "one"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	coreCtx := &core.Context{
		Ctx: context.Background(),
		PeerID: f.target.Peer,
		Chat: &core.Chat{ID: 42, Type: "private"},
		Sender: &core.User{ID: 1},
	}
	if err := f.feature.openCommand(coreCtx); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t, f.port.sent, actionSurfaceCallback, 201)
	f.dispatch(t, f.port.edited, slotID(0), 202)

	if _, err := f.bindings.SetEnabled(
		context.Background(),
		created.Surface,
		created.Alias,
		false,
		created.Revision,
		created.Incarnation,
	); err != nil {
		t.Fatal(err)
	}

	// The rendered detail carries the old CAS lease. Toggle must not overwrite
	// the concurrent change; it refreshes from durable state instead.
	f.dispatch(t, f.port.edited, actionToggle, 203)
	current, err := f.bindings.Get(context.Background(), created.Surface, created.Alias)
	if err != nil || current == nil {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if current.Enabled || current.Revision != created.Revision+1 {
		t.Fatalf("stale a2 mutation overwrote concurrent state: %+v", current)
	}
}

func TestA2ControlSurfaceHonorsCollisionGuard(t *testing.T) {
	f := newAdminFixture(t)
	f.bindings.SetAliasGuard(func(surface savedresponse.Surface, alias string) error {
		if surface == savedresponse.SurfaceAssistantCommand && alias == "start" {
			return savedresponse.ErrBindingReserved
		}
		return nil
	})

	coreCtx := &core.Context{
		Ctx: context.Background(),
		PeerID: f.target.Peer,
		Chat: &core.Chat{ID: 42, Type: "private"},
		Sender: &core.User{ID: 1},
	}
	if err := f.feature.openCommand(coreCtx); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t, f.port.sent, actionSurfaceAssistant, 301)
	f.dispatch(t, f.port.edited, actionCreate, 302)
	f.input(t, "start notes 1 key")

	got, err := f.bindings.Get(context.Background(), savedresponse.SurfaceAssistantCommand, "start")
	if err != nil {
		t.Fatal(err)
	}
	if got != nil {
		t.Fatalf("reserved binding persisted: %+v", got)
	}
	if _, handled, err := f.engine.TakeInput(context.Background(), 1, 42); err != nil || !handled {
		t.Fatalf("collision should re-arm input; handled=%v err=%v", handled, err)
	}
}

func TestA2ControlSurfaceRejectsWrongActor(t *testing.T) {
	f := newAdminFixture(t)
	coreCtx := &core.Context{
		Ctx: context.Background(),
		PeerID: f.target.Peer,
		Chat: &core.Chat{ID: 42, Type: "private"},
		Sender: &core.User{ID: 1},
	}
	if err := f.feature.openCommand(coreCtx); err != nil {
		t.Fatal(err)
	}
	err := f.engine.Dispatch(context.Background(), orchestration.CallbackRequest{
		Data: callbackForAction(t, f.port.sent, actionSurfaceInline),
		ActorID: 2,
		QueryID: 401,
		Target: f.target,
	})
	if !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("wrong actor error=%v, want %v", err, rootinteraction.ErrBindingMismatch)
	}
}


func TestAdminCommandPolicyIsAssistantOwnerPrivateOnly(t *testing.T) {
	admin := New(nil)
	commands := admin.Commands()
	if len(commands) != 1 {
		t.Fatalf("commands=%d, want 1", len(commands))
	}
	cmd := commands[0]
	if cmd.Name != CommandID ||
		cmd.Permission != core.PermissionOwner ||
		cmd.Invocation.Assistant != core.InvocationSelfOnly ||
		!cmd.PrivateOnly ||
		!cmd.Surfaces.Supports(execution.SourceAssistant) ||
		cmd.Surfaces.Supports(execution.SourceUserbot) {
		t.Fatalf("unexpected admin command policy: %+v", cmd)
	}
}


func TestA2ControlSurfaceInputCASDoesNotOverwriteConcurrentEdit(t *testing.T) {
	f := newAdminFixture(t)
	created, err := f.bindings.Create(context.Background(), savedresponse.SurfaceBinding{
		Surface: savedresponse.SurfaceDeepLink,
		Alias: "race",
		Reference: savedresponse.Reference{Provider: "notes", ScopeID: 1, Key: "before"},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	coreCtx := &core.Context{
		Ctx: context.Background(),
		PeerID: f.target.Peer,
		Chat: &core.Chat{ID: 42, Type: "private"},
		Sender: &core.User{ID: 1},
	}
	if err := f.feature.openCommand(coreCtx); err != nil {
		t.Fatal(err)
	}
	f.dispatch(t, f.port.sent, actionSurfaceDeepLink, 501)
	f.dispatch(t, f.port.edited, slotID(0), 502)
	f.dispatch(t, f.port.edited, actionEdit, 503)

	concurrent, err := f.bindings.Update(
		context.Background(),
		created.Surface,
		created.Alias,
		savedresponse.Reference{Provider: "notes", ScopeID: 2, Key: "concurrent"},
		created.Enabled,
		created.Revision,
		created.Incarnation,
	)
	if err != nil {
		t.Fatal(err)
	}

	f.input(t, "notes 3 stale-input")
	current, err := f.bindings.Get(context.Background(), created.Surface, created.Alias)
	if err != nil || current == nil {
		t.Fatalf("current=%+v err=%v", current, err)
	}
	if current.Revision != concurrent.Revision ||
		current.Reference.ScopeID != concurrent.Reference.ScopeID ||
		current.Reference.Key != concurrent.Reference.Key {
		t.Fatalf("stale input overwrote concurrent mutation: current=%+v concurrent=%+v", current, concurrent)
	}
}
