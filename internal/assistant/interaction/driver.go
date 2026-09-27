package interaction

import (
	"context"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
)

// Admitter applies the current Assistant authorization policy immediately
// before a driver opens a screen or executes an action. Drivers keep policy
// metadata in the feature catalog; the Assistant owns identity/permission data.
type Admitter func(featureID string, kind feature.InteractionKind, interactionID string, actorID int64, target presentation.Target) error

// MediaSender is the feature-driver transport boundary for Assistant-owned
// media side effects. Download and unrelated Telegram operations are excluded.
type MediaSender interface {
	SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error)
}

// MessageSender is the feature-driver transport boundary for Assistant-owned
// message delivery with optional reply markup.
type MessageSender interface {
	SendMessageWithMarkup(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error)
}

// DriverService is the complete Telegram surface exposed to Assistant feature
// drivers. It remains deliberately narrower than core Telegram services.
type DriverService interface {
	MediaSender
	MessageSender
}

// DriverRuntime is the transport-bound runtime supplied to feature drivers while
// one Assistant generation is running. It deliberately exposes only the canonical Assistant interaction
// orchestration surface, read-only catalog, admission callback, and Telegram
// delivery operations needed for feature-owned side effects.
type DriverRuntime struct {
	Engine  *orchestration.Engine
	Catalog feature.Catalog
	Service DriverService
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
