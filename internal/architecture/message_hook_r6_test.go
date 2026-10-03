package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func r6Read(t *testing.T, root, path string) string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func r6FunctionBody(t *testing.T, source, start, next string) string {
	t.Helper()
	begin := strings.Index(source, start)
	if begin < 0 {
		t.Fatalf("missing function %q", start)
	}
	end := strings.Index(source[begin+len(start):], next)
	if end < 0 {
		t.Fatalf("missing boundary %q after %q", next, start)
	}
	return source[begin : begin+len(start)+end]
}

func TestR6BlacklistDecisionAndDeleteEffectBoundary(t *testing.T) {
	root := repositoryRoot(t)
	source := r6Read(t, root, "plugins/blacklist/message_hook_r6.go")
	decision := r6FunctionBody(t, source, "func (p *Plugin) handleBlacklistDecision", "func (p *Plugin) submitDeleteEffect")

	for _, forbidden := range []string{"DeleteMessage(", "time.Sleep(", "go func(", "scope.Go("} {
		if strings.Contains(decision, forbidden) {
			t.Fatalf("blacklist decision contains effect/runtime operation %q", forbidden)
		}
	}
	for _, required := range []string{
		"MessageHookDecision",
		"MessageHookFailClosed",
		"MessageHookOrderingChat",
		"submitDeleteEffect",
		"tasks.PoolID(\"general\")",
		"blacklistDeleteEffectTimeout",
	} {
		if !strings.Contains(source, required) {
			t.Fatalf("blacklist R6 boundary missing %q", required)
		}
	}
	module := r6Read(t, root, "plugins/blacklist/module.go")
	if !strings.Contains(module, "plugin.CapTasks") {
		t.Fatal("blacklist manifest does not declare TaskEngine capability")
	}
}

func TestR6PMPermitDecisionAndTelegramEffectBoundary(t *testing.T) {
	root := repositoryRoot(t)
	pluginSource := r6Read(t, root, "plugins/pmpermit/message_hook_r6.go")
	incoming := r6FunctionBody(
		t,
		pluginSource,
		"func (p *Plugin) handleIncomingPMDecision",
		"func (p *Plugin) handleOutgoingPMDecision",
	)
	outgoing := r6FunctionBody(
		t,
		pluginSource,
		"func (p *Plugin) handleOutgoingPMDecision",
		"func (p *Plugin) submitIncomingEffect",
	)
	for name, body := range map[string]string{"incoming": incoming, "outgoing": outgoing} {
		for _, forbidden := range []string{
			"SendMessage(",
			"DeleteMessage(",
			"BlockUser(",
			"UnblockUser(",
			"ResolveUser(",
			"time.Sleep(",
			"go func(",
		} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("pmpermit %s decision contains effect/runtime operation %q", name, forbidden)
			}
		}
	}
	for _, required := range []string{
		"MessageHookFailClosed",
		"MessageHookFailOpen",
		"MessageHookOrderingChat",
		"tasks.PoolID(\"general\")",
		"pmpermitEffectTimeout",
		"resolveEffectPeer",
	} {
		if !strings.Contains(pluginSource, required) {
			t.Fatalf("pmpermit R6 plugin boundary missing %q", required)
		}
	}

	serviceSource := r6Read(t, root, "internal/services/pmpermit/message_hook_r6.go")
	decide := r6FunctionBody(
		t,
		serviceSource,
		"func (s *Service) DecideIncomingPM",
		"func (s *Service) ApplyIncomingPMEffect",
	)
	prepare := r6FunctionBody(
		t,
		serviceSource,
		"func (s *Service) PrepareAutoApproveOutgoing",
		"func (s *Service) ApplyAutoApproveOutgoingEffect",
	)
	for name, body := range map[string]string{"incoming": decide, "outgoing": prepare} {
		for _, forbidden := range []string{"sendTemplate(", ".BlockUser(", ".UnblockUser(", ".DeleteMessage("} {
			if strings.Contains(body, forbidden) {
				t.Fatalf("pmpermit %s state phase contains Telegram effect %q", name, forbidden)
			}
		}
	}
	if !strings.Contains(serviceSource, "ApplyIncomingPMEffect") ||
		!strings.Contains(serviceSource, "ApplyAutoApproveOutgoingEffect") {
		t.Fatal("pmpermit asynchronous effect API missing")
	}

	module := r6Read(t, root, "plugins/pmpermit/module.go")
	if !strings.Contains(module, "plugin.CapTasks") {
		t.Fatal("pmpermit manifest does not declare TaskEngine capability")
	}
}

func TestR6FiltersKeepMatchBarrierButPreloadStateAndDeferDelivery(t *testing.T) {
	root := repositoryRoot(t)
	registration := r6Read(t, root, "plugins/filters/message_hook_r6.go")
	for _, required := range []string{
		"MessageHookDecision",
		"MessageHookFailOpen",
		"MessageHookOrderingChat",
		"Handler: p.HandleMessageEvent",
	} {
		if !strings.Contains(registration, required) {
			t.Fatalf("filters R6 registration missing %q", required)
		}
	}

	source := r6Read(t, root, "plugins/filters/filters.go")
	initPlugin := r6FunctionBody(t, source, "func (p *Plugin) InitPlugin", "func (p *Plugin) InitContext")
	for _, required := range []string{"pctx.TaskClient()", "return p.InitContext(pctx)"} {
		if !strings.Contains(initPlugin, required) {
			t.Fatalf("filters production initialization missing %q", required)
		}
	}
	handle := r6FunctionBody(t, source, "func (p *Plugin) HandleMessageEvent", "func (p *Plugin) submitDelivery")
	if !strings.Contains(handle, "p.submitDelivery(") {
		t.Fatal("filters decision no longer delegates response delivery")
	}
	if strings.Contains(handle, "p.deliverResponse(") {
		t.Fatal("filters decision performs Telegram delivery directly")
	}
	submit := r6FunctionBody(t, source, "func (p *Plugin) submitDelivery", "func (p *Plugin) deliverResponse")
	if !strings.Contains(submit, "p.submitContinuation(") || !strings.Contains(submit, "tasks.PoolID(\"general\")") {
		t.Fatal("filters production delivery is not delegated to shared TaskEngine continuation")
	}
	module := r6Read(t, root, "plugins/filters/module.go")
	if !strings.Contains(module, "plugin.CapTasks") {
		t.Fatal("filters manifest lost TaskEngine capability")
	}
}
