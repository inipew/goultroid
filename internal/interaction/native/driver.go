package native

import (
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/tasks"
)

// DriverRuntime is supplied after the plugin's FeatureSpec is staged for the
// current generation. Drivers register only transport bindings here; session
// state and action ownership remain in the shared a2 runtime.
type DriverRuntime struct {
	Interactions *Adapter
	Catalog      feature.Catalog
	Scope        tasks.ScopeIdentity
}

// FeatureDriver lets a plugin bind native/userbot a2 actions transactionally to
// its plugin generation. Plugin manager invokes the returned cleanup before the
// generation is removed.
type FeatureDriver interface {
	NativeFeatureID() string
	BindNative(DriverRuntime) (func(), error)
}

// OptionalFeatureDriver permits a feature with an existing text-only command
// path to start without the native a2 transport. Strict native drivers still
// require a bound adapter. Optional drivers must provide their own safe fallback.
type OptionalFeatureDriver interface {
	FeatureDriver
	NativeOptional() bool
}
