package download

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/tasks"
)

type extractorSearchFakeRunner struct {
	result      *process.Result
	err         error
	requests    []process.Request
	heldProcess bool
}

func (r *extractorSearchFakeRunner) Run(ctx context.Context, req process.Request) (*process.Result, error) {
	r.requests = append(r.requests, req)
	r.heldProcess = tasks.HasHeldResource(ctx, "process")
	return r.result, r.err
}

type extractorSearchTicket struct {
	id     tasks.TaskID
	result tasks.TaskResult
	done   chan struct{}
}

func (t *extractorSearchTicket) TaskID() tasks.TaskID { return t.id }
func (t *extractorSearchTicket) State() tasks.TaskState {
	if t.result.IsSuccess() {
		return tasks.StateCompleted
	}
	return tasks.StateFailed
}
func (t *extractorSearchTicket) Done() <-chan struct{}                          { return t.done }
func (t *extractorSearchTicket) Result() (tasks.TaskResult, bool)               { return t.result, true }
func (t *extractorSearchTicket) Wait(context.Context) (tasks.TaskResult, error) { return t.result, nil }

type extractorSearchTaskClient struct {
	submits int
	spec    tasks.WorkSpec
}

func (c *extractorSearchTaskClient) Submit(ctx context.Context, spec tasks.WorkSpec) (tasks.Ticket, error) {
	c.submits++
	c.spec = spec
	runCtx := ctx
	for _, resource := range spec.Resources {
		if resource.Name == "process" && resource.Amount > 0 {
			runCtx = tasks.WithHeldResource(runCtx, "process")
		}
	}
	runErr := spec.Handler(runCtx)
	result := tasks.TaskResult{TaskID: spec.ID, Outcome: tasks.OutcomeCompleted}
	if runErr != nil {
		result.Outcome = tasks.OutcomeFailed
		result.Failure = tasks.FailureInfo{Message: runErr.Error()}
	}
	done := make(chan struct{})
	close(done)
	return &extractorSearchTicket{id: spec.ID, result: result, done: done}, nil
}
func (*extractorSearchTaskClient) Cancel(tasks.TaskID, tasks.Cause) (tasks.CancelReceipt, error) {
	return tasks.CancelReceipt{}, nil
}
func (*extractorSearchTaskClient) CancelScope(tasks.ScopeIdentity, tasks.Cause) int { return 0 }
func (*extractorSearchTaskClient) Snapshot(tasks.TaskID) (tasks.TaskSnapshot, bool) {
	return tasks.TaskSnapshot{}, false
}

func searchFixtureJSON() string {
	return `{"entries":[
{"id":"abcdefghijk","title":"First Video","description":"first description","thumbnail":"http://unsafe.example/thumb.jpg","thumbnails":[{"url":"https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg"}],"channel":"First Channel","duration":305.9,"view_count":12345,"upload_date":"20260901"},
{"id":"lmnopqrstuv","title":"Second Video","description":"second description","thumbnail":"https://i.ytimg.com/vi/lmnopqrstuv/hqdefault.jpg","uploader":"Second Uploader","duration":10,"view_count":42},
{"id":"bad","title":"Invalid ID"}]}`
}

func newSearchExtractor(runner process.Runner) *ExtractorProvider {
	provider := NewExtractorProvider(runner, 500*1024*1024)
	provider.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }
	return provider
}

func TestExtractorSearchUsesBoundedStructuredYTDLPQuery(t *testing.T) {
	runner := &extractorSearchFakeRunner{result: &process.Result{Stdout: searchFixtureJSON()}}
	provider := newSearchExtractor(runner)
	ctx := tasks.WithHeldResource(context.Background(), "process")
	results, err := provider.Search(ctx, "avenged sevenfold", SearchOptions{Limit: 2})
	if err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("results = %d, want 2", len(results))
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner calls = %d, want 1", len(runner.requests))
	}
	req := runner.requests[0]
	wantArgs := []string{"--ignore-config", "--no-warnings", "--simulate", "--flat-playlist", "--dump-single-json", "ytsearch2:avenged sevenfold"}
	if strings.Join(req.Args, "\x00") != strings.Join(wantArgs, "\x00") {
		t.Fatalf("args = %q, want %q", req.Args, wantArgs)
	}
	if req.Timeout != DefaultSearchTimeout || req.MaxOutput != extractorSearchMaxOutput {
		t.Fatalf("request bounds = timeout:%v maxOutput:%d", req.Timeout, req.MaxOutput)
	}
	first := results[0]
	if first.Provider != "extractor" || first.Source != "youtube" || first.SourceID != "abcdefghijk" {
		t.Fatalf("first identity = %+v", first)
	}
	if first.URL != "https://www.youtube.com/watch?v=abcdefghijk" {
		t.Fatalf("canonical URL = %q", first.URL)
	}
	if first.Thumbnail != "https://i.ytimg.com/vi/abcdefghijk/hqdefault.jpg" {
		t.Fatalf("thumbnail = %q", first.Thumbnail)
	}
	if first.Channel != "First Channel" || first.DurationSeconds != 305 || first.Views != 12345 || first.PublishedAt != "20260901" {
		t.Fatalf("normalized metadata = %+v", first)
	}
	if results[1].Channel != "Second Uploader" {
		t.Fatalf("uploader fallback = %+v", results[1])
	}
}

