package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP7KGroupPlaneResourceAndLifecycleFences(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "assistant", "groupauth", "resolver.go"): {
			"defaultCacheCapacity    = 4096",
			"defaultMaxVerifications = 32",
			"verificationSlots chan struct{}",
			"ErrGroupRoleSaturated",
		},
		filepath.Join(root, "internal", "assistant", "peer", "cache.go"): {
			"maxMemoryCacheEntries    = 4096",
			"memoryCacheTTL           = 30 * time.Minute",
			"Evict one arbitrary entry in O(1) expected time",
		},
		filepath.Join(root, "internal", "services", "groupstate", "store.go"): {
			"DefaultMaxEntries   = 10_000",
			"HardMaxEntries      = 50_000",
			"It owns no goroutine/cache/ticker",
		},
		filepath.Join(root, "internal", "assistant", "command", "router.go"): {
			"quotaOwner = tasks.OwnerID(fmt.Sprintf(\"telegram:chat:%d\"",
			"orderingKey = core.GroupOrderingKey",
		},
		filepath.Join(root, "internal", "assistant", "client", "updates.go"): {
			"QuotaOwner:       tasks.OwnerID(fmt.Sprintf(\"telegram:chat:%d\", chatID))",
			"OrderingKey:      core.GroupOrderingKey(chatID, assistantTopicID(message))",
			"deps.IsShuttingDown != nil && deps.IsShuttingDown()",
		},
		filepath.Join(root, "plugins", "filters", "filters.go"): {
			"QuotaOwner:       tasks.OwnerID(fmt.Sprintf(\"telegram:chat:%d\", chatID))",
			"OrderingKey:      core.GroupOrderingKey(chatID, topicID)",
		},
		filepath.Join(root, "internal", "core", "events.go"): {
			"type QuotaOwnedEvent interface",
			"func (e *GroupServiceEvent) QuotaOwner() tasks.OwnerID",
			"owner = eventOwner",
			"defaultEventWorkerIdle = 30 * time.Second",
		},
		filepath.Join(root, "internal", "assistant", "groupevents", "service.go"): {
			"Disabled configuration remains durable but is intentionally not",
			"func (s *Service) StateContext(",
			"s.chats = make(map[int64]chatConfig)",
			"s.welcomeInterest.ReplaceLoaded(nil)",
		},
		filepath.Join(root, "internal", "assistant", "client", "client.go"): {
			"groupEvents.SetTransport(nil)",
			"groupRules.SetTransport(nil)",
			"groupRules.SetRoleResolver(nil)",
			"c.shuttingDown.Store(true)",
		},
		filepath.Join(root, "internal", "app", "shutdown.go"): {
			"One App-owned deadline governs every teardown phase",
			"runtime.StopWithin(shutdownCtx)",
			"transportCancel()",
		},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, invariant := range required {
			if !strings.Contains(source, invariant) {
				t.Errorf("P7-K invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP7KGroupPlaneDoesNotAddPerChatWorkersOrPolling(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "assistant", "groupauth", "resolver.go"),
		filepath.Join("internal", "assistant", "groupevents", "service.go"),
		filepath.Join("internal", "assistant", "grouprules", "service.go"),
		filepath.Join("internal", "core", "group_context.go"),
		filepath.Join("internal", "core", "group_state.go"),
		filepath.Join("internal", "core", "feature_state.go"),
		filepath.Join("internal", "services", "groupstate", "store.go"),
	} {
		raw, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{"time.NewTicker(", "time.Tick(", "time.AfterFunc(", "go func("} {
			if strings.Contains(source, forbidden) {
				t.Errorf("P7-K group-plane state must remain occurrence-driven; %s contains %q", rel, forbidden)
			}
		}
	}
}
