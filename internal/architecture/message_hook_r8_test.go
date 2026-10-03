package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestR8PluginManagerHasSingleExplicitMessageHookContract(t *testing.T) {
	root := repositoryRoot(t)

	pluginRaw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "plugin.go"))
	if err != nil {
		t.Fatal(err)
	}
	pluginSource := string(pluginRaw)
	for _, retired := range []string{
		"type MessageEventPlugin interface",
		"type MessageEventRoutingPlugin interface",
		"type MessageEventStatePlugin interface",
		"type MessageHookPlugin interface",
		"type MessageHookRoutingPlugin interface",
		"type MessageHookStatePlugin interface",
	} {
		if strings.Contains(pluginSource, retired) {
			t.Fatalf("retired plugin message-hook adapter remains: %s", retired)
		}
	}
	for _, required := range []string{
		"type MessageEventRegistrationsPlugin interface",
		"MessageHookPriority() int",
		"MessageHookRegistrations() []core.MessageHookRegistration",
	} {
		if !strings.Contains(pluginSource, required) {
			t.Fatalf("final message-hook contract missing %q", required)
		}
	}

	managerRaw, err := os.ReadFile(filepath.Join(root, "internal", "plugin", "manager.go"))
	if err != nil {
		t.Fatal(err)
	}
	manager := string(managerRaw)
	for _, retired := range []string{
		"MessageEventPlugin",
		"MessageEventRoutingPlugin",
		"MessageEventStatePlugin",
		"MessageHookPlugin",
		"MessageHookRoutingPlugin",
		"MessageHookStatePlugin",
		"canonicalMessageHookStateGate",
	} {
		if strings.Contains(manager, retired) {
			t.Fatalf("plugin manager still depends on retired hook adapter %q", retired)
		}
	}
	start := strings.Index(manager, "func registerMessageHook(")
	end := strings.Index(manager[start:], "// SchedulerTaskCleaner")
	if start < 0 || end < 0 {
		t.Fatal("registerMessageHook boundary missing")
	}
	body := manager[start : start+end]
	if strings.Count(body, "registrar.RegisterMessageHook(") != 1 {
		t.Fatal("plugin manager must contain one explicit registration path")
	}
	if !strings.Contains(body, "p.(MessageEventRegistrationsPlugin)") {
		t.Fatal("plugin manager does not require explicit message-hook registrations")
	}
}

func TestR8StatefulProductionHooksDeclareStateGateInRegistration(t *testing.T) {
	root := repositoryRoot(t)
	for _, path := range []string{
		"plugins/blacklist/message_hook_r6.go",
		"plugins/filters/message_hook_r6.go",
		"plugins/pmpermit/message_hook_r6.go",
	} {
		raw, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if !strings.Contains(source, "StateGate:") {
			t.Fatalf("%s does not declare state gate explicitly in MessageHookRegistration", path)
		}
	}
}

func TestR8ProductionDecisionHookAllowlist(t *testing.T) {
	root := repositoryRoot(t)
	allowed := map[string]struct{}{
		"plugins/afk/afk.go":                         {},
		"plugins/blacklist/message_hook_r6.go":       {},
		"plugins/filters/message_hook_r6.go":         {},
		"plugins/pmpermit/message_hook_r6.go":        {},
	}

	matches, err := filepath.Glob(filepath.Join(root, "plugins", "*", "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range matches {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		source := string(raw)
		if !strings.Contains(source, "MessageHookRegistrations()") ||
			!strings.Contains(source, "MessageHookDecision") {
			continue
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			t.Fatal(err)
		}
		rel = filepath.ToSlash(rel)
		if _, ok := allowed[rel]; !ok {
			t.Fatalf("new production decision-hook registration requires explicit R8 review: %s", rel)
		}
		if !strings.Contains(source, "Execution: core.MessageHookExecutionPolicy") ||
			!strings.Contains(source, "FailurePolicy:") {
			t.Fatalf("decision-hook registration in %s does not declare explicit execution/failure policy", rel)
		}
	}
}
