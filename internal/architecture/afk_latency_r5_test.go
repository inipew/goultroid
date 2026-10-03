package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR5AFKWelcomeKeepsSharedRPCAuthority(t *testing.T) {
	root := repositoryRoot(t)

	afkPath := filepath.Join(root, "plugins", "afk", "afk.go")
	raw, err := os.ReadFile(afkPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	start := strings.Index(source, "func (p *Plugin) sendWelcomeEffect")
	end := strings.Index(source[start:], "func (p *Plugin) deleteWelcomeAfter")
	if start < 0 || end < 0 {
		t.Fatal("AFK welcome effect boundary missing")
	}
	body := source[start : start+end]

	for _, forbidden := range []string{
		"NewRPCExecutor(",
		"NewHierarchicalRPCLimiter(",
		"RetryPolicy{",
		"time.Sleep(",
		"go func(",
		"scope.Go(",
	} {
		if strings.Contains(body, forbidden) {
			t.Errorf("AFK welcome effect created a private execution/retry path via %q", forbidden)
		}
	}
	if strings.Count(body, "p.sendTemplate(") != 1 {
		t.Fatalf("AFK welcome effect must delegate exactly once to canonical Telegram delivery, body=%q", body)
	}

	modulePath := filepath.Join(root, "plugins", "afk", "module.go")
	moduleRaw, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	moduleSource := string(moduleRaw)
	if !strings.Contains(moduleSource, "core.MessageServicer") {
		t.Fatal("AFK runtime service no longer delegates through the shared Telegram message service")
	}
	if strings.Contains(moduleSource, "NewRPCExecutor(") ||
		strings.Contains(moduleSource, "NewHierarchicalRPCLimiter(") {
		t.Fatal("AFK module constructed a private RPC executor or limiter")
	}
}
