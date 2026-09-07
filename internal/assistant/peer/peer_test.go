package peer_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/peer"
)

func TestMemoryCache(t *testing.T) {
	c := peer.NewMemoryCache()

	if _, ok := c.Get(peer.PeerKindUser, 100); ok {
		t.Fatalf("expected miss for empty cache")
	}

	c.Put(peer.PeerRecord{
		ID:         100,
		Kind:       peer.PeerKindUser,
		AccessHash: 123456,
		UpdatedAt:  time.Now(),
	})

	rec, ok := c.Get(peer.PeerKindUser, 100)
	if !ok || rec.AccessHash != 123456 {
		t.Fatalf("expected hit with access hash 123456, got ok=%v, rec=%+v", ok, rec)
	}

	c.Invalidate(peer.PeerKindUser, 100)
	if _, ok := c.Get(peer.PeerKindUser, 100); ok {
		t.Fatalf("expected miss after invalidate")
	}
}

func TestMemoryCache_CacheEntities(t *testing.T) {
	c := peer.NewMemoryCache()

	entities := tg.Entities{
		Users: map[int64]*tg.User{
			1001: {ID: 1001, AccessHash: 555},
			1002: {ID: 1002, AccessHash: 0}, // should be ignored
		},
		Channels: map[int64]*tg.Channel{
			2001: {ID: 2001, AccessHash: 777},
		},
	}

	c.CacheEntities(entities)

	if rec, ok := c.Get(peer.PeerKindUser, 1001); !ok || rec.AccessHash != 555 {
		t.Fatalf("expected user 1001 cached with 555")
	}
	if _, ok := c.Get(peer.PeerKindUser, 1002); ok {
		t.Fatalf("expected user 1002 with 0 hash not cached")
	}
	if rec, ok := c.Get(peer.PeerKindChannel, 2001); !ok || rec.AccessHash != 777 {
		t.Fatalf("expected channel 2001 cached with 777")
	}
}

