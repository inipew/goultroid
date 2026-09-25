package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP8BSelfInlineUsesSingleManagedTransport(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "presentation", "selfinline", "render.go"): {
			"type Renderer interface",
			"QueryInlineBot(",
			"SendInlineBotResult(",
			"crypto/rand",
		},
		filepath.Join(root, "internal", "presentation", "selfinline", "current.go"): {
			"type TransportProvider func() Transport",
			"CurrentTransport(provider TransportProvider) Transport",
			"transport := t.provider()",
		},
		filepath.Join(root, "internal", "telegram", "selfinline.go"): {
			"s.execReadOnlyPeerVal(",
			"s.execNonIdempotentPeerVal(",
			"messages.getInlineBotResults",
			"messages.sendInlineBotResult",
		},
		filepath.Join(root, "internal", "app", "selfinline.go"): {
			"SelfInlineRenderer() selfinline.Renderer",
			"selfinline.CurrentTransport(",
			"newSelfInlineRenderer(a.client, a.assistant)",
		},
		filepath.Join(root, "internal", "app", "selfinline_features.go"): {
			"base := newSelfInlineRenderer(client, assistantClient)",
		},
		filepath.Join(root, "internal", "services", "inline", "engine.go"): {
			"Binding:   rootinteraction.Binding{ActorID: userID}",
		},
		filepath.Join(root, "internal", "interaction", "runtime_callback_test.go"): {
			"TestResolveCallbackClaimsFirstConcreteInlineTarget",
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
				t.Errorf("P8-B invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP8BSelfInlineBridgeOwnsNoWorkersOrGlobalResultCache(t *testing.T) {
	root := repositoryRoot(t)
	for _, rel := range []string{
		filepath.Join("internal", "presentation", "selfinline", "render.go"),
		filepath.Join("internal", "presentation", "selfinline", "current.go"),
	} {
		path := filepath.Join(root, rel)
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		for _, forbidden := range []string{
			"go func(",
			"time.NewTicker(",
			"time.Tick(",
			"time.AfterFunc(",
			"map[string]",
			"sync.Map",
		} {
			if strings.Contains(source, forbidden) {
				t.Errorf("P8-B bridge introduced forbidden retained/runtime state %q in %s", forbidden, rel)
			}
		}
	}
}
