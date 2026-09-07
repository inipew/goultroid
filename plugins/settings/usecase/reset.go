package usecase

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/settings"
)

// ResetSettingUseCase encapsulates the single mutation path for setting resets.
type ResetSettingUseCase struct {
	Service *settings.Service
}

// Execute validates scope and delegates to the domain service.
func (uc *ResetSettingUseCase) Execute(ctx context.Context, scope settings.SettingScope, scopeID int64, ns, key string, actorID int64) error {
	if uc.Service == nil {
		return fmt.Errorf("settings service not configured")
	}
	if err := (settings.ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return err
	}
	return uc.Service.Reset(ctx, scope, scopeID, ns, key, actorID)
}
