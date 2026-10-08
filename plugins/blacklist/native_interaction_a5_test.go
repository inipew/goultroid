package blacklist

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

func makeA5Blacklist(t *testing.T) *Plugin {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	p := NewSQLiteRepository(db)
	return NewWithMessageDeleter(p, nil)
}

// Preserve the production mutation's fresh contextual authorization even
// when testing its repository, snapshot and cancellation failure semantics.
func a5AuthorizedBlacklistDelete(p *Plugin, ctx context.Context, chatID int64, word, digest string) error {
	const actorID int64 = 1001
	session := interaction.Session{Binding: interaction.Binding{ActorID: actorID, ChatID: chatID, MessageID: 100}}
	target := presentationtelegram.MessageTarget{
		Peer:   &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 777},
		ChatID: chatID, MessageID: 100,
	}
	scope := nativeinteraction.GroupActionScope{ChatID: chatID, Kind: core.ChatKindSupergroup}
	roles := &a5BlacklistRoles{role: core.GroupActorRoleAdministrator}
	return p.removeNativeBlacklistIfCurrent(ctx, session, target, scope, word, digest, roles)
}

func TestA5C2BlacklistMenuPaginationAndBoundedState(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	const chatID int64 = 500
	for i := 0; i < 13; i++ {
		word := string(rune('a' + i))
		if err := p.addBlacklistRule(ctx, chatID, word); err != nil {
			t.Fatal(err)
		}
	}
	state := nativeBlacklistState{Scope: nativeinteraction.GroupActionScope{ChatID: chatID, Kind: core.ChatKindSupergroup, TopicID: 55}}
	raw, view, err := p.nativeBlacklistView(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if len(view.Rows) > 8 || len(raw) > 4096 {
		t.Fatalf("unbounded menu: rows=%d state=%d", len(view.Rows), len(raw))
	}
	if !strings.Contains(view.Text, "Halaman 1/3") {
		t.Fatalf("missing pagination: %s", view.Text)
	}
	var restored nativeBlacklistState
	if err := json.Unmarshal(raw, &restored); err != nil {
		t.Fatal(err)
	}
	if restored.Scope.ChatID != chatID || restored.Scope.TopicID != 55 || restored.Digest == "" {
		t.Fatalf("scope/snapshot lost: %+v", restored)
	}
	if len(restored.Choices) > nativeBlacklistSlots {
		t.Fatalf("too many buttons: %d", len(restored.Choices))
	}
	seenNext := false
	for _, choice := range restored.Choices {
		if choice.Kind == "page" && choice.Page == 1 {
			seenNext = true
		}
	}
	if !seenNext {
		t.Fatal("next page action missing")
	}
	state = restored
	state.Page = 1
	if _, view, err = p.nativeBlacklistView(ctx, state); err != nil || !strings.Contains(view.Text, "Halaman 2/3") {
		t.Fatalf("second page: %s (%v)", view.Text, err)
	}
}

func TestA5C2BlacklistConfirmScopedToChatAndSnapshot(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	for _, chatID := range []int64{500, 600} {
		if err := p.addBlacklistRule(ctx, chatID, "same"); err != nil {
			t.Fatal(err)
		}
	}
	words, err := p.db.ListBlacklists(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	digest := blacklistSnapshot(words)
	if err := a5AuthorizedBlacklistDelete(p, ctx, 500, "same", digest); err != nil {
		t.Fatal(err)
	}
	groupA, err := p.db.ListBlacklists(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	groupB, err := p.db.ListBlacklists(ctx, 600)
	if err != nil {
		t.Fatal(err)
	}
	if len(groupA) != 0 || len(groupB) != 1 || groupB[0] != "same" {
		t.Fatalf("cross-chat corruption: a=%v b=%v", groupA, groupB)
	}
	if err := a5AuthorizedBlacklistDelete(p, ctx, 600, "same", digest); err != nil {
		t.Fatal(err)
	}
}

func TestA5C2BlacklistChangedPreviewFailsClosed(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	if err := p.addBlacklistRule(ctx, 500, "scam"); err != nil {
		t.Fatal(err)
	}
	state := nativeBlacklistState{Scope: nativeinteraction.GroupActionScope{ChatID: 500, Kind: core.ChatKindGroup}}
	raw, _, err := p.nativeBlacklistView(ctx, state)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &state); err != nil {
		t.Fatal(err)
	}
	if err := p.addBlacklistRule(ctx, 500, "another"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.nativeBlacklistView(ctx, state); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("changed list accepted: %v", err)
	}
	if err := a5AuthorizedBlacklistDelete(p, ctx, 500, "scam", state.Digest); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale preview deleted: %v", err)
	}
	words, err := p.db.ListBlacklists(ctx, 500)
	if err != nil || len(words) != 2 {
		t.Fatalf("stale delete damaged DB: %v (%v)", words, err)
	}
}

func TestA5C2BlacklistDBFailureCannotRemoveRule(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	if err := p.addBlacklistRule(ctx, 500, "scam"); err != nil {
		t.Fatal(err)
	}
	words, err := p.db.ListBlacklists(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	canceled, cancel := context.WithCancel(ctx)
	cancel()
	if err := a5AuthorizedBlacklistDelete(p, canceled, 500, "scam", blacklistSnapshot(words)); err == nil {
		t.Fatal("canceled DB operation succeeded")
	}
	check, err := p.db.ListBlacklists(ctx, 500)
	if err != nil || len(check) != 1 {
		t.Fatalf("failed mutation removed rule: %v (%v)", check, err)
	}
}

func TestA5C2BlacklistPrivateAndBroadcastScopesRejected(t *testing.T) {
	p := makeA5Blacklist(t)
	for _, kind := range []core.ChatKind{core.ChatKindPrivate, core.ChatKindChannel, core.ChatKindUnknown} {
		if _, _, err := p.nativeBlacklistView(context.Background(), nativeBlacklistState{Scope: nativeinteraction.GroupActionScope{ChatID: 500, Kind: kind}}); !errors.Is(err, core.ErrGroupOnly) {
			t.Fatalf("chat kind %s accepted: %v", kind, err)
		}
	}
	if opened, err := p.openNativeBlacklist(&core.Context{}); opened || err != nil {
		t.Fatalf("native-free fallback broken: opened=%v err=%v", opened, err)
	}
}
