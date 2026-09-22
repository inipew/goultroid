package savedresponsecallback

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/savedresponse"
	"github.com/inipew/goultroid/internal/tasks"
)

type callbackResolver struct {
	response savedresponse.Response
}

func (r *callbackResolver) ResolveSavedResponse(context.Context, savedresponse.Reference) (savedresponse.Response, bool, error) {
	return r.response, true, nil
}

type callbackPort struct {
	sent presentation.CompiledView
}

func (p *callbackPort) Send(_ context.Context, target presentation.Target, view presentation.CompiledView) (presentation.Target, error) {
	p.sent = view
	messageTarget, ok := target.(presentationtelegram.MessageTarget)
	if !ok {
		return nil, presentationtelegram.ErrInvalidTarget
	}
	messageTarget.MessageID = 77
	return messageTarget, nil
}
func (*callbackPort) Edit(context.Context, presentation.Target, presentation.CompiledView) error {
	return nil
}
func (*callbackPort) Answer(context.Context, presentation.Answer) error { return nil }

type callbackTelegramService struct {
	core.MockTelegramServicer
	sentText     string
	sentMedia    string
	sentCaption  string
}

func (s *callbackTelegramService) SendMessageWithMarkup(
	_ context.Context,
	_ tg.InputPeerClass,
	text string,
	_ tg.ReplyMarkupClass,
) (*tg.Message, error) {
	s.sentText = text
	return &tg.Message{ID: 101, Message: text}, nil
}

func (s *callbackTelegramService) SendMedia(
	_ context.Context,
	_ tg.InputPeerClass,
	mediaType string,
	_ string,
	caption string,
) (*tg.Message, error) {
	s.sentMedia = mediaType
	s.sentCaption = caption
	return &tg.Message{ID: 102}, nil
}

type callbackFixture struct {
	feature      *Feature
	bindings     *savedresponse.BindingService
	binding      savedresponse.SurfaceBinding
	registry     *savedresponse.Registry
	resolver     *callbackResolver
	registration *savedresponse.Registration
	providerScope tasks.ScopeIdentity
	engine       *orchestration.Engine
	port         *callbackPort
	service      *callbackTelegramService
}

func newCallbackFixture(t *testing.T, response savedresponse.Response) *callbackFixture {
	t.Helper()
	ctx := context.Background()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(ctx, db, savedresponse.MigrationProvider{}); err != nil {
		t.Fatalf("RunFeatureMigrations() error=%v", err)
	}

	responseRegistry := savedresponse.NewRegistry()
	providerScope := tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 7}
	resolver := &callbackResolver{response: response}
	responseRegistration, err := responseRegistry.Register("notes", providerScope, resolver)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(responseRegistration.Close)

	bindings := savedresponse.NewBindingService(
		savedresponse.NewSQLiteSurfaceBindingRepository(db),
		responseRegistry,
	)
	binding, err := bindings.Create(ctx, savedresponse.SurfaceBinding{
		Surface: savedresponse.SurfaceCallback,
		Alias:   "hello",
		Reference: savedresponse.Reference{
			Provider: "notes",
			ScopeID:  42,
			Key:      "hello",
		},
		Enabled: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	callbackFeature := New(
		bindings,
		savedresponse.NewResponseDelivery(savedresponse.NewService(nil)),
	)
	catalog := feature.NewRegistry()
	spec, err := feature.BindCanonicalCommands(callbackFeature.FeatureSpec(), nil)
	if err != nil {
		t.Fatal(err)
	}
	featureScope := tasks.ScopeIdentity{Owner: "plugin:" + FeatureID, Generation: 1}
	featureRegistration, err := catalog.Register(feature.Owner{ID: FeatureID, Scope: featureScope}, spec)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(featureRegistration.Close)

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sessions.Close() })
	port := &callbackPort{}
	engine, err := orchestration.New(sessions, rootinteraction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatal(err)
	}
	service := &callbackTelegramService{}
	cleanup, err := callbackFeature.BindAssistant(assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: catalog,
		Service: service,
		Admit: func(string, feature.InteractionKind, string, int64, presentation.Target) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cleanup)

	return &callbackFixture{
		feature: callbackFeature, bindings: bindings, binding: binding,
		registry: responseRegistry, resolver: resolver, registration: responseRegistration,
		providerScope: providerScope, engine: engine, port: port, service: service,
	}
}

func (f *callbackFixture) begin(t *testing.T) *orchestration.Context {
	t.Helper()
	ctx, err := f.feature.Begin(context.Background(), BeginRequest{
		Alias:      "hello",
		ActorID:    99,
		Target:     presentationtelegram.MessageTarget{Peer: &tg.InputPeerUser{UserID: 99}, ChatID: 99},
		Text:       "Run saved response",
		ButtonText: "Run",
		TTL:        time.Minute,
	})
	if err != nil {
		t.Fatalf("Begin() error=%v", err)
	}
	return ctx
}

func (f *callbackFixture) callbackData(t *testing.T) []byte {
	t.Helper()
	if len(f.port.sent.Rows) != 1 || len(f.port.sent.Rows[0]) != 1 {
		t.Fatalf("compiled rows=%+v", f.port.sent.Rows)
	}
	return append([]byte(nil), f.port.sent.Rows[0][0].Data...)
}

