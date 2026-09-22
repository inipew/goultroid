package architecture

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestP7IGroupRuleCoordinatorOwnsNoSecondExecutionEngine(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "grouprules", "service.go")
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil { t.Fatalf("parse %s: %v", path, err) }
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil { t.Fatal(err) }
		if importPath == modulePath+"/internal/tasks" || importPath == modulePath+"/internal/taskengine" {
			t.Fatalf("P7-I coordinator owns execution engine import %q", importPath)
		}
	}
	ast.Inspect(file, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.GoStmt:
			t.Errorf("P7-I coordinator starts goroutine at %s", fset.Position(n.Pos()))
		case *ast.CallExpr:
			sel, ok := n.Fun.(*ast.SelectorExpr)
			if !ok { return true }
			pkg, ok := sel.X.(*ast.Ident)
			if !ok || pkg.Name != "time" { return true }
			switch sel.Sel.Name {
			case "NewTicker", "Tick", "AfterFunc", "NewTimer":
				t.Errorf("P7-I coordinator installs timer %s at %s", sel.Sel.Name, fset.Position(n.Pos()))
			}
		}
		return true
	})
}

func TestP7IAssistantColdPathGatesBeforeCacheResolveAndTask(t *testing.T) {
	root := repositoryRoot(t)
	path := filepath.Join(root, "internal", "assistant", "client", "updates.go")
	raw, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	source := string(raw)
	gate := strings.Index(source, "deps.GroupRules.Interested(chatID)")
	cache := strings.Index(source, "deps.CacheEntities(e)")
	submit := strings.Index(source, "submitAssistantGroupRules(ctx, msg, e, senderID, deps, logger)")
	if gate < 0 || cache < 0 || submit < 0 {
		t.Fatalf("P7-I ingress markers missing gate=%d cache=%d submit=%d", gate, cache, submit)
	}
	if gate > cache { t.Fatal("P7-I interest gate occurs after entity cache work") }
	if cache > submit { } else { t.Fatal("P7-I rule task ordering changed unexpectedly") }
	taskStart := strings.Index(source, "func submitAssistantGroupRules(")
	taskEndRel := strings.Index(source[taskStart+1:], "\n// InlineQueryExecutor")
	if taskStart < 0 || taskEndRel < 0 { t.Fatal("P7-I task admission helper is missing") }
	taskBlock := source[taskStart : taskStart+1+taskEndRel]
	for _, required := range []string{
		"deps.Tasks.Submit",
		"OrderingKey:",
		"fmt.Sprintf(\"chat:%d\", chatID)",
		"deps.Resolver.Resolve(",
		"deps.GroupRuleChats.Classify(",
		"deps.GroupRules.Handle(taskCtx, envelope)",
	} {
		if !strings.Contains(taskBlock, required) { t.Errorf("P7-I admitted rule path missing %q", required) }
	}
}

func TestP7IRulesAreIndexedAndRevisionScopedPerChat(t *testing.T) {
	root := repositoryRoot(t)
	blacklistPath := filepath.Join(root, "plugins", "blacklist", "blacklist.go")
	blacklistRaw, err := os.ReadFile(blacklistPath)
	if err != nil { t.Fatal(err) }
	blacklist := string(blacklistRaw)
	for _, required := range []string{"chatRevision  map[int64]uint64", "newBlacklistMatcher(items)", "compiled.matcher.matches(message.Text)", "revisionSeq", "cacheClock"} {
		if !strings.Contains(blacklist, required) { t.Errorf("P7-I blacklist invariant missing %q", required) }
	}
	for _, forbidden := range []string{"ruleRevision atomic.Uint64", "chatAccess", "regexp.Compile"} {
		if strings.Contains(blacklist, forbidden) { t.Errorf("P7-I blacklist still uses global/linear mechanism %q", forbidden) }
	}
	matcherRaw, err := os.ReadFile(filepath.Join(root, "plugins", "blacklist", "matcher.go"))
	if err != nil { t.Fatal(err) }
	matcher := string(matcherRaw)
	for _, required := range []string{"fail    int", "outputs []int", "buildFailures()", "blacklistBoundaryOK"} {
		if !strings.Contains(matcher, required) { t.Errorf("P7-I indexed blacklist matcher missing %q", required) }
	}
	filtersRaw, err := os.ReadFile(filepath.Join(root, "plugins", "filters", "filters.go"))
	if err != nil { t.Fatal(err) }
	filters := string(filtersRaw)
	for _, required := range []string{"chatRevision map[int64]uint64", "newKeywordMatcher(filters)", "filterSet.matcher.firstMatch(message.Text)", "revisionSeq", "cacheClock"} {
		if !strings.Contains(filters, required) { t.Errorf("P7-I filter invariant missing %q", required) }
	}
	for _, forbidden := range []string{"ruleRevision atomic.Uint64", "chatAccess"} {
		if strings.Contains(filters, forbidden) { t.Errorf("P7-I filters still use global revision/access mechanism %q", forbidden) }
	}
}

