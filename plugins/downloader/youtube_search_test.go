package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
)

type youtubeSearchTestTicket struct {
	id     tasks.TaskID
	result tasks.TaskResult
	done   chan struct{}
}

func (t *youtubeSearchTestTicket) TaskID() tasks.TaskID             { return t.id }
func (t *youtubeSearchTestTicket) State() tasks.TaskState           { return tasks.StateCompleted }
func (t *youtubeSearchTestTicket) Done() <-chan struct{}            { return t.done }
func (t *youtubeSearchTestTicket) Result() (tasks.TaskResult, bool) { return t.result, true }
func (t *youtubeSearchTestTicket) Wait(context.Context) (tasks.TaskResult, error) {
	return t.result, nil
}

type youtubeSearchTaskClient struct {
	tasks.Client
	specs []tasks.WorkSpec
}

func (c *youtubeSearchTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.specs = append(c.specs, spec)
	runCtx := tasks.WithHeldResources(ctx, spec.Resources)
	runErr := spec.Handler(runCtx)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if runErr != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure = tasks.FailureInfo{Message: runErr.Error()}
	}
	done := make(chan struct{})
	close(done)
	return &youtubeSearchTestTicket{id: spec.ID, result: result, done: done}, nil
}

type youtubeSearchProvider struct {
	results     []download.SearchResult
	err         error
	query       string
	opts        download.SearchOptions
	sawProcess  bool
	sawDownload bool
}

func (*youtubeSearchProvider) Name() string { return "extractor" }
func (*youtubeSearchProvider) Match(rawURL string) bool {
	return strings.Contains(rawURL, "youtube.com/watch?v=") || strings.Contains(rawURL, "youtu.be/")
}
func (*youtubeSearchProvider) Download(context.Context, string, storage.Storage, download.DownloadOptions) (*storage.Asset, error) {
	return nil, errors.New("not used")
}
func (p *youtubeSearchProvider) Search(ctx context.Context, query string, opts download.SearchOptions) ([]download.SearchResult, error) {
	p.query = query
	p.opts = opts
	p.sawProcess = tasks.HasHeldResource(ctx, "process")
	p.sawDownload = tasks.HasHeldResource(ctx, "download")
	return p.results, p.err
}

func TestYouTubeMatcherSharesCanonicalDownloaderBinding(t *testing.T) {
	matcher := downloaderMatcher{}
	if args, ok := matcher.Match("yt avenged sevenfold"); !ok || len(args) != 2 || args[0] != "avenged" || args[1] != "sevenfold" {
		t.Fatalf("yt matcher ok=%v args=%v", ok, args)
	}
	if args, ok := matcher.Match("yt"); !ok || len(args) != 0 {
		t.Fatalf("bare yt matcher ok=%v args=%v", ok, args)
	}
	if _, ok := matcher.Match("ytfoo song"); ok {
		t.Fatal("yt prefix collision unexpectedly matched")
	}
	p := New()
	if got := len(p.InlineBindings()); got != 1 {
		t.Fatalf("inline bindings=%d, want canonical single binding", got)
	}
}

func TestYouTubeEmptyQueryDoesNotEnterTaskEngine(t *testing.T) {
	client := &youtubeSearchTaskClient{}
	p := New(client)
	h := &interactiveInlineHandler{plugin: p}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:      context.Background(),
		RawQuery: "yt",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 0 {
		t.Fatalf("empty yt query submitted %d tasks", len(client.specs))
	}
	if response == nil || !response.Private || response.Cache != inlineservice.CacheNone || len(response.Results) != 1 {
		t.Fatalf("unexpected help response: %+v", response)
	}
	if response.Results[0].Title != "YouTube search" || len(response.Results[0].InteractionState) != 0 {
		t.Fatalf("unexpected help result: %+v", response.Results[0])
	}
}

