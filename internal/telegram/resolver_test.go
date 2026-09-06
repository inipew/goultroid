package telegram

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

func TestResolver_BasicParsing(t *testing.T) {
	resolver := NewResolver(nil, nil)
	ctx := context.Background()

	// 1. Empty ref -> ErrInvalidArgs
	_, _, err := resolver.ResolveUser(ctx, "")
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs for empty user ref, got %v", err)
	}

	_, err = resolver.ResolveChat(ctx, "   ")
	if !errors.Is(err, core.ErrInvalidArgs) {
		t.Errorf("expected ErrInvalidArgs for empty chat ref, got %v", err)
	}

	// 2. Numeric user ID fallback
	uPeer, uid, err := resolver.ResolveUser(ctx, "12345678")
	if err != nil {
		t.Fatalf("unexpected error resolving numeric user: %v", err)
	}
	if uid != 12345678 {
		t.Errorf("expected uid 12345678, got %d", uid)
	}
	if up, ok := uPeer.(*tg.InputPeerUser); !ok || up.UserID != 12345678 {
		t.Errorf("expected *tg.InputPeerUser with ID 12345678, got %+v", uPeer)
	}

	// 3. Numeric basic chat ID fallback
	cPeer, err := resolver.ResolveChat(ctx, "888777")
	if err != nil {
		t.Fatalf("unexpected error resolving numeric chat: %v", err)
	}
	if cp, ok := cPeer.(*tg.InputPeerChat); !ok || cp.ChatID != 888777 {
		t.Errorf("expected *tg.InputPeerChat with ID 888777, got %+v", cPeer)
	}

	// 4. Numeric supergroup/channel ID with -100 prefix
	chPeer, err := resolver.ResolveChat(ctx, "-1001234567890")
	if err != nil {
		t.Fatalf("unexpected error resolving -100 channel: %v", err)
	}
	if chp, ok := chPeer.(*tg.InputPeerChannel); !ok || chp.ChannelID != 1234567890 {
		t.Errorf("expected *tg.InputPeerChannel with ID 1234567890, got %+v", chPeer)
	}

	// 4b. Numeric legacy basic chat ID with negative non--100 prefix (Bug 8 fix)
	basicPeer, err := resolver.ResolveChat(ctx, "-12345")
	if err != nil {
		t.Fatalf("unexpected error resolving -12345 basic group: %v", err)
	}
	if bp, ok := basicPeer.(*tg.InputPeerChat); !ok || bp.ChatID != 12345 {
		t.Errorf("expected *tg.InputPeerChat with ID 12345, got %+v", basicPeer)
	}

	// 5. Unknown username without API/peerManager -> ErrNotFound
	_, _, err = resolver.ResolveUser(ctx, "@nonexistent")
	if !errors.Is(err, core.ErrNotFound) {
		t.Errorf("expected ErrNotFound for nonexistent username, got %v", err)
	}

	// 6. Test Resolve unified
	selfPeer, err := resolver.Resolve(ctx, "self")
	if err != nil {
		t.Fatalf("unexpected error resolving self: %v", err)
	}
	if _, ok := selfPeer.(*tg.InputPeerSelf); !ok {
		t.Errorf("expected *tg.InputPeerSelf, got %T", selfPeer)
	}

	userPeer, err := resolver.Resolve(ctx, "999888")
	if err != nil {
		t.Fatalf("unexpected error resolving user via unified Resolve: %v", err)
	}
	if up, ok := userPeer.(*tg.InputPeerUser); !ok || up.UserID != 999888 {
		t.Errorf("expected *tg.InputPeerUser with ID 999888, got %+v", userPeer)
	}

	channelPeer, err := resolver.Resolve(ctx, "-100987654321")
	if err != nil {
		t.Fatalf("unexpected error resolving channel via unified Resolve: %v", err)
	}
	if cp, ok := channelPeer.(*tg.InputPeerChannel); !ok || cp.ChannelID != 987654321 {
		t.Errorf("expected *tg.InputPeerChannel with ID 987654321, got %+v", channelPeer)
	}

	basicUnified, err := resolver.Resolve(ctx, "-54321")
	if err != nil {
		t.Fatalf("unexpected error resolving basic group via unified Resolve: %v", err)
	}
	if bp, ok := basicUnified.(*tg.InputPeerChat); !ok || bp.ChatID != 54321 {
		t.Errorf("expected *tg.InputPeerChat with ID 54321, got %+v", basicUnified)
	}
}

func TestResolver_WithStorage_GuaranteedAccessHash(t *testing.T) {
	ctx := context.Background()
	db, err := database.Open(fmt.Sprintf("file:resolver_test_%d?mode=memory&cache=shared", time.Now().UnixNano()))
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	storage := NewPeerStorage(db)
	resolver := NewResolver(nil, nil)
	resolver.SetStorage(storage)

	// 1. Unknown user with storage configured -> ErrAccessHashMissing (NOT fake user with AccessHash: 0)
	_, _, err = resolver.ResolveUser(ctx, "999888777")
	if !errors.Is(err, core.ErrAccessHashMissing) {
		t.Errorf("expected ErrAccessHashMissing for uncached user with storage, got %v", err)
	}

	// 2. Unknown channel with storage configured -> ErrAccessHashMissing
	_, err = resolver.ResolveChat(ctx, "-1001234567890")
	if !errors.Is(err, core.ErrAccessHashMissing) {
		t.Errorf("expected ErrAccessHashMissing for uncached channel with storage, got %v", err)
	}

	// 3. User present in storage with AccessHash -> Success
	_ = storage.Save(ctx, peers.Key{Prefix: "user", ID: 112233}, peers.Value{AccessHash: 778899})
	uPeer, uid, err := resolver.ResolveUser(ctx, "112233")
	if err != nil {
		t.Fatalf("unexpected error resolving cached user: %v", err)
	}
	if uid != 112233 {
		t.Errorf("expected uid 112233, got %d", uid)
	}
	if up, ok := uPeer.(*tg.InputPeerUser); !ok || up.AccessHash != 778899 {
		t.Errorf("expected *tg.InputPeerUser with AccessHash 778899, got %+v", uPeer)
	}

	// 4. Positive basic group ID: should resolve to InputPeerChat and NOT be shadowed by fake user
	chatPeer, err := resolver.Resolve(ctx, "888777")
	if err != nil {
		t.Fatalf("unexpected error resolving basic chat: %v", err)
	}
	if cp, ok := chatPeer.(*tg.InputPeerChat); !ok || cp.ChatID != 888777 {
		t.Errorf("expected *tg.InputPeerChat with ID 888777, got %+v", chatPeer)
	}
}
