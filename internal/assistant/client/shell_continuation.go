package client

import (
	"fmt"

	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
)

func (c *AssistantClient) admitShellContinuation(catalog feature.Catalog, kind feature.InteractionKind, interactionID string, ctx *orchestration.Context) error {
	if ctx == nil || catalog == nil {
		return ErrShellUnavailable
	}
	interaction, ok := catalog.FindInteraction(assistantshell.FeatureID, kind, interactionID)
	if !ok {
		return ErrShellUnavailable
	}
	userID := ctx.Session().Binding.ActorID
	if isInlineInteractionTarget(ctx) {
		if err := feature.AdmitInteractionIdentity(interaction, execution.SourceInline, userID, c.shellPermissions()); err != nil {
			return fmt.Errorf("%w: %w", ErrShellAdmission, err)
		}
		return nil
	}
	private := false
	if target, ok := ctx.Target().(presentationtelegram.MessageTarget); ok {
		private = isPrivatePeer(target.Peer)
	}
	if err := feature.AdmitInteraction(interaction, execution.SourceAssistant, userID, private, c.shellPermissions()); err != nil {
		return fmt.Errorf("%w: %w", ErrShellAdmission, err)
	}
	return nil
}

func isInlineInteractionTarget(ctx *orchestration.Context) bool {
	if ctx == nil {
		return false
	}
	_, ok := ctx.Target().(presentationtelegram.InlineTarget)
	return ok
}
