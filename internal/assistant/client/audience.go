package client

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/services/pmrelay"
	"go.uber.org/zap"
)

func touchAssistantAudience(
	ctx context.Context,
	registry pmrelay.AudienceRegistry,
	userID int64,
	source pmrelay.AudienceSource,
	logger *zap.Logger,
) {
	if registry == nil || userID <= 0 {
		return
	}
	if _, err := registry.TouchAudience(ctx, pmrelay.AudienceTouch{
		UserID: userID,
		Source: source,
		SeenAt: time.Now().UTC(),
	}); err != nil && logger != nil {
		logger.Warn("assistant: audience registry touch failed",
			zap.Int64("user_id", userID),
			zap.Uint8("source", uint8(source)),
			zap.Error(err),
		)
	}
}

func (c *AssistantClient) touchAudience(ctx context.Context, userID int64, source pmrelay.AudienceSource) {
	if c == nil {
		return
	}
	c.mu.RLock()
	registry := c.audience
	logger := c.logger
	c.mu.RUnlock()
	touchAssistantAudience(ctx, registry, userID, source, logger)
}