func TestExtractorSearchClampsBoundsAndRejectsInvalidQuery(t *testing.T) {
	runner := &extractorSearchFakeRunner{result: &process.Result{Stdout: searchFixtureJSON()}}
	provider := newSearchExtractor(runner)
	ctx := tasks.WithHeldResource(context.Background(), "process")
	if _, err := provider.Search(ctx, "", SearchOptions{}); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("empty query error = %v, want ErrInvalidArgs", err)
	}
	if _, err := provider.Search(ctx, strings.Repeat("x", MaxSearchQueryBytes+1), SearchOptions{}); !errors.Is(err, core.ErrInvalidArgs) {
		t.Fatalf("oversized query error = %v, want ErrInvalidArgs", err)
	}
	if _, err := provider.Search(ctx, "query", SearchOptions{Limit: 100, Timeout: time.Hour}); err != nil {
		t.Fatalf("bounded Search() error = %v", err)
	}
	req := runner.requests[len(runner.requests)-1]
	if got := req.Args[len(req.Args)-1]; got != "ytsearch5:query" {
		t.Fatalf("bounded target = %q", got)
	}
	if req.Timeout != MaxSearchTimeout {
		t.Fatalf("timeout = %v, want %v", req.Timeout, MaxSearchTimeout)
	}
}

func TestExtractorSearchRejectsMalformedTruncatedAndEmptyOutput(t *testing.T) {
	ctx := tasks.WithHeldResource(context.Background(), "process")
	provider := newSearchExtractor(&extractorSearchFakeRunner{result: &process.Result{Stdout: "{"}})
	if _, err := provider.Search(ctx, "query", SearchOptions{}); !errors.Is(err, ErrSearchFailed) {
		t.Fatalf("malformed output error = %v, want ErrSearchFailed", err)
	}
	provider = newSearchExtractor(&extractorSearchFakeRunner{result: &process.Result{Stdout: searchFixtureJSON(), Truncated: true}})
	if _, err := provider.Search(ctx, "query", SearchOptions{}); !errors.Is(err, core.ErrResourceLimit) {
		t.Fatalf("truncated output error = %v, want ErrResourceLimit", err)
	}
	provider = newSearchExtractor(&extractorSearchFakeRunner{result: &process.Result{Stdout: `{"entries":[{"id":"bad","title":"invalid"}]}`}})
	if _, err := provider.Search(ctx, "query", SearchOptions{}); !errors.Is(err, ErrSearchNoResults) {
		t.Fatalf("empty normalized results error = %v, want ErrSearchNoResults", err)
	}
}

func TestExtractorSearchAcquiresOnlyProcessResource(t *testing.T) {
	runner := &extractorSearchFakeRunner{result: &process.Result{Stdout: searchFixtureJSON()}}
	provider := newSearchExtractor(runner)
	client := &extractorSearchTaskClient{}
	provider.SetTasks(client)
	if _, err := provider.Search(context.Background(), "query", SearchOptions{Limit: 1}); err != nil {
		t.Fatalf("Search() error = %v", err)
	}
	if client.submits != 1 {
		t.Fatalf("task submissions = %d, want 1", client.submits)
	}
	if client.spec.Class != tasks.PriorityInteractive {
		t.Fatalf("priority = %q, want interactive", client.spec.Class)
	}
	if len(client.spec.Resources) != 1 || client.spec.Resources[0].Name != "process" || client.spec.Resources[0].Amount != 1 {
		t.Fatalf("resources = %+v, want process=1", client.spec.Resources)
	}
	if !runner.heldProcess {
		t.Fatal("runner did not execute with process resource held")
	}
}

func TestExtractorSearchUnavailable(t *testing.T) {
	provider := NewExtractorProvider(&extractorSearchFakeRunner{}, 500*1024*1024)
	provider.lookPath = func(string) (string, error) { return "", errors.New("missing") }
	if _, err := provider.Search(context.Background(), "query", SearchOptions{}); !errors.Is(err, ErrExtractorUnavailable) {
		t.Fatalf("error = %v, want ErrExtractorUnavailable", err)
	}
}
