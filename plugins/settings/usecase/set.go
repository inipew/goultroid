package usecase

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/settings"
)

// SetSettingUseCase encapsulates the single mutation path for setting changes.
// Both command (.config set) and button callbacks must use this use-case
// so validation, canonicalization, and audit events are consistent.
type SetSettingUseCase struct {
	Service *settings.Service
}

// Execute validates scope and delegates to the domain service.
func (uc *SetSettingUseCase) Execute(ctx context.Context, scope settings.SettingScope, scopeID int64, ns, key, value string, actorID int64) error {
	if uc.Service == nil {
		return fmt.Errorf("settings service not configured")
	}
	if err := (settings.ScopeRef{Type: scope, ID: scopeID}).Validate(); err != nil {
		return err
	}
	return uc.Service.Set(ctx, scope, scopeID, ns, key, value, actorID)
}
