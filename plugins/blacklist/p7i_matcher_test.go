package blacklist

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

func TestP7IBlacklistMatcherPreservesWordBoundarySemantics(t *testing.T) {
	items := compileBlacklist([]string{"scam", "free crypto", "hello"})
	matcher := newBlacklistMatcher(items)

	tests := []struct {
		text string
		want bool
	}{
		{text: "SCAM", want: true},
		{text: "this is a scam!", want: true},
		{text: "hello there", want: true},
		{text: "join free crypto now", want: true},
		{text: "scammer", want: false},
		{text: "free cryptocurrencies", want: false},
		{text: "foo_scam_bar", want: false},
		{text: "🔥scam🔥", want: true},
	}
	for _, tc := range tests {
		if got := matcher.matches(tc.text); got != tc.want {
			t.Fatalf("matches(%q)=%v, want %v", tc.text, got, tc.want)
		}
	}
}

func TestP7IBlacklistMatcherHandlesMaxPerChatWithoutRuleScan(t *testing.T) {
	raw := make([]string, 0, MaxRulesPerChat)
	for i := 0; i < MaxRulesPerChat-1; i++ {
		raw = append(raw, fmt.Sprintf("rule-%03d", i))
	}
	raw = append(raw, "needle phrase")

	matcher := newBlacklistMatcher(compileBlacklist(raw))
	if !matcher.matches("prefix: needle phrase!") {
		t.Fatal("max-cardinality matcher missed terminal rule")
	}
	if matcher.matches("needle phrases") {
		t.Fatal("max-cardinality matcher ignored word boundary")
	}
}

func TestP7IBlacklistRevisionIsChatScoped(t *testing.T) {
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

	p.invalidateChat(10, true)
	if got := p.AssistantRuleRevision(10); got == rev10 || got == 0 {
		t.Fatalf("chat 10 revision did not advance independently: old=%d new=%d", rev10, got)
	}
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("chat 10 mutation changed chat 20 revision: got=%d want=%d", got, rev20)
	}

	p.invalidateChat(10, false)
	if got := p.AssistantRuleRevision(10); got != 0 {
		t.Fatalf("inactive chat retained revision=%d", got)
	}
	if got := p.AssistantRuleRevision(20); got != rev20 {
		t.Fatalf("deactivating chat 10 changed chat 20 revision: got=%d want=%d", got, rev20)
	}
}

type p7iChurningBlacklistRepo struct {
	plugin *Plugin
}

func (*p7iChurningBlacklistRepo) AddBlacklist(context.Context, int64, string) error { return nil }
func (*p7iChurningBlacklistRepo) RemoveBlacklist(context.Context, int64, string) error { return nil }
func (r *p7iChurningBlacklistRepo) ListBlacklists(_ context.Context, chatID int64) ([]string, error) {
	// Simulate a manager mutation after the compiler captured its generation but
	// before it attempts to publish the compiled snapshot.
	r.plugin.featureState.SetActive(chatID, true)
	r.plugin.invalidateChat(chatID, true)
	return nil, nil
}

func TestP7IStaleBlacklistCompileCannotClearActiveInterest(t *testing.T) {
	repo := &p7iChurningBlacklistRepo{}
	p := New(repo, nil)
	repo.plugin = p
	p.featureState.ReplaceLoaded([]int64{10})
	p.invalidateChat(10, true)

	_, err := p.compiledForChat(context.Background(), 10)
	if !errors.Is(err, core.ErrConflict) {
		t.Fatalf("compiledForChat error=%v, want ErrConflict after repeated generation churn", err)
	}
	if !p.MessageHookInterested(10) {
		t.Fatal("stale blacklist compilation cleared active chat interest")
	}
}

type p7iBlacklistMemoryRepo struct {
	words []string
}

func (r *p7iBlacklistMemoryRepo) AddBlacklist(_ context.Context, _ int64, word string) error {
	r.words = append(r.words, word)
	return nil
}

func (r *p7iBlacklistMemoryRepo) RemoveBlacklist(_ context.Context, _ int64, word string) error {
	filtered := r.words[:0]
	for _, current := range r.words {
		if current != word {
			filtered = append(filtered, current)
		}
	}
	r.words = filtered
	return nil
}

func (r *p7iBlacklistMemoryRepo) ListBlacklists(context.Context, int64) ([]string, error) {
	return append([]string(nil), r.words...), nil
}

type p7iBlockingDeleteService struct {
	core.MockTelegramServicer
	entered chan struct{}
	release chan struct{}
}

