package filters

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

func TestFilterFeatureStatePreloadAndMutation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database open: %v", err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatalf("filters migrations: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SaveFilter(context.Background(), 10, "rules", savedresponse.NewHTML("read them")); err != nil {
		t.Fatalf("seed filter: %v", err)
	}

	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	if err := p.InitContext(context.Background()); err != nil {
		t.Fatalf("init context: %v", err)
	}
	if !p.MessageHookInterested(10) {
		t.Fatal("persisted active filter chat not preloaded")
	}
	if p.MessageHookInterested(20) {
		t.Fatal("inactive filter chat unexpectedly interested")
	}

	cmds := p.Commands()
	byName := make(map[string]core.Command, len(cmds))
	for _, cmd := range cmds {
		byName[cmd.Name] = cmd
	}
	saveCtx := &core.Context{
		Ctx:     context.Background(),
		Args:    []string{"hello", "world"},
		RawArgs: "hello world",
		Chat:    &core.Chat{ID: 20},
		PeerID:  &tg.InputPeerChat{ChatID: 20},
		Svc:     svc,
	}
	beforeRevision := p.AssistantRuleRevision(20)
	if err := byName["filter"].Handler(saveCtx); err != nil {
		t.Fatalf("save filter: %v", err)
	}
	if got := p.AssistantRuleRevision(20); got != beforeRevision+1 {
		t.Fatalf("filter revision=%d, want %d", got, beforeRevision+1)
	}
	if !p.MessageHookInterested(20) {
		t.Fatal("mutation did not mark filter chat active")
	}

	saveCtx.Args = []string{"second", "reply"}
	saveCtx.RawArgs = "second reply"
	if err := byName["filter"].Handler(saveCtx); err != nil {
		t.Fatalf("save second filter: %v", err)
	}

	stopCtx := &core.Context{
		Ctx:    context.Background(),
		Args:   []string{"hello"},
		Chat:   &core.Chat{ID: 20},
		PeerID: &tg.InputPeerChat{ChatID: 20},
		Svc:    svc,
	}
	if err := byName["stop"].Handler(stopCtx); err != nil {
		t.Fatalf("delete first filter: %v", err)
	}
	if !p.MessageHookInterested(20) {
		t.Fatal("chat became inactive while another filter remained")
	}

	stopCtx.Args = []string{"second"}
	if err := byName["stop"].Handler(stopCtx); err != nil {
		t.Fatalf("delete final filter: %v", err)
	}
	if got := p.AssistantRuleRevision(20); got != 0 {
		t.Fatalf("filter revision after final delete=%d, want inactive zero revision", got)
	}
	if p.MessageHookInterested(20) {
		t.Fatal("removing final filter did not mark chat inactive")
	}
}

func TestFilterSQLiteRepositoryListsActiveChats(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database open: %v", err)
	}
	defer db.Close()
	if err := database.RunFeatureMigrations(context.Background(), db, Module); err != nil {
		t.Fatalf("filters migrations: %v", err)
	}

	repo := NewSQLiteRepository(db)
	if err := repo.SaveFilter(context.Background(), 30, "a", savedresponse.NewHTML("1")); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveFilter(context.Background(), 10, "b", savedresponse.NewHTML("2")); err != nil {
		t.Fatal(err)
	}
	if err := repo.SaveFilter(context.Background(), 30, "c", savedresponse.NewHTML("3")); err != nil {
		t.Fatal(err)
	}
	ids, err := repo.ListActiveChatIDs(context.Background())
	if err != nil {
		t.Fatalf("list active chats: %v", err)
	}
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 30 {
		t.Fatalf("active chat ids=%v, want [10 30]", ids)
	}
}
