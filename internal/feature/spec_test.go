package feature

import (
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
)

func TestBindCanonicalCommandsProjectsCoreMetadata(t *testing.T) {
	spec, err := BindCanonicalCommands(Spec{
		ID:   "demo",
		Name: "Demo",
		Interactions: []Interaction{
			{
				ID:       "search",
				Kind:     InteractionInline,
				Surfaces: execution.SurfaceInline,
				Policy:   OwnerPolicy(execution.SurfaceInline),
			},
		},
	}, []core.Command{
		{
			Name:        "Ping",
			Aliases:     []string{"P"},
			Description: "check status",
			Usage:       ".ping",
			Category:    "system",
			Permission:  core.PermissionSudo,
			Invocation: core.InvocationPolicy{
				Userbot:   core.InvocationSelfOnly,
				Assistant: core.InvocationSelfOrSudo,
			},
			Surfaces: execution.SurfaceBotAndUser,
			Handler:  func(*core.Context) error { return nil },
		},
	})
	if err != nil {
		t.Fatalf("BindCanonicalCommands() error = %v", err)
	}
	if len(spec.Commands) != 1 {
		t.Fatalf("commands = %d, want 1", len(spec.Commands))
	}
	command := spec.Commands[0]
	if command.Name != "ping" {
		t.Fatalf("command name = %q, want ping", command.Name)
	}
	if len(command.Aliases) != 1 || command.Aliases[0] != "p" {
		t.Fatalf("aliases = %v, want [p]", command.Aliases)
	}
	if command.Policy.Permission != core.PermissionSudo {
		t.Fatalf("permission = %v, want sudo", command.Policy.Permission)
	}
	if command.Policy.Invocation.Userbot != core.InvocationSelfOnly {
		t.Fatalf("userbot invocation = %v, want self-only", command.Policy.Invocation.Userbot)
	}
	if command.Policy.Invocation.Assistant != core.InvocationSelfOrSudo {
		t.Fatalf("assistant invocation = %v, want self-or-sudo", command.Policy.Invocation.Assistant)
	}
}

func TestBindCanonicalCommandsRejectsParallelCommandDeclaration(t *testing.T) {
	_, err := BindCanonicalCommands(Spec{
		ID:   "demo",
		Name: "Demo",
		Commands: []CommandSurface{
			{Name: "ping", Surfaces: execution.SurfaceUserbot},
		},
	}, nil)
	if !errors.Is(err, ErrCommandSurfaceDeclared) {
		t.Fatalf("error = %v, want %v", err, ErrCommandSurfaceDeclared)
	}
}

func TestSpecValidateRequiresExplicitInteractionInvocation(t *testing.T) {
	spec := Spec{
		ID:   "demo",
		Name: "Demo",
		Interactions: []Interaction{
			{
				ID:       "search",
				Kind:     InteractionInline,
				Surfaces: execution.SurfaceInline,
				Policy:   Policy{Permission: core.PermissionOwner},
			},
		},
	}
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want invalid spec", err)
	}
}

func TestSpecValidateRejectsWrongInteractionSurface(t *testing.T) {
	spec := Spec{
		ID:   "demo",
		Name: "Demo",
		Interactions: []Interaction{
			{
				ID:       "start-token",
				Kind:     InteractionDeepLink,
				Surfaces: execution.SurfaceInline,
				Policy:   OwnerPolicy(execution.SurfaceInline),
			},
		},
	}
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error = %v, want invalid spec", err)
	}
}


func TestSpecValidateRejectsActionThatCannotFitCallbackBudget(t *testing.T) {
	surface := execution.SurfaceAssistant
	spec := Spec{
		ID:   "very_long_feature_identity_is_server_side_only",
		Name: "Callback budget",
		Interactions: []Interaction{{
			ID:       strings.Repeat("a", rootinteraction.MaxCallbackActionIDBytes+1),
			Kind:     InteractionAction,
			Surfaces: surface,
			Policy:   OwnerPolicy(surface),
		}},
	}
	if err := spec.Validate(); !errors.Is(err, ErrInvalidSpec) {
		t.Fatalf("Validate() error=%v, want invalid spec", err)
	}
}
