package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestP1SelfInlineLifecycleAcceptanceCoversCurrentIdentityServiceAndReload(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "internal", "app", "selfinline_p1_lifecycle_test.go"): {
			"TestP1SelfInlineUsesCurrentTelegramServiceAcrossReconnect",
			"provider.service = second",
			"TestP1SelfInlineRejectsStaleAssistantIdentityAcrossRestart",
			"assistantclient.ErrNotReady",
			"TestP1SelfInlinePluginDisableEnableReloadUsesLiveAuthorization",
			"manager.Disable",
			"manager.Enable",
			"secondScope.Generation() == firstGeneration",
		},
		filepath.Join(root, "internal", "assistant", "client", "identity_p1_lifecycle_test.go"): {
			"TestP1AssistantRestartNeverExposesStaleInlineUsername",
			"StateStarting",
			"StateRunning",
			"StateFailed",
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
				t.Fatalf("P1 lifecycle acceptance invariant missing from %s: %q", path, invariant)
			}
		}
	}
}

func TestP1SelfInlineEndToEndAcceptanceUsesProductionCompositionSeams(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "selfinline_p1_e2e_test.go")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	for _, required := range []string{
		"tg.NewClient(tgmock.Invoker",
		"telegramservice.NewServiceWithExecutor",
		"telegramservice.NewResolverWithContextAndExecutor",
		"newAssistantInlineQueryServicer",
		"*tg.MessagesGetInlineBotResultsRequest",
		"*tg.MessagesSetInlineBotResultsRequest",
		"*tg.MessagesSendInlineBotResultRequest",
		"[]string{\"query\", \"answer\", \"send\"}",
		"manager.InteractionRuntime()",
		"ErrStaleHandler",
		"fresh callback after plugin reload",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("P1 true E2E acceptance invariant missing: %q", required)
		}
	}
	if strings.Contains(source, "selfinline.Transport") {
		t.Fatal("P1 true E2E acceptance must not replace production selfinline.Transport")
	}
	if strings.Count(source, "telegramservice.NewRPCExecutor(") != 1 {
		t.Fatalf("P1 E2E should construct exactly one shared userbot RPC executor, count=%d", strings.Count(source, "telegramservice.NewRPCExecutor("))
	}
	for _, forbidden := range []string{
		"interaction.NewRuntime(",
		"taskengine.New",
		"callback.NewStateStore(",
		"go func(",
		"time.NewTicker(",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("P1 acceptance introduced forbidden duplicate/background authority %q", forbidden)
		}
	}
}
