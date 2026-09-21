package interaction

import (
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
)

// V2Admitter applies the current Assistant authorization policy immediately
// before a driver opens a screen or executes an action. Drivers keep policy
// metadata in the feature catalog; the Assistant owns identity/permission data.
type V2Admitter func(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error

// V2Runtime is the transport-bound runtime supplied to feature drivers while
// one Assistant generation is running. It deliberately exposes only the a2
// orchestration surface, read-only catalog, admission callback, and Telegram
// service needed for feature-owned media side effects.
type V2Runtime struct {
	Engine  *orchestration.Engine
	Catalog feature.Catalog
	Service core.TelegramServicer
	Admit   V2Admitter
}

// V2FeatureDriver is implemented by feature plugins that own Assistant a2
// screens/actions and free-form input. Bind returns a cleanup that must detach
// transport-bound registrations before the Assistant generation disappears.
type V2FeatureDriver interface {
	AssistantFeatureID() string
	BindAssistantV2(V2Runtime) (func(), error)
	HandleAssistantV2Input(*orchestration.Context, string) error
}
