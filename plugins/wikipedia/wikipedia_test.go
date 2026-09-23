package wikipedia

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/platform/network"
	"github.com/inipew/goultroid/internal/plugin"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
)

func TestWikipediaPlugin_InitPlugin(t *testing.T) {
	gate := plugin.NewCapabilityGate()
	netSvc := network.NewService(nil, nil)

	// Denied when capability not in manifest
	gate.Register("wikipedia", []string{})
	pctxDenied := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "wikipedia",
		Gate:    gate,
		Network: netSvc,
	})
	p := New()
	if err := p.InitPlugin(pctxDenied); err == nil {
		t.Fatal("expected capability error when CapHTTP is not registered")
	}

	// Granted when capability registered
	gate.Register("wikipedia", []string{plugin.CapHTTP})
	pctxGranted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner:   "wikipedia",
		Gate:    gate,
		Network: netSvc,
	})
	if err := p.InitPlugin(pctxGranted); err != nil {
		t.Fatalf("unexpected error when CapHTTP is granted: %v", err)
	}
	if p.http == nil {
		t.Fatal("expected http service to be set")
	}
}

func TestWikipediaPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "wikipedia" {
		t.Errorf("got name %q, want wikipedia", p.Name())
	}
	if len(p.Commands()) == 0 {
		t.Fatal("expected commands to be declared")
	}
	cmd := p.Commands()[0]
	if cmd.Name != "wiki" {
		t.Errorf("got command %q, want wiki", cmd.Name)
	}
	if !strings.Contains(cmd.Usage, ".wiki") {
		t.Errorf("unexpected usage %q", cmd.Usage)
	}
	caps := p.Capabilities()
	if len(caps) != 1 || !caps[0].Surfaces.Supports(execution.SourceInline) {
		t.Fatalf("Wikipedia capability does not advertise inline surface: %+v", caps)
	}
}

func TestWikipediaFeatureSpecOwnsPublicInlineLookup(t *testing.T) {
	p := New()
	spec := p.FeatureSpec()
	if err := spec.Validate(); err != nil {
		t.Fatalf("FeatureSpec validation: %v", err)
	}
	if len(spec.Interactions) != 1 {
		t.Fatalf("interactions=%d want 1", len(spec.Interactions))
	}
	decl := spec.Interactions[0]
	if decl.ID != inlineInteractionID || decl.Kind != feature.InteractionInline {
		t.Fatalf("inline declaration=%+v", decl)
	}
	if !decl.Surfaces.Supports(execution.SourceInline) || decl.Policy.Invocation.Inline != core.InvocationAnyone {
		t.Fatalf("inline policy=%+v surfaces=%v", decl.Policy, decl.Surfaces)
	}
	bindings := p.InlineBindings()
	if len(bindings) != 1 || bindings[0].InteractionID != inlineInteractionID || bindings[0].Handler == nil {
		t.Fatalf("inline bindings=%+v", bindings)
	}
}

type fakePageLookup struct {
	pages []searchPage
	err   error
	calls int
	query string
	limit int
}

func (f *fakePageLookup) searchPages(_ context.Context, query string, limit int) ([]searchPage, error) {
	f.calls++
	f.query = query
	f.limit = limit
	return append([]searchPage(nil), f.pages...), f.err
}

func TestWikipediaInlineLookupIsBoundedRichAndGloballyCacheable(t *testing.T) {
	lookup := &fakePageLookup{pages: []searchPage{
		{Key: "Go_(programming_language)", Title: "Go (programming language)", Description: "Programming language", Excerpt: "Go is an <span class=\"searchmatch\">open-source</span> language &amp; toolchain.", Thumbnail: &searchThumbnail{URL: "//upload.wikimedia.org/go.jpg"}},
		{Key: "Go", Title: "Go", Description: "Board game", Excerpt: "Ancient board game"},
		{Key: "Go!", Title: "Go!", Description: "Album", Excerpt: "Album"},
		{Key: "Go_(novel)", Title: "Go (novel)", Description: "Novel", Excerpt: "Novel"},
		{Key: "Go_(film)", Title: "Go (film)", Description: "Film", Excerpt: "Film"},
		{Key: "overflow", Title: "Overflow", Description: "Must not escape bound", Excerpt: "Overflow"},
	}}
	h := &inlineHandler{lookup: lookup}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{"Go", "language"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if lookup.calls != 1 || lookup.query != "Go language" || lookup.limit != maxInlineResults {
		t.Fatalf("lookup calls=%d query=%q limit=%d", lookup.calls, lookup.query, lookup.limit)
	}
	if len(response.Results) != maxInlineResults {
		t.Fatalf("results=%d want %d", len(response.Results), maxInlineResults)
	}
	if response.Cache != inlineservice.CacheGlobal || response.CacheTime != inlineCacheTimeSeconds || response.Private {
		t.Fatalf("cache policy=%v time=%d private=%v", response.Cache, response.CacheTime, response.Private)
	}
	first := response.Results[0]
	if first.ID != "wiki-1" || first.Title != "Go (programming language)" {
		t.Fatalf("first result=%+v", first)
	}
	if strings.Contains(first.Text, "<span") || !strings.Contains(first.Text, "open-source") || !strings.Contains(first.Text, "&amp;") {
		t.Fatalf("rich result text was not sanitized/escaped: %q", first.Text)
	}
	if first.ThumbURL != "https://upload.wikimedia.org/go.jpg" {
		t.Fatalf("thumbnail=%q", first.ThumbURL)
	}
	if !strings.Contains(first.URL, "wikipedia.org/wiki/Go_%28programming_language%29") {
		t.Fatalf("article url=%q", first.URL)
	}
}

func TestWikipediaInlineHelpAndQueryBoundsAvoidNetwork(t *testing.T) {
	lookup := &fakePageLookup{}
	h := &inlineHandler{lookup: lookup}

	response, err := h.HandleInlineV2(&inlineservice.InlineContext{Ctx: context.Background()})
	if err != nil {
		t.Fatal(err)
	}
	if lookup.calls != 0 || len(response.Results) != 1 || response.Results[0].ID != "wiki-help" {
		t.Fatalf("help path calls=%d response=%+v", lookup.calls, response)
	}

	_, err = h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{strings.Repeat("x", maxLookupQueryBytes+1)},
	})
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("long query error=%v want ErrInvalidArgs", err)
	}
	if lookup.calls != 0 {
		t.Fatalf("oversized query reached network %d times", lookup.calls)
	}
}

func TestWikipediaInlineMatcherRequiresKeywordBoundary(t *testing.T) {
	matcher := wikiMatcher{}
	if _, ok := matcher.Match("wikileaks"); ok {
		t.Fatal("wiki matcher accepted a non-boundary prefix")
	}
	args, ok := matcher.Match("wiki distributed systems")
	if !ok || strings.Join(args, " ") != "distributed systems" {
		t.Fatalf("match=%v args=%v", ok, args)
	}
}

func TestWikipediaGetJSONFailsClosedWithoutHTTPCapability(t *testing.T) {
	p := New()
	var target any
	err := p.getJSON(context.Background(), "https://example.invalid", &target)
	if !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("getJSON without HTTP capability error=%v want ErrUnavailable", err)
	}
}
