package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR4AFKTransitionAndWelcomeEffectBoundaries(t *testing.T) {
	root := repositoryRoot(t)

	afkPath := filepath.Join(root, "plugins", "afk", "afk.go")
	raw, err := os.ReadFile(afkPath)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)

	if !strings.Contains(source, "Ordering: core.MessageHookOrderingPlugin") {
		t.Fatal("AFK outgoing transition must use plugin-global TaskEngine ordering")
	}
	if !strings.Contains(source, "func (p *Plugin) InitPlugin(pctx plugin.PluginContext) error") ||
		!strings.Contains(source, "pctx.TaskClient()") {
		t.Fatal("AFK must receive the shared scoped TaskEngine client through PluginContext")
	}

	handleStart := strings.Index(source, "func (p *Plugin) HandleMessageEvent")
	if handleStart < 0 {
		t.Fatal("AFK HandleMessageEvent missing")
	}
	handleEnd := strings.Index(source[handleStart:], "func (p *Plugin) submitWelcomeEffect")
	if handleEnd < 0 {
		t.Fatal("AFK welcome-effect submission boundary missing")
	}
	handleBody := source[handleStart : handleStart+handleEnd]
	outgoingStart := strings.Index(handleBody, "if message.Outgoing {")
	outgoingEnd := strings.Index(handleBody, "\n\tif p.svcFunc == nil {")
	if outgoingStart < 0 || outgoingEnd < 0 || outgoingEnd <= outgoingStart {
		t.Fatal("AFK outgoing transition branch boundaries missing")
	}
	outgoingBody := handleBody[outgoingStart:outgoingEnd]
	if !strings.Contains(outgoingBody, "p.disableAFK(ctx)") {
		t.Fatal("AFK outgoing barrier no longer owns the state transition")
	}
	if !strings.Contains(outgoingBody, "p.submitWelcomeEffect(ctx, message, dur)") {
		t.Fatal("AFK outgoing barrier must hand presentation to the effect path")
	}
	for _, forbidden := range []string{
		"p.sendTemplate(",
		"p.sendWelcomeEffect(",
		"svc.SendMessage(",
		"p.resolveEnvelopePeer(ctx, message)",
	} {
		if strings.Contains(outgoingBody, forbidden) {
			t.Errorf("AFK transition barrier contains presentation/RPC work %q", forbidden)
		}
	}

	effectStart := strings.Index(source, "func (p *Plugin) submitWelcomeEffect")
	if effectStart < 0 {
		t.Fatal("submitWelcomeEffect missing")
	}
	effectEnd := strings.Index(source[effectStart:], "func (p *Plugin) sendWelcomeEffect")
	if effectEnd < 0 {
		t.Fatal("sendWelcomeEffect missing")
	}
	effectBody := source[effectStart : effectStart+effectEnd]
	for _, required := range []string{
		"client.Submit(",
		"tasks.PoolID(\"general\")",
		"tasks.PriorityNormal",
		"afkWelcomeEffectTimeout",
		"Scope:            p.taskScope()",
		"Input: fmt.Sprintf(",
	} {
		if !strings.Contains(effectBody, required) {
			t.Errorf("AFK welcome effect submission missing %q", required)
		}
	}
	if strings.Contains(effectBody, "Input:            effect") {
		t.Fatal("AFK welcome effect must not submit unsupported struct payloads to TaskEngine")
	}
	if strings.Contains(effectBody, "scope.Go(") {
		t.Fatal("AFK welcome effect must use shared TaskEngine, not Scope.Go")
	}

	modulePath := filepath.Join(root, "plugins", "afk", "module.go")
	moduleRaw, err := os.ReadFile(modulePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(moduleRaw), "plugin.CapTasks") {
		t.Fatal("AFK manifest must declare tasks capability for scoped TaskEngine access")
	}
}