func TestDefaultResolver_Resolve(t *testing.T) {
	c := peer.NewMemoryCache()
	res := peer.NewResolver(c)
	ctx := context.Background()

	// 1. User resolution with missing access hash
	pUser := &tg.PeerUser{UserID: 42}
	_, err := res.Resolve(ctx, pUser, 0, tg.Entities{})
	if !errors.Is(err, peer.ErrAccessHashMissing) {
		t.Fatalf("expected ErrAccessHashMissing for user without hash, got %v", err)
	}

	// 2. User resolution with entities
	entities := tg.Entities{
		Users: map[int64]*tg.User{
			42: {ID: 42, AccessHash: 9999},
		},
	}
	resolvedUser, err := res.Resolve(ctx, pUser, 0, entities)
	if err != nil {
		t.Fatalf("unexpected error resolving user: %v", err)
	}
	inputUser, ok := resolvedUser.(*tg.InputPeerUser)
	if !ok || inputUser.UserID != 42 || inputUser.AccessHash != 9999 {
		t.Fatalf("unexpected input user: %+v", resolvedUser)
	}

	// 3. User resolution from cache on subsequent call
	resolvedCached, err := res.Resolve(ctx, pUser, 0, tg.Entities{})
	if err != nil {
		t.Fatalf("unexpected error resolving cached user: %v", err)
	}
	if inputUser, ok := resolvedCached.(*tg.InputPeerUser); !ok || inputUser.AccessHash != 9999 {
		t.Fatalf("expected cache hit with 9999, got %+v", resolvedCached)
	}

	// 4. Basic Chat resolution
	pChat := &tg.PeerChat{ChatID: 888}
	resolvedChat, err := res.Resolve(ctx, pChat, 0, tg.Entities{})
	if err != nil {
		t.Fatalf("unexpected error resolving chat: %v", err)
	}
	if inputChat, ok := resolvedChat.(*tg.InputPeerChat); !ok || inputChat.ChatID != 888 {
		t.Fatalf("expected InputPeerChat with ChatID 888, got %+v", resolvedChat)
	}

	// 5. Channel resolution
	pChan := &tg.PeerChannel{ChannelID: 777}
	chanEntities := tg.Entities{
		Channels: map[int64]*tg.Channel{
			777: {ID: 777, AccessHash: 4444},
		},
	}
	resolvedChan, err := res.Resolve(ctx, pChan, 0, chanEntities)
	if err != nil {
		t.Fatalf("unexpected error resolving channel: %v", err)
	}
	if inputChan, ok := resolvedChan.(*tg.InputPeerChannel); !ok || inputChan.AccessHash != 4444 {
		t.Fatalf("expected InputPeerChannel with AccessHash 4444, got %+v", resolvedChan)
	}

	// 6. Nil peer with senderID fallback
	resolvedSender, err := res.Resolve(ctx, nil, 42, tg.Entities{})
	if err != nil {
		t.Fatalf("unexpected error resolving sender fallback: %v", err)
	}
	if inputUser, ok := resolvedSender.(*tg.InputPeerUser); !ok || inputUser.UserID != 42 || inputUser.AccessHash != 9999 {
		t.Fatalf("expected sender fallback to user 42 with 9999, got %+v", resolvedSender)
	}

	// 7. InvalidatePeer removes cached peer
	res.InvalidatePeer(inputUser)
	if _, ok := c.Get(peer.PeerKindUser, 42); ok {
		t.Fatalf("expected user 42 to be removed after InvalidatePeer")
	}

	// 8. ReResolve after invalidation fails
	_, err = res.ReResolve(ctx, inputUser)
	if !errors.Is(err, peer.ErrAccessHashMissing) {
		t.Fatalf("expected ErrAccessHashMissing on ReResolve after invalidation, got %v", err)
	}

	// Put back hash and verify ReResolve succeeds
	c.Put(peer.PeerRecord{ID: 42, Kind: peer.PeerKindUser, AccessHash: 8888})
	reresolved, err := res.ReResolve(ctx, inputUser)
	if err != nil {
		t.Fatalf("unexpected error on ReResolve: %v", err)
	}
	if inpUser, ok := reresolved.(*tg.InputPeerUser); !ok || inpUser.AccessHash != 8888 {
		t.Fatalf("expected ReResolve with 8888, got %+v", reresolved)
	}

	// 9. ReResolve with EntityFetcher on cache miss
	res.InvalidatePeer(inputUser)
	mockFetcher := &mockEntityFetcher{
		userHash: map[int64]int64{42: 77777},
	}
	res.SetEntityFetcher(mockFetcher)
	fetched, err := res.ReResolve(ctx, inputUser)
	if err != nil {
		t.Fatalf("unexpected error on ReResolve with fetcher: %v", err)
	}
	if inpUser, ok := fetched.(*tg.InputPeerUser); !ok || inpUser.AccessHash != 77777 {
		t.Fatalf("expected ReResolve with 77777 from fetcher, got %+v", fetched)
	}
	if mockFetcher.userCalls != 1 {
		t.Fatalf("expected 1 fetch call, got %d", mockFetcher.userCalls)
	}
}

type mockEntityFetcher struct {
	userHash  map[int64]int64
	chanHash  map[int64]int64
	userCalls int
}

func (m *mockEntityFetcher) FetchUser(ctx context.Context, id int64) (*tg.User, error) {
	m.userCalls++
	if h, ok := m.userHash[id]; ok {
		return &tg.User{ID: id, AccessHash: h}, nil
	}
	return nil, errors.New("user not found")
}

func (m *mockEntityFetcher) FetchChannel(ctx context.Context, id int64) (*tg.Channel, error) {
	if h, ok := m.chanHash[id]; ok {
		return &tg.Channel{ID: id, AccessHash: h}, nil
	}
	return nil, errors.New("channel not found")
}

