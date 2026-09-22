package filters

import (
	"context"
	"errors"
	"testing"

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
