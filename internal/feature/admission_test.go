package feature

import (
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

func TestAdmitInteractionSeparatesInvocationPermissionAndChatScope(t *testing.T) {
	policy := OwnerPolicy(execution.SurfaceAssistant)
	policy.PrivateOnly = true
	interaction := Interaction{ID: "home", Kind: InteractionScreen, Surfaces: execution.SurfaceAssistant, Policy: policy}
	perms := core.NewPermissions(7, []int64{8})

	if err := AdmitInteraction(interaction, execution.SourceAssistant, 7, true, perms); err != nil {
		t.Fatalf("owner admission error = %v", err)
	}
	if err := AdmitInteraction(interaction, execution.SourceAssistant, 8, true, perms); !errors.Is(err, ErrInteractionDenied) {
		t.Fatalf("sudo admission error = %v, want %v", err, ErrInteractionDenied)
	}
	if err := AdmitInteraction(interaction, execution.SourceAssistant, 7, false, perms); !errors.Is(err, ErrInteractionDenied) {
		t.Fatalf("group admission error = %v, want %v", err, ErrInteractionDenied)
	}
	if err := AdmitInteraction(interaction, execution.SourceInline, 7, true, perms); !errors.Is(err, ErrInteractionUnavailable) {
		t.Fatalf("wrong surface error = %v, want %v", err, ErrInteractionUnavailable)
	}
}

func TestAdmitInteractionPublicPolicyAllowsVisitor(t *testing.T) {
	policy := PublicPolicy(execution.SurfaceAssistant)
	policy.PrivateOnly = true
	interaction := Interaction{ID: "start", Kind: InteractionDeepLink, Surfaces: execution.SurfaceAssistant, Policy: policy}

	if err := AdmitInteraction(interaction, execution.SourceAssistant, 99, true, core.NewPermissions(7, nil)); err != nil {
		t.Fatalf("public admission error = %v", err)
	}
}