func TestResolver_Resolve_AutoFetchOnCacheMiss(t *testing.T) {
	ctx := context.Background()
	cache := peer.NewMemoryCache()
	res := peer.NewResolver(cache)

	mockFetcher := &mockEntityFetcher{
		userHash: map[int64]int64{101: 55555},
		chanHash: map[int64]int64{202: 66666},
	}
	res.SetEntityFetcher(mockFetcher)

	// User auto fetch on cache miss
	pUser, err := res.Resolve(ctx, &tg.PeerUser{UserID: 101}, 0, tg.Entities{})
	if err != nil {
		t.Fatalf("expected auto-fetch to resolve user, got %v", err)
	}
	if inpUser, ok := pUser.(*tg.InputPeerUser); !ok || inpUser.AccessHash != 55555 {
		t.Fatalf("expected access hash 55555, got %+v", pUser)
	}

	// Channel auto fetch on cache miss
	pChan, err := res.Resolve(ctx, &tg.PeerChannel{ChannelID: 202}, 0, tg.Entities{})
	if err != nil {
		t.Fatalf("expected auto-fetch to resolve channel, got %v", err)
	}
	if inpChan, ok := pChan.(*tg.InputPeerChannel); !ok || inpChan.AccessHash != 66666 {
		t.Fatalf("expected access hash 66666, got %+v", pChan)
	}

	// Nil peer with senderID auto fetch
	pSender, err := res.Resolve(ctx, nil, 101, tg.Entities{})
	if err != nil {
		t.Fatalf("expected sender auto-fetch, got %v", err)
	}
	if inpUser, ok := pSender.(*tg.InputPeerUser); !ok || inpUser.AccessHash != 55555 {
		t.Fatalf("expected access hash 55555 for sender, got %+v", pSender)
	}
}

type mockFetcherAPI struct {
	users   []tg.UserClass
	chats   tg.MessagesChatsClass
	userErr error
	chanErr error
}

func (m *mockFetcherAPI) UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error) {
	if m.userErr != nil {
		return nil, m.userErr
	}
	return m.users, nil
}

func (m *mockFetcherAPI) ChannelsGetChannels(ctx context.Context, id []tg.InputChannelClass) (tg.MessagesChatsClass, error) {
	if m.chanErr != nil {
		return nil, m.chanErr
	}
	return m.chats, nil
}

func TestTelegramEntityFetcher(t *testing.T) {
	ctx := context.Background()
	api := &mockFetcherAPI{
		users: []tg.UserClass{
			&tg.User{ID: 12345, AccessHash: 98765},
		},
		chats: &tg.MessagesChats{
			Chats: []tg.ChatClass{
				&tg.Channel{ID: 54321, AccessHash: 13579},
			},
		},
	}

	fetcher := peer.NewTelegramEntityFetcher(api)

	// FetchUser success
	u, err := fetcher.FetchUser(ctx, 12345)
	if err != nil {
		t.Fatalf("FetchUser failed: %v", err)
	}
	if u.AccessHash != 98765 {
		t.Fatalf("expected access hash 98765, got %d", u.AccessHash)
	}

	// FetchUser not found
	_, err = fetcher.FetchUser(ctx, 99999)
	if !errors.Is(err, peer.ErrPeerResolution) {
		t.Fatalf("expected ErrPeerResolution for unknown user, got %v", err)
	}

	// FetchChannel success
	ch, err := fetcher.FetchChannel(ctx, 54321)
	if err != nil {
		t.Fatalf("FetchChannel failed: %v", err)
	}
	if ch.AccessHash != 13579 {
		t.Fatalf("expected access hash 13579, got %d", ch.AccessHash)
	}

	// FetchChannel not found
	_, err = fetcher.FetchChannel(ctx, 99999)
	if !errors.Is(err, peer.ErrPeerResolution) {
		t.Fatalf("expected ErrPeerResolution for unknown channel, got %v", err)
	}
}
