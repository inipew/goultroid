package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/tasks"
)

func TestP8EDownloaderFeatureDeclaresBoundedTypedWorkflow(t *testing.T) {
	p := New()
	spec := p.FeatureSpec()
	if spec.ID != "downloader" {
		t.Fatalf("feature ID=%q", spec.ID)
	}
	inline := 0
	actions := 0
	for _, interaction := range spec.Interactions {
		switch interaction.Kind {
		case feature.InteractionInline:
			inline++
		case feature.InteractionAction:
			actions++
		}
	}
	if inline != 1 || actions != len(interactiveActions) {
		t.Fatalf("inline/actions=%d/%d, want 1/%d", inline, actions, len(interactiveActions))
	}
	bindings := p.InlineBindings()
	if len(bindings) != 1 || bindings[0].InteractionID != interactiveInlineID {
		t.Fatalf("inline bindings=%+v", bindings)
	}
}

func TestP8EInlineDirectHTTPUsesPrivateNoCacheSession(t *testing.T) {
	p := New()
	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)
	h := &interactiveInlineHandler{plugin: p}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{Args: []string{"https://example.com/media/file.mp4"}})
	if err != nil {
		t.Fatal(err)
	}
	if !response.Private || response.Cache != inlineservice.CacheNone || len(response.Results) != 1 {
		t.Fatalf("response policy private=%v cache=%v results=%d", response.Private, response.Cache, len(response.Results))
	}
	result := response.Results[0]
	state, err := decodeInteractiveState(result.InteractionState)
	if err != nil {
		t.Fatal(err)
	}
	if state.Provider != "http" || len(result.InteractionState) > maxInteractiveState {
		t.Fatalf("state=%+v bytes=%d", state, len(result.InteractionState))
	}
	if !viewHasAction(result.ActionRows, actionDownloadFile) || viewHasAction(result.ActionRows, actionAudio) {
		t.Fatalf("direct HTTP action rows=%+v", result.ActionRows)
	}
}

func TestP8EInlineExtractorOffersMediaThenFormatChoice(t *testing.T) {
	p := New()
	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)
	h := &interactiveInlineHandler{plugin: p}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{Args: []string{"https://www.youtube.com/watch?v=dQw4w9WgXcQ"}})
	if err != nil {
		t.Fatal(err)
	}
	result := response.Results[0]
	state, err := decodeInteractiveState(result.InteractionState)
	if err != nil {
		t.Fatal(err)
	}
	if state.Provider != "extractor" || state.Phase != phaseChoose {
		t.Fatalf("initial extractor state=%+v", state)
	}
	if !viewHasAction(result.ActionRows, actionAudio) || !viewHasAction(result.ActionRows, actionVideo) {
		t.Fatalf("extractor action rows=%+v", result.ActionRows)
	}
	if rows := audioFormatView(state.URL).Rows; !viewHasAction(rows, actionFormatM4A) || !viewHasAction(rows, actionFormatMP3) {
		t.Fatalf("audio format rows=%+v", rows)
	}
	if rows := videoFormatView(state.URL).Rows; !viewHasAction(rows, actionFormatMP4) || !viewHasAction(rows, actionFormatBest) {
		t.Fatalf("video format rows=%+v", rows)
	}
}

func TestP8EFinalSelectionResourcePlanningMatchesProvider(t *testing.T) {
	p := New()
	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)

	direct := interactiveState{URL: "https://example.com/file.bin", Provider: "http", Phase: phaseChoose}
	if err := p.validateFinalAction(direct, actionDownloadFile); err != nil {
		t.Fatal(err)
	}
	resources := p.urlResources(direct.URL)
	if !hasResource(resources, "download") || hasResource(resources, "process") {
		t.Fatalf("direct resources=%+v", resources)
	}

	extractor := interactiveState{
		URL:      "https://youtu.be/dQw4w9WgXcQ",
		Provider: "extractor",
		Phase:    phaseVideoFormat,
		Mode:     download.MediaModeVideo,
	}
	if err := p.validateFinalAction(extractor, actionFormatMP4); err != nil {
		t.Fatal(err)
	}
	resources = p.urlResources(extractor.URL)
	if !hasResource(resources, "download") || !hasResource(resources, "process") {
		t.Fatalf("extractor resources=%+v", resources)
	}
	mode, format, err := finalSelection(extractor, actionFormatMP4)
	if err != nil || mode != download.MediaModeVideo || format != download.MediaFormatMP4 {
		t.Fatalf("selection=%q/%q err=%v", mode, format, err)
	}
}

