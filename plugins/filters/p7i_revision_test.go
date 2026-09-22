package filters

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

func TestP7IFilterRevisionIsChatScopedAndKeepsOtherCacheWarm(t *testing.T) {
	p := New(nil, nil)

	p.invalidateChat(10, true)
	rev10 := p.AssistantRuleRevision(10)
	if rev10 == 0 {
		t.Fatal("chat 10 revision did not advance")
	}

	p.invalidateChat(20, true)
	rev20 := p.AssistantRuleRevision(20)
	if rev20 == 0 {
		t.Fatal("chat 20 revision did not advance")
	}
	if got := p.AssistantRuleRevision(10); got != rev10 {
		t.Fatalf("chat 20 mutation changed chat 10 revision: got=%d want=%d", got, rev10)
	}

	cached20 := &compiledFilterSet{}
	if !p.cacheFilters(20, cached20, rev20) {
		t.Fatal("failed to seed chat 20 compiled cache")
	}

	p.invalidateChat(10, true)
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("chat 10 mutation changed chat 20 revision: got=%d want=%d", got, rev20)
	}
	got20, ok := p.getCachedFilters(20, rev20)
	if !ok || got20 != cached20 {
		t.Fatal("chat 10 mutation invalidated chat 20 compiled cache")
	}

	p.invalidateChat(10, false)
	if got := p.AssistantRuleRevision(10); got != 0 {
		t.Fatalf("inactive chat retained revision=%d", got)
	}
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("deactivating chat 10 changed chat 20 revision: got=%d want=%d", got, rev20)
	}
}

type p7iChurningFilterRepo struct {
	plugin *Plugin
}

func (*p7iChurningFilterRepo) SaveFilter(context.Context, int64, string, savedresponse.Response) error {
	return nil
}
func (*p7iChurningFilterRepo) GetFilter(context.Context, int64, string) (*Filter, error) {
	return nil, nil
}
func (r *p7iChurningFilterRepo) ListFilters(_ context.Context, chatID int64) ([]Filter, error) {
	r.plugin.featureState.SetActive(chatID, true)
	r.plugin.invalidateChat(chatID, true)
	return nil, nil
}
func (*p7iChurningFilterRepo) DeleteFilter(context.Context, int64, string) error { return nil }

func TestP7IStaleFilterCompileCannotClearActiveInterest(t *testing.T) {
	repo := &p7iChurningFilterRepo{}
	p := New(repo, nil)
	repo.plugin = p
	p.featureState.ReplaceLoaded([]int64{10})
	p.invalidateChat(10, true)

	_, err := p.compiledFiltersForChat(context.Background(), 10)
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("compiledFiltersForChat error=%v, want ErrConflict after repeated generation churn", err)
	}
	if !p.MessageHookInterested(10) {
		t.Fatal("stale filter compilation cleared active chat interest")
	}
}

type p7iBlockingFilterRepo struct {
	mu          sync.Mutex
	filters     map[int64]map[string]Filter
	saveEnter   chan struct{}
	saveRelease chan struct{}
}

func (r *p7iBlockingFilterRepo) SaveFilter(
	ctx context.Context,
	chatID int64,
	keyword string,
	response savedresponse.Response,
) error {
	select {
	case r.saveEnter <- struct{}{}:
	default:
	}
	select {
	case <-r.saveRelease:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	if r.filters[chatID] == nil {
		r.filters[chatID] = make(map[string]Filter)
	}
	r.filters[chatID][keyword] = Filter{
		ChatID:   chatID,
		Keyword:  keyword,
		Response: response,
	}
	r.mu.Unlock()
	return nil
}

func (r *p7iBlockingFilterRepo) GetFilter(
	_ context.Context,
	chatID int64,
	keyword string,
) (*Filter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	filter, ok := r.filters[chatID][keyword]
	if !ok {
		return nil, nil
	}
	copy := filter
	return &copy, nil
}

func (r *p7iBlockingFilterRepo) ListFilters(
	_ context.Context,
	chatID int64,
) ([]Filter, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	items := make([]Filter, 0, len(r.filters[chatID]))
	for _, filter := range r.filters[chatID] {
		items = append(items, filter)
	}
	return items, nil
}

func (r *p7iBlockingFilterRepo) DeleteFilter(
	_ context.Context,
	chatID int64,
	keyword string,
) error {
	r.mu.Lock()
	delete(r.filters[chatID], keyword)
	r.mu.Unlock()
	return nil
}

func TestP7IFilterMutationLinearizesWithMatcher(t *testing.T) {
	repo := &p7iBlockingFilterRepo{
		filters:     make(map[int64]map[string]Filter),
		saveEnter:   make(chan struct{}, 1),
		saveRelease: make(chan struct{}),
	}
	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	p.featureState.ReplaceLoaded(nil)

	commandCtx := &core.Context{
		Ctx:    context.Background(),
		Chat:   &core.Chat{ID: 77, Type: "supergroup"},
		PeerID: &tg.InputPeerChat{ChatID: 77},
		Svc:    svc,
	}
	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- p.saveFilterResponse(
			commandCtx,
			77,
			"hello",
			savedresponse.NewText("world"),
		)
	}()

	select {
	case <-repo.saveEnter:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for durable filter mutation")
	}

	type matchResult struct {
		matched bool
		err     error
	}
	matchDone := make(chan matchResult, 1)
	go func() {
		_, matched, err := p.matchAssistantRule(context.Background(), &core.MessageEnvelope{
			ChatID: 77,
			Text:   "hello",
		})
		matchDone <- matchResult{matched: matched, err: err}
	}()

	select {
	case got := <-matchDone:
		t.Fatalf("filter matcher escaped mutation fence before commit: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	close(repo.saveRelease)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-matchDone:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !got.matched {
			t.Fatal("filter matcher did not observe newly committed rule")
		}
	case <-time.After(time.Second):
		t.Fatal("filter matcher remained blocked after mutation commit")
	}

	if !p.MessageHookInterested(77) {
		t.Fatal("committed filter mutation did not publish chat interest")
	}
	if p.AssistantRuleRevision(77) == 0 {
		t.Fatal("committed filter mutation did not advance chat generation")
	}
}
