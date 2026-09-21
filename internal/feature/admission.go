package feature

import (
	"errors"
	"fmt"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

var (
	ErrInteractionUnavailable = errors.New("feature: interaction unavailable")
	ErrInteractionDenied      = errors.New("feature: interaction admission denied")
)

// AdmitInteraction applies one declared non-command interaction policy to a
// concrete human invocation. Invocation and authorization remain independent:
// the caller must satisfy both dimensions, plus the declared chat scope.
func AdmitInteraction(interaction Interaction, source execution.Source, userID int64, private bool, perms *core.Permissions) error {
	if !interaction.Surfaces.Supports(source) {
		return fmt.Errorf("%w: surface %s", ErrInteractionUnavailable, source)
	}
	if interaction.Policy.PrivateOnly && !private {
		return fmt.Errorf("%w: private chat required", ErrInteractionDenied)
	}
	if interaction.Policy.GroupOnly && private {
		return fmt.Errorf("%w: group chat required", ErrInteractionDenied)
	}

	switch interaction.Policy.Invocation.For(source) {
	case core.InvocationAnyone:
		// Continue to authorization.
	case core.InvocationSelfOnly:
		if perms == nil || !perms.IsOwner(userID) {
			return fmt.Errorf("%w: owner invocation required", ErrInteractionDenied)
		}
	case core.InvocationSelfOrSudo:
		if perms == nil || !perms.IsSudo(userID) {
			return fmt.Errorf("%w: sudo invocation required", ErrInteractionDenied)
		}
	default:
		return fmt.Errorf("%w: invocation policy is not explicit", ErrInteractionDenied)
	}

	level := core.PermissionEveryone
	if perms != nil {
		level = perms.Level(userID)
	}
	if level < interaction.Policy.Permission {
		return fmt.Errorf("%w: %s permission required", ErrInteractionDenied, interaction.Policy.Permission)
	}
	return nil
}