func TestP8EMatcherKeepsKeywordBoundary(t *testing.T) {
	matcher := downloaderMatcher{}
	if args, ok := matcher.Match("dl https://example.com/file.mp4"); !ok || len(args) != 1 {
		t.Fatalf("valid dl query match=%v args=%v", ok, args)
	}
	if _, ok := matcher.Match("dlfoo https://example.com/file.mp4"); ok {
		t.Fatal("prefix collision unexpectedly matched downloader")
	}
}

func TestP8EInteractiveStateRejectsOversizedOrUnsafeURL(t *testing.T) {
	if _, err := normalizeInteractiveURL("file:///tmp/secret"); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("file URL error=%v", err)
	}
	if _, err := normalizeInteractiveURL("https://example.com/" + strings.Repeat("a", maxInteractiveURLLen)); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("oversized URL error=%v", err)
	}
	if _, err := decodeInteractiveState(make([]byte, maxInteractiveState+1)); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("oversized state error=%v", err)
	}
}

type p8ePort struct{}

func (*p8ePort) Send(_ context.Context, target presentation.Target, _ presentation.CompiledView) (presentation.Target, error) {
	return target, nil
}
func (*p8ePort) Edit(context.Context, presentation.Target, presentation.CompiledView) error {
	return nil
}
func (*p8ePort) Answer(context.Context, presentation.Answer) error { return nil }

func TestP8EFinalCallbackPublishesDynamicResourcesBeforeDispatch(t *testing.T) {
	p := New()
	p.registry = download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(time.Minute, 500*1024*1024),
	)
	catalog := feature.NewRegistry()
	scope := tasks.ScopeIdentity{Owner: "plugin:downloader", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: scope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()
	actions := rootinteraction.NewDispatcher(sessions)
	engine, err := orchestration.New(sessions, actions, &p8ePort{})
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindAssistant(assistantinteraction.DriverRuntime{
		Engine:  engine,
		Catalog: catalog,
		Admit: func(string, feature.InteractionKind, string, int64, presentation.Target) error {
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	state, err := encodeInteractiveState(interactiveState{
		URL:      "https://youtu.be/dQw4w9WgXcQ",
		Provider: "extractor",
		Phase:    phaseVideoFormat,
		Mode:     download.MediaModeVideo,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: p.Name(),
		Binding:   rootinteraction.Binding{ActorID: 42},
		State:     state,
		TTL:       interactiveTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, actionFormatMP4)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data:    data,
		ActorID: 42,
		QueryID: 99,
		Target:  presentationtelegram.InlineTarget{BindingID: "inline:downloader:p8e"},
	})
	if err != nil {
		t.Fatal(err)
	}
	resourceAware, ok := prepared.(orchestration.ResourcePreparedCallback)
	if !ok {
		t.Fatal("final downloader callback did not expose TaskEngine resources")
	}
	resources := resourceAware.Resources()
	if !hasResource(resources, "download") || !hasResource(resources, "process") {
		t.Fatalf("prepared resources=%+v", resources)
	}
	if resourceAware.Scope() != scope {
		t.Fatalf("prepared scope=%+v, want %+v", resourceAware.Scope(), scope)
	}
}

func viewHasAction(rows []presentation.Row, actionID string) bool {
	for _, row := range rows {
		for _, button := range row {
			if button.ActionID == actionID {
				return true
			}
		}
	}
	return false
}