func TestSavedResponseCallbackUsesOpaqueA2TokenAndProviderAdmission(t *testing.T) {
	fixture := newCallbackFixture(t, savedresponse.NewText("Hello {id} in {chat}"))
	sessionCtx := fixture.begin(t)
	data := fixture.callbackData(t)

	token, err := rootinteraction.ParseCallbackToken(data)
	if err != nil {
		t.Fatalf("ParseCallbackToken() error=%v", err)
	}
	if token.FeatureID != FeatureID || token.ActionID != ActionDeliver {
		t.Fatalf("callback token=%+v", token)
	}
	raw := string(data)
	for _, forbidden := range []string{"hello", "notes"} {
		if strings.Contains(raw, forbidden) {
			t.Fatalf("callback data leaks SavedResponse identity %q: %q", forbidden, raw)
		}
	}

	prepared, err := fixture.engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: data, ActorID: 99, QueryID: 500, Target: sessionCtx.Target(),
	})
	if err != nil {
		t.Fatalf("PrepareCallback() error=%v", err)
	}
	if prepared.Scope() != fixture.providerScope {
		t.Fatalf("prepared scope=%+v, want provider scope %+v", prepared.Scope(), fixture.providerScope)
	}
	if len(prepared.Resources()) != 0 {
		t.Fatalf("text callback resources=%+v, want none", prepared.Resources())
	}
	if err := prepared.Dispatch(context.Background()); err != nil {
		t.Fatalf("Dispatch() error=%v", err)
	}
	if fixture.service.sentText != "Hello 99 in 99" {
		t.Fatalf("delivered text=%q", fixture.service.sentText)
	}
}

func TestSavedResponseCallbackCarriesMediaResourceBeforeExecution(t *testing.T) {
	fixture := newCallbackFixture(t, savedresponse.Response{
		Text: "caption",
		Media: &savedresponse.MediaRef{
			AssetID: "asset-1", MediaType: "photo", Name: "photo.jpg", MIMEType: "image/jpeg",
		},
	})
	sessionCtx := fixture.begin(t)
	prepared, err := fixture.engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: fixture.callbackData(t), ActorID: 99, QueryID: 501, Target: sessionCtx.Target(),
	})
	if err != nil {
		t.Fatal(err)
	}
	resources := prepared.Resources()
	if len(resources) != 1 || resources[0].Name != "media" || resources[0].Amount != 1 {
		t.Fatalf("media callback resources=%+v, want media:1", resources)
	}
	if prepared.Scope() != fixture.providerScope {
		t.Fatalf("media callback scope=%+v, want %+v", prepared.Scope(), fixture.providerScope)
	}
}

func TestSavedResponseCallbackBindingMutationStalesVisibleButton(t *testing.T) {
	fixture := newCallbackFixture(t, savedresponse.NewText("old"))
	sessionCtx := fixture.begin(t)
	current, err := fixture.bindings.Get(context.Background(), savedresponse.SurfaceCallback, "hello")
	if err != nil || current == nil {
		t.Fatalf("Get() binding=%+v err=%v", current, err)
	}
	if _, err := fixture.bindings.Update(
		context.Background(),
		current.Surface,
		current.Alias,
		current.Reference,
		current.Enabled,
		current.Revision,
		current.Incarnation,
	); err != nil {
		t.Fatal(err)
	}

	_, err = fixture.engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: fixture.callbackData(t), ActorID: 99, QueryID: 502, Target: sessionCtx.Target(),
	})
	if !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("PrepareCallback(after mutation) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}
	if fixture.service.sentText != "" {
		t.Fatalf("stale binding delivered text=%q", fixture.service.sentText)
	}
}

func TestSavedResponseCallbackQueuedProviderReloadFailsClosed(t *testing.T) {
	fixture := newCallbackFixture(t, savedresponse.NewText("old"))
	sessionCtx := fixture.begin(t)
	prepared, err := fixture.engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: fixture.callbackData(t), ActorID: 99, QueryID: 503, Target: sessionCtx.Target(),
	})
	if err != nil {
		t.Fatal(err)
	}

	fixture.registration.Close()
	reloaded, err := fixture.registry.Register(
		"notes",
		tasks.ScopeIdentity{Owner: "plugin:notes", Generation: 8},
		&callbackResolver{response: savedresponse.NewText("new")},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer reloaded.Close()

	if err := prepared.Dispatch(context.Background()); !errors.Is(err, savedresponse.ErrBindingStale) {
		t.Fatalf("Dispatch(after provider reload) error=%v, want %v", err, savedresponse.ErrBindingStale)
	}
	if fixture.service.sentText != "" {
		t.Fatalf("stale provider delivered text=%q", fixture.service.sentText)
	}
}

func TestSavedResponseCallbackRejectsWrongActorBeforeDynamicPrepare(t *testing.T) {
	fixture := newCallbackFixture(t, savedresponse.NewText("secret"))
	sessionCtx := fixture.begin(t)
	_, err := fixture.engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: fixture.callbackData(t), ActorID: 100, QueryID: 504, Target: sessionCtx.Target(),
	})
	if !errors.Is(err, rootinteraction.ErrBindingMismatch) {
		t.Fatalf("PrepareCallback(wrong actor) error=%v, want %v", err, rootinteraction.ErrBindingMismatch)
	}
}