func TestYouTubeSearchUsesProcessOnlyAndConvergesOnP8EState(t *testing.T) {
	provider := &youtubeSearchProvider{results: []download.SearchResult{{
		Provider:        "extractor",
		Source:          "youtube",
		SourceID:        "abcdefghijk",
		URL:             "https://www.youtube.com/watch?v=abcdefghijk",
		Title:           "First Video",
		Channel:         "First Channel",
		DurationSeconds: 305,
		Views:           12345,
		Thumbnail:       "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg",
	}}}
	client := &youtubeSearchTaskClient{}
	p := New(client)
	p.registry = download.NewRegistry(provider)
	h := &interactiveInlineHandler{plugin: p}

	response, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:      context.Background(),
		RawQuery: "yt first video",
		Args:     []string{"first", "video"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(client.specs) != 1 {
		t.Fatalf("search tasks=%d, want 1", len(client.specs))
	}
	spec := client.specs[0]
	if spec.Class != tasks.PriorityInteractive || spec.ExecutionTimeout != youtubeInlineSearchTimeout {
		t.Fatalf("search task class/timeout=%q/%v", spec.Class, spec.ExecutionTimeout)
	}
	if len(spec.Resources) != 1 || spec.Resources[0].Name != "process" || spec.Resources[0].Amount != 1 {
		t.Fatalf("search resources=%+v, want process=1 only", spec.Resources)
	}
	if !provider.sawProcess || provider.sawDownload {
		t.Fatalf("held resources process=%v download=%v", provider.sawProcess, provider.sawDownload)
	}
	if provider.query != "first video" || provider.opts.Limit != download.MaxSearchLimit || provider.opts.Timeout != youtubeInlineSearchTimeout {
		t.Fatalf("search request query=%q opts=%+v", provider.query, provider.opts)
	}
	if response == nil || !response.Private || response.Cache != inlineservice.CacheNone || len(response.Results) != 1 {
		t.Fatalf("unexpected search response: %+v", response)
	}
	result := response.Results[0]
	if result.Title != "First Video" || result.ThumbURL == "" || result.URL != provider.results[0].URL {
		t.Fatalf("rich result=%+v", result)
	}
	if !strings.Contains(result.Description, "First Channel") || !strings.Contains(result.Description, "5:05") || !strings.Contains(result.Description, "12345 views") {
		t.Fatalf("description=%q", result.Description)
	}
	if !viewHasAction(result.ActionRows, actionAudio) || !viewHasAction(result.ActionRows, actionVideo) {
		t.Fatalf("search action rows=%+v", result.ActionRows)
	}
	searchState, err := decodeInteractiveState(result.InteractionState)
	if err != nil {
		t.Fatal(err)
	}
	if searchState.Provider != "extractor" || searchState.Phase != phaseChoose || searchState.URL != provider.results[0].URL {
		t.Fatalf("search state=%+v", searchState)
	}

	direct, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:      context.Background(),
		RawQuery: "dl " + provider.results[0].URL,
		Args:     []string{provider.results[0].URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	directState, err := decodeInteractiveState(direct.Results[0].InteractionState)
	if err != nil {
		t.Fatal(err)
	}
	if directState != searchState {
		t.Fatalf("search/direct state diverged: search=%+v direct=%+v", searchState, directState)
	}
}

func TestYouTubeSearchNoResultsReturnsNonInteractiveHelp(t *testing.T) {
	provider := &youtubeSearchProvider{err: download.ErrSearchNoResults}
	client := &youtubeSearchTaskClient{}
	p := New(client)
	p.registry = download.NewRegistry(provider)
	h := &interactiveInlineHandler{plugin: p}
	response, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:      context.Background(),
		RawQuery: "yt impossible query",
		Args:     []string{"impossible", "query"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Title != "No YouTube results" || len(response.Results[0].InteractionState) != 0 {
		t.Fatalf("unexpected no-results response: %+v", response)
	}
}

func TestYouTubeSearchRejectsOversizedQueryBeforeAdmission(t *testing.T) {
	client := &youtubeSearchTaskClient{}
	p := New(client)
	h := &interactiveInlineHandler{plugin: p}
	query := strings.Repeat("x", download.MaxSearchQueryBytes+1)
	_, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:      context.Background(),
		RawQuery: "yt " + query,
		Args:     []string{query},
	})
	if err == nil {
		t.Fatal("expected oversized query error")
	}
	if len(client.specs) != 0 {
		t.Fatalf("oversized query submitted %d tasks", len(client.specs))
	}
}
