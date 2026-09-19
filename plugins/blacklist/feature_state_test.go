package blacklist

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestBlacklistFeatureStatePreloadAndMutation(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database open: %v", err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	if err := repo.AddBlacklist(context.Background(), 10, "spam"); err != nil {
		t.Fatalf("seed blacklist: %v", err)
	}

	svc := &mockService{}
	p := New(repo, func() core.TelegramServicer { return svc })
	if err := p.InitContext(context.Background()); err != nil {
		t.Fatalf("init context: %v", err)
	}
	if !p.MessageHookInterested(10) {
		t.Fatal("persisted active blacklist chat not preloaded")
	}
	if p.MessageHookInterested(20) {
		t.Fatal("inactive blacklist chat unexpectedly interested")
	}

	cmds := p.Commands()
	byName := make(map[string]core.Command, len(cmds))
	for _, cmd := range cmds {
		byName[cmd.Name] = cmd
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Args:    []string{"scam"},
		RawArgs: "scam",
		Chat:    &core.Chat{ID: 20},
		PeerID:  &tg.InputPeerChat{ChatID: 20},
		Svc:     svc,
	}
	if err := byName["blacklist"].Handler(ctx); err != nil {
		t.Fatalf("add blacklist: %v", err)
	}
	if !p.MessageHookInterested(20) {
		t.Fatal("mutation did not mark blacklist chat active")
	}

	ctx.Args = []string{"phish"}
	ctx.RawArgs = "phish"
	if err := byName["blacklist"].Handler(ctx); err != nil {
		t.Fatalf("add second blacklist: %v", err)
	}

	ctx.Args = []string{"scam"}
	ctx.RawArgs = "scam"
	if err := byName["unblacklist"].Handler(ctx); err != nil {
		t.Fatalf("remove first blacklist: %v", err)
	}
	if !p.MessageHookInterested(20) {
		t.Fatal("chat became inactive while another blacklist item remained")
	}

	ctx.Args = []string{"phish"}
	ctx.RawArgs = "phish"
	if err := byName["unblacklist"].Handler(ctx); err != nil {
		t.Fatalf("remove final blacklist: %v", err)
	}
	if p.MessageHookInterested(20) {
		t.Fatal("removing final blacklist item did not mark chat inactive")
	}
}

func TestBlacklistSQLiteRepositoryListsActiveChats(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("database open: %v", err)
	}
	defer db.Close()

	repo := NewSQLiteRepository(db)
	for _, chatID := range []int64{30, 10, 30} {
		word := "one"
		if chatID == 30 {
			word = "word"
		}
		if err := repo.AddBlacklist(context.Background(), chatID, word); err != nil {
			t.Fatalf("seed chat %d: %v", chatID, err)
		}
	}
	ids, err := repo.ListActiveChatIDs(context.Background())
	if err != nil {
		t.Fatalf("list active chats: %v", err)
	}
	if len(ids) != 2 || ids[0] != 10 || ids[1] != 30 {
		t.Fatalf("active chat ids=%v, want [10 30]", ids)
	}
}
