package interaction

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
)

// Admitter applies the current Assistant authorization policy immediately
// before a driver opens a screen or executes an action. Drivers keep policy
// metadata in the feature catalog; the Assistant owns identity/permission data.
type Admitter func(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error

// DriverRuntime is the transport-bound runtime supplied to feature drivers while
// one Assistant generation is running. It deliberately exposes only the canonical Assistant interaction
// orchestration surface, read-only catalog, admission callback, and Telegram
// service needed for feature-owned media side effects.
type DriverRuntime struct {
	Engine  *orchestration.Engine
	Catalog feature.Catalog
	Service core.TelegramServicer
	Admit   Admitter
}

// FeatureDriver is implemented by feature plugins that own Assistant interaction
// screens/actions and free-form input. Bind returns a cleanup that must detach
// transport-bound registrations before the Assistant generation disappears.
type FeatureDriver interface {
	AssistantFeatureID() string
	BindAssistant(DriverRuntime) (func(), error)
	HandleAssistantInput(*orchestration.Context, string) error
}
