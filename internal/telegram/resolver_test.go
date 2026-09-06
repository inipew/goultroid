package telegram

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
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
}
