package presentation

import (
	"context"
	"sync"
	"testing"

	"github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/tasks"
)

type revisionAtomicCatalog struct {
	scope tasks.ScopeIdentity

	mu          sync.Mutex
	actionCalls int
	firstAction chan struct{}
	release     chan struct{}
}

func (c *revisionAtomicCatalog) FeatureScope(featureID string) (tasks.ScopeIdentity, bool) {
	if featureID != "atomic" {
		return tasks.ScopeIdentity{}, false
	}
	return c.scope, true
}

func (c *revisionAtomicCatalog) HasAction(featureID, actionID string) bool {
	if featureID != "atomic" || (actionID != "first" && actionID != "second") {
		return false
	}
	c.mu.Lock()
	c.actionCalls++
	first := c.actionCalls == 1 && c.firstAction != nil && c.release != nil
	firstAction := c.firstAction
	release := c.release
	c.mu.Unlock()
	if first {
		close(firstAction)
		<-release
	}
	return true
}

func TestCompilerUsesOneRevisionForAllActionButtons(t *testing.T) {
	catalog := &revisionAtomicCatalog{scope: tasks.ScopeIdentity{Owner: "plugin:atomic", Generation: 1}}
	runtime, err := interaction.NewRuntime(catalog, interaction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error=%v", err)
	}
	defer runtime.Close()

	created, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: "atomic",
		Binding:   interaction.Binding{ActorID: 7},
	})
	if err != nil {
		t.Fatalf("Create() error=%v", err)
	}
	compiled, err := NewCompiler(runtime).CompileRows(context.Background(), created.Session.ID, []Row{{
		{Text: "First", ActionID: "first"},
		{Text: "Second", ActionID: "second"},
	}})
	if err != nil {
		t.Fatalf("CompileRows() error=%v", err)
	}
	if len(compiled) != 1 || len(compiled[0]) != 2 {
		t.Fatalf("compiled rows=%+v", compiled)
	}
	for i, button := range compiled[0] {
		token, err := interaction.ParseCallbackToken(button.Data)
		if err != nil {
			t.Fatalf("ParseCallbackToken(%d) error=%v", i, err)
		}
		if token.Revision != created.Session.Revision {
			t.Fatalf("button[%d] revision=%d, want %d", i, token.Revision, created.Session.Revision)
		}
	}
}

func TestCompilerConcurrentRevisionAdvanceCannotMixKeyboard(t *testing.T) {
	catalog := &revisionAtomicCatalog{
		scope:       tasks.ScopeIdentity{Owner: "plugin:atomic", Generation: 1},
		firstAction: make(chan struct{}),
		release:     make(chan struct{}),
	}
	runtime, err := interaction.NewRuntime(catalog, interaction.Config{})
	if err != nil {
		t.Fatalf("NewRuntime() error=%v", err)
	}
	defer runtime.Close()

	created, err := runtime.Create(context.Background(), interaction.CreateRequest{
		FeatureID: "atomic",
		Binding:   interaction.Binding{ActorID: 7},
		State:     []byte("one"),
	})
	if err != nil {
		t.Fatalf("Create() error=%v", err)
	}

	type compileResult struct {
		rows []CompiledRow
		err  error
	}
	resultCh := make(chan compileResult, 1)
	go func() {
		rows, compileErr := NewCompiler(runtime).CompileRows(context.Background(), created.Session.ID, []Row{{
			{Text: "First", ActionID: "first"},
			{Text: "Docs", Type: ButtonURL, URL: "https://example.com"},
			{Text: "Second", ActionID: "second"},
			{Text: "Search", Type: ButtonSwitchInline, InlineQuery: "query", SamePeer: true},
		}})
		resultCh <- compileResult{rows: rows, err: compileErr}
	}()

	<-catalog.firstAction
	updated, err := runtime.UpdateState(context.Background(), created.Session.ID, interaction.UpdateRequest{
		ExpectedRevision: created.Session.Revision,
		State:            []byte("two"),
	})
	if err != nil {
		t.Fatalf("UpdateState() error=%v", err)
	}
	if updated.Revision == created.Session.Revision {
		t.Fatal("UpdateState() did not advance revision")
	}
	close(catalog.release)

	result := <-resultCh
	if result.err != nil {
		t.Fatalf("CompileRows() error=%v", result.err)
	}
	if len(result.rows) != 1 || len(result.rows[0]) != 4 {
		t.Fatalf("compiled rows=%+v", result.rows)
	}

	for _, index := range []int{0, 2} {
		token, err := interaction.ParseCallbackToken(result.rows[0][index].Data)
		if err != nil {
			t.Fatalf("ParseCallbackToken(%d) error=%v", index, err)
		}
		if token.Revision != created.Session.Revision {
			t.Fatalf("action button[%d] revision=%d, want snapshot revision %d", index, token.Revision, created.Session.Revision)
		}
	}
	if result.rows[0][1].URL != "https://example.com" || len(result.rows[0][1].Data) != 0 {
		t.Fatalf("URL button changed: %+v", result.rows[0][1])
	}
	if result.rows[0][3].InlineQuery != "query" || !result.rows[0][3].SamePeer || len(result.rows[0][3].Data) != 0 {
		t.Fatalf("switch-inline button changed: %+v", result.rows[0][3])
	}
}
