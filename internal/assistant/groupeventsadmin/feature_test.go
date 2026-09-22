package groupeventsadmin

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
)

func TestP7HCommandsAreCanonicalAssistantGroupAdminControls(t *testing.T) {
	f := New(nil)
	commands := f.Commands()
	if len(commands) != 2 {
		t.Fatalf("commands=%d, want 2", len(commands))
	}

	seen := map[string]bool{}
	for _, command := range commands {
		seen[command.Name] = true
		if command.Surfaces != execution.SurfaceAssistant {
			t.Fatalf("%s surfaces=%v, want Assistant only", command.Name, command.Surfaces)
		}
		if command.Permission != core.PermissionEveryone ||
			command.EffectivePermission(core.ExecutionAssistant) != core.PermissionEveryone {
			t.Fatalf("%s permission=%s assistant=%s",
				command.Name,
				command.Permission,
				command.EffectivePermission(core.ExecutionAssistant),
			)
		}
		if command.Invocation.Assistant != core.InvocationAnyone {
			t.Fatalf("%s invocation=%s, want anyone", command.Name, command.Invocation.Assistant)
		}
		if !command.GroupOnly || command.PrivateOnly {
			t.Fatalf("%s group/private flags=%v/%v", command.Name, command.GroupOnly, command.PrivateOnly)
		}
		if command.GroupAuthorization.Level != core.GroupAuthorizationAdministrator ||
			command.GroupAuthorization.Rights.Any() {
			t.Fatalf("%s group auth=%+v, want administrator without specific right",
				command.Name, command.GroupAuthorization)
		}
	}
	if !seen["welcome"] || !seen["goodbye"] {
		t.Fatalf("commands=%v, want welcome+goodbye", seen)
	}
	if _, err := feature.BindCanonicalCommands(f.FeatureSpec(), commands); err != nil {
		t.Fatalf("BindCanonicalCommands() error=%v", err)
	}
}