func (s *p7iBlockingDeleteService) DeleteMessage(
	context.Context,
	tg.InputPeerClass,
	[]int,
) error {
	select {
	case <-s.entered:
	default:
		close(s.entered)
	}
	<-s.release
	return nil
}

func TestP7IBlacklistRemovalWaitsForInFlightDeletion(t *testing.T) {
	repo := &p7iBlacklistMemoryRepo{words: []string{"spam"}}
	p := New(repo, nil)
	p.featureState.ReplaceLoaded([]int64{77})
	p.invalidateChat(77, true)

	message := &core.MessageEnvelope{
		ID:     9,
		ChatID: 77,
		Peer: core.PeerRef{
			Kind:       core.PeerKindChannel,
			ID:         77,
			AccessHash: 177,
		},
		Chat:   core.Chat{ID: 77, Type: string(core.ChatKindSupergroup)},
		Sender: core.User{ID: 42},
		Text:   "spam",
	}
	svc := &p7iBlockingDeleteService{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}

	applyDone := make(chan error, 1)
	go func() {
		_, err := p.ApplyAssistantRule(context.Background(), svc, message)
		applyDone <- err
	}()

	select {
	case <-svc.entered:
	case <-time.After(time.Second):
		t.Fatal("blacklist apply did not reach delete")
	}

	removeDone := make(chan error, 1)
	go func() {
		removeDone <- p.removeBlacklistRule(context.Background(), 77, "spam")
	}()

	select {
	case err := <-removeDone:
		t.Fatalf("rule removal crossed in-flight deletion fence: %v", err)
	case <-time.After(25 * time.Millisecond):
	}

	close(svc.release)
	if err := <-applyDone; err != nil {
		t.Fatal(err)
	}
	if err := <-removeDone; err != nil {
		t.Fatal(err)
	}
	if p.MessageHookInterested(77) {
		t.Fatal("final blacklist removal did not clear chat interest")
	}
}

type p7iBlockingBlacklistRepo struct {
	mu        sync.Mutex
	words     map[int64][]string
	addEnter  chan struct{}
	addRelease chan struct{}
}

func (r *p7iBlockingBlacklistRepo) AddBlacklist(
	ctx context.Context,
	chatID int64,
	word string,
) error {
	select {
	case r.addEnter <- struct{}{}:
	default:
	}
	select {
	case <-r.addRelease:
	case <-ctx.Done():
		return ctx.Err()
	}
	r.mu.Lock()
	r.words[chatID] = append(r.words[chatID], word)
	r.mu.Unlock()
	return nil
}

func (r *p7iBlockingBlacklistRepo) RemoveBlacklist(
	context.Context,
	int64,
	string,
) error {
	return nil
}

func (r *p7iBlockingBlacklistRepo) ListBlacklists(
	_ context.Context,
	chatID int64,
) ([]string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.words[chatID]...), nil
}

func TestP7IBlacklistMutationLinearizesWithMatcher(t *testing.T) {
	repo := &p7iBlockingBlacklistRepo{
		words:      make(map[int64][]string),
		addEnter:   make(chan struct{}, 1),
		addRelease: make(chan struct{}),
	}
	p := New(repo, nil)
	p.featureState.ReplaceLoaded(nil)

	mutationDone := make(chan error, 1)
	go func() {
		mutationDone <- p.addBlacklistRule(context.Background(), 77, "spam")
	}()

	select {
	case <-repo.addEnter:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for durable blacklist mutation")
	}

	type matchResult struct {
		matched bool
		err     error
	}
	matchDone := make(chan matchResult, 1)
	go func() {
		matched, err := p.matchMessage(context.Background(), &core.MessageEnvelope{
			ChatID: 77,
			Text:   "spam",
		})
		matchDone <- matchResult{matched: matched, err: err}
	}()

	select {
	case got := <-matchDone:
		t.Fatalf("matcher escaped mutation fence before commit: %+v", got)
	case <-time.After(25 * time.Millisecond):
	}

	close(repo.addRelease)
	if err := <-mutationDone; err != nil {
		t.Fatal(err)
	}
	select {
	case got := <-matchDone:
		if got.err != nil {
			t.Fatal(got.err)
		}
		if !got.matched {
			t.Fatal("matcher did not observe newly committed blacklist rule")
		}
	case <-time.After(time.Second):
		t.Fatal("matcher remained blocked after mutation commit")
	}

	if !p.MessageHookInterested(77) {
		t.Fatal("committed blacklist mutation did not publish chat interest")
	}
	if p.AssistantRuleRevision(77) == 0 {
		t.Fatal("committed blacklist mutation did not advance chat generation")
	}
}
