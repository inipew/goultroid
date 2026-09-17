package presentation

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/ui"
)

// ScreenKey identifies a presentation screen uniquely across namespace, name, and version.
type ScreenKey struct {
	Namespace string
	Name      string
	Version   uint16
}

// String returns the canonical string representation of a ScreenKey.
func (k ScreenKey) String() string {
	if k.Version == 0 {
		return fmt.Sprintf("%s:%s", k.Namespace, k.Name)
	}
	return fmt.Sprintf("%s:%s:v%d", k.Namespace, k.Name, k.Version)
}

// IsZero reports whether the key is uninitialized.
func (k ScreenKey) IsZero() bool {
	return k.Namespace == "" && k.Name == "" && k.Version == 0
}

// ChatType identifies the conversation environment where the presentation is rendered.
type ChatType string

const (
	ChatTypePrivate    ChatType = "private"
	ChatTypeGroup      ChatType = "group"
	ChatTypeSupergroup ChatType = "supergroup"
	ChatTypeChannel    ChatType = "channel"
)

// CacheControl indicates caching constraints for a built presentation.
type CacheControl uint8

const (
	CacheControlNone CacheControl = iota
	CacheControlPrivate
	CacheControlGlobal
)

// Sensitivity indicates whether a screen contains sensitive or private data.
type Sensitivity uint8

const (
	SensitivityNormal Sensitivity = iota
	SensitivitySensitive
)

// BuildRequest encapsulates the context and parameters required to build a Screen.
type BuildRequest struct {
	Key           ScreenKey
	Actor         execution.Actor
	Source        execution.Source
	ChatType      ChatType
	MenuOwner     int64
	Locale        string
	CorrelationID string
	Input         any
}

// BuildResult holds the output of a Screen build operation.
type BuildResult struct {
	Screen       *ui.Screen
	CacheControl CacheControl
	Sensitivity  Sensitivity
}

// Builder constructs a UI screen deterministically from a BuildRequest.
type Builder interface {
	Key() ScreenKey
	Build(ctx context.Context, req BuildRequest) (BuildResult, error)
}