func TestP7IRuleAndWarningBoundsRemainExplicit(t *testing.T) {
	root := repositoryRoot(t)
	checks := map[string][]string{
		filepath.Join(root, "plugins", "blacklist", "blacklist.go"): {"MaxRulesPerChat", "= 512", "MaxActiveChats", "= 50_000", "maxCompiledCacheChats = 500"},
		filepath.Join(root, "plugins", "filters", "filters.go"): {"MaxRulesPerChat", "= 512", "MaxActiveChats", "= 50_000", "maxCompiledFilterCacheChats", "= 500", "maxFilterCooldownEntries", "= 1_000"},
		filepath.Join(root, "internal", "services", "moderation", "service.go"): {"MaxWarningThreshold   = 16", "MaxWarningReasonBytes = 1024", "MaxWarningRows        = 50_000", "warningLockStripes    = 64"},
	}
	for path, required := range checks {
		raw, err := os.ReadFile(path)
		if err != nil { t.Fatal(err) }
		source := string(raw)
		for _, marker := range required {
			if !strings.Contains(source, marker) { rel, _ := filepath.Rel(root, path); t.Errorf("P7-I resource bound %q missing from %s", marker, rel) }
		}
	}
}

func TestP7IBypassOrderingAvoidsWorkForOwnerSudo(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "grouprules", "service.go"))
	if err != nil { t.Fatal(err) }
	source := string(raw)
	start := strings.Index(source, "func (s *Service) Evaluate(")
	endRel := strings.Index(source[start+1:], "\ntype interactionServicer")
	if start < 0 || endRel < 0 { t.Fatal("P7-I Evaluate block is missing") }
	block := source[start : start+1+endRel]
	privileged := strings.Index(block, "privileged(message.Sender.ID)")
	matched := strings.Index(block, "s.matched(ctx, message, enabled)")
	bypassed := strings.Index(block, "s.bypassed(ctx, message, roles, privileged)")
	if privileged < 0 || matched < 0 || bypassed < 0 { t.Fatalf("P7-I bypass markers missing privileged=%d match=%d bypass=%d", privileged, matched, bypassed) }
	if privileged > matched { t.Fatal("P7-I Owner/Sudo bypass occurs after rule matching") }
	if matched >= bypassed { t.Fatal("P7-I contextual admin bypass occurs before rule match") }
}


func TestP7IManagedChannelClassificationStaysInsideAdmittedPath(t *testing.T) {
	root := repositoryRoot(t)
	updatesRaw, err := os.ReadFile(filepath.Join(root, "internal", "assistant", "client", "updates.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(updatesRaw)
	taskStart := strings.Index(source, "func submitAssistantGroupRules(")
	taskEndRel := strings.Index(source[taskStart+1:], "\n// InlineQueryExecutor")
	if taskStart < 0 || taskEndRel < 0 {
		t.Fatal("P7-I admitted rule task helper is missing")
	}
	taskBlock := source[taskStart : taskStart+1+taskEndRel]
	if !strings.Contains(taskBlock, "deps.GroupRuleChats.Classify(") {
		t.Fatal("P7-I managed chat classification is not inside admitted task work")
	}

	classifierRaw, err := os.ReadFile(filepath.Join(
		root, "internal", "assistant", "client", "group_rule_chat.go",
	))
	if err != nil {
		t.Fatal(err)
	}
	classifier := string(classifierRaw)
	for _, required := range []string{
		"ChannelsGetChannels(",
		"channel.Megagroup",
		"core.ErrGroupOnly",
	} {
		if !strings.Contains(classifier, required) {
			t.Errorf("P7-I managed channel classifier missing %q", required)
		}
	}
}

func TestP7IWarningResetSharesSameTargetStripe(t *testing.T) {
	root := repositoryRoot(t)
	raw, err := os.ReadFile(filepath.Join(
		root, "internal", "services", "moderation", "service.go",
	))
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	start := strings.Index(source, "func (s *Service) ResetWarnings(")
	endRel := strings.Index(source[start+1:], "\nfunc (s *Service) Mute(")
	if start < 0 || endRel < 0 {
		t.Fatal("P7-I ResetWarnings block is missing")
	}
	block := source[start : start+1+endRel]
	for _, required := range []string{
		"s.warningLock(chatID, userID)",
		"lock.Lock()",
		"defer lock.Unlock()",
	} {
		if !strings.Contains(block, required) {
			t.Errorf("P7-I reset stripe invariant missing %q", required)
		}
	}
}
