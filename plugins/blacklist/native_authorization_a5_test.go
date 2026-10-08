package blacklist

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/interaction"
	nativeinteraction "github.com/inipew/goultroid/internal/interaction/native"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

// This resolver proves that contextual authority is queried while the same
// per-chat rule lock is held that protects the subsequent SQLite mutation.
type a5BlacklistLockProbe struct {
	lock            *sync.RWMutex
	role            core.GroupActorRole
	deleteMessages  bool
	freshCalls      int
	checkedWithLock bool
}

func (r *a5BlacklistLockProbe) ResolveGroupRole(ctx context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	return r.ResolveGroupRoleFresh(ctx, request)
}

func (r *a5BlacklistLockProbe) ResolveGroupRoleFresh(_ context.Context, request core.GroupRoleRequest) (core.GroupRoleSnapshot, error) {
	r.freshCalls++
	if r.lock.TryLock() {
		r.lock.Unlock()
	} else {
		r.checkedWithLock = true
	}
	return core.GroupRoleSnapshot{
		Principal: core.GroupActorPrincipal{
			UserID: request.UserID, Verified: true, Role: r.role,
			Rights: core.GroupAdminRights{DeleteMessages: r.deleteMessages},
		},
	}, nil
}

func a5BlacklistMutationBinding(chatID int64) (interaction.Session, presentationtelegram.MessageTarget, nativeinteraction.GroupActionScope) {
	const actorID int64 = 1001
	session := interaction.Session{Binding: interaction.Binding{ActorID: actorID, ChatID: chatID, MessageID: 100}}
	target := presentationtelegram.MessageTarget{
		Peer:   &tg.InputPeerChannel{ChannelID: chatID, AccessHash: 777},
		ChatID: chatID, MessageID: 100,
	}
	scope := nativeinteraction.GroupActionScope{ChatID: chatID, Kind: core.ChatKindSupergroup, TopicID: 55}
	return session, target, scope
}

func TestA5FinalBlacklistFreshRoleIsCheckedInsideMutationLock(t *testing.T) {
	for _, tc := range []struct {
		name           string
		role           core.GroupActorRole
		deleteMessages bool
		allowed        bool
	}{
		{name: "administrator with delete rights", role: core.GroupActorRoleAdministrator, deleteMessages: true, allowed: true},
		{name: "demoted administrator", role: core.GroupActorRoleMember, deleteMessages: true},
		{name: "delete right revoked", role: core.GroupActorRoleAdministrator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := makeA5Blacklist(t)
			ctx := context.Background()
			if err := p.addBlacklistRule(ctx, 500, "scam"); err != nil {
				t.Fatal(err)
			}
			if err := p.addBlacklistRule(ctx, 600, "scam"); err != nil {
				t.Fatal(err)
			}
			rules, err := p.db.ListBlacklists(ctx, 500)
			if err != nil {
				t.Fatal(err)
			}
			session, target, scope := a5BlacklistMutationBinding(500)
			probe := &a5BlacklistLockProbe{lock: p.ruleLock(500), role: tc.role, deleteMessages: tc.deleteMessages}
			err = p.removeNativeBlacklistIfCurrent(ctx, session, target, scope, "scam", blacklistSnapshot(rules), probe)
			if tc.allowed && err != nil {
				t.Fatalf("authorized deletion failed: %v", err)
			}
			if !tc.allowed && !errors.Is(err, core.ErrGroupAuthorizationDenied) {
				t.Fatalf("revoked role or rights permitted deletion: %v", err)
			}
			if probe.freshCalls != 1 || !probe.checkedWithLock {
				t.Fatalf("fresh role not checked under write lock: calls=%d held=%v", probe.freshCalls, probe.checkedWithLock)
			}
			after, err := p.db.ListBlacklists(ctx, 500)
			if err != nil {
				t.Fatal(err)
			}
			expected := 1
			if tc.allowed {
				expected = 0
			}
			if len(after) != expected {
				t.Fatalf("unexpected group mutation: got %v; want %d rules", after, expected)
			}
			other, err := p.db.ListBlacklists(ctx, 600)
			if err != nil || len(other) != 1 {
				t.Fatalf("other group changed: %v (%v)", other, err)
			}
		})
	}
}

func TestA5FinalBlacklistStaleSnapshotAndMissingRoleFailClosed(t *testing.T) {
	p := makeA5Blacklist(t)
	ctx := context.Background()
	if err := p.addBlacklistRule(ctx, 500, "scam"); err != nil {
		t.Fatal(err)
	}
	rules, err := p.db.ListBlacklists(ctx, 500)
	if err != nil {
		t.Fatal(err)
	}
	digest := blacklistSnapshot(rules)
	session, target, scope := a5BlacklistMutationBinding(500)
	if err := p.removeNativeBlacklistIfCurrent(ctx, session, target, scope, "scam", digest, nil); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("missing live verifier allowed deletion: %v", err)
	}
	if err := p.addBlacklistRule(ctx, 500, "another"); err != nil {
		t.Fatal(err)
	}
	probe := &a5BlacklistLockProbe{lock: p.ruleLock(500), role: core.GroupActorRoleAdministrator, deleteMessages: true}
	if err := p.removeNativeBlacklistIfCurrent(ctx, session, target, scope, "scam", digest, probe); !errors.Is(err, core.ErrConflict) {
		t.Fatalf("stale list allowed deletion: %v", err)
	}
	if probe.freshCalls != 0 {
		t.Fatalf("stale snapshot should reject before role RPC, calls=%d", probe.freshCalls)
	}
	remaining, err := p.db.ListBlacklists(ctx, 500)
	if err != nil || len(remaining) != 2 {
		t.Fatalf("rejected callback damaged group DB: %v (%v)", remaining, err)
	}
}
