package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Surface string

const (
	SurfaceAssistantCommand Surface = "assistant_command"
	SurfaceInline           Surface = "inline"
	SurfaceDeepLink         Surface = "deep_link"
	SurfaceCallback         Surface = "callback"
)

const (
	MaxBindingAliasBytes = 64
	MaxBindingList       = 200
)

var (
	ErrInvalidBinding  = errors.New("saved response: invalid surface binding")
	ErrBindingExists   = errors.New("saved response: surface binding already exists")
	ErrBindingNotFound = errors.New("saved response: surface binding not found")
	ErrBindingConflict = errors.New("saved response: surface binding revision conflict")
	ErrBindingDisabled = errors.New("saved response: surface binding disabled")
)

// SurfaceBinding exposes one provider-owned SavedResponse on a stable external
// surface identity. It contains no response text/media and therefore cannot
// become a second source of truth for SavedResponse content.
type SurfaceBinding struct {
	Surface   Surface
	Alias     string
	Reference Reference
	Enabled   bool
	Revision  uint64
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (b SurfaceBinding) Normalize() (SurfaceBinding, error) {
	b.Surface = Surface(strings.ToLower(strings.TrimSpace(string(b.Surface))))
	b.Alias = strings.ToLower(strings.TrimSpace(b.Alias))
	b.Reference.Provider = normalizeProvider(b.Reference.Provider)
	b.Reference.Key = strings.TrimSpace(b.Reference.Key)
	if !validSurface(b.Surface) || !validBindingAlias(b.Alias) {
		return SurfaceBinding{}, ErrInvalidBinding
	}
	if err := b.Reference.Validate(); err != nil {
		return SurfaceBinding{}, fmt.Errorf("%w: %v", ErrInvalidBinding, err)
	}
	return b, nil
}

func validSurface(surface Surface) bool {
	switch surface {
	case SurfaceAssistantCommand, SurfaceInline, SurfaceDeepLink, SurfaceCallback:
		return true
	default:
		return false
	}
}

func validBindingAlias(alias string) bool {
	if alias == "" || len(alias) > MaxBindingAliasBytes {
		return false
	}
	for _, r := range alias {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

// SurfaceBindingRepository persists routing metadata only. Provider content and
// media remain in the owning subsystem repository referenced by Reference.
type SurfaceBindingRepository interface {
	CreateBinding(context.Context, SurfaceBinding) (SurfaceBinding, error)
	GetBinding(context.Context, Surface, string) (*SurfaceBinding, error)
	ListBindings(context.Context, Surface, bool, int) ([]SurfaceBinding, error)
	UpdateBinding(context.Context, SurfaceBinding, uint64) (SurfaceBinding, error)
	SetBindingEnabled(context.Context, Surface, string, bool, uint64) (SurfaceBinding, error)
	DeleteBinding(context.Context, Surface, string, uint64) error
}

type ResolvedBinding struct {
	Binding  SurfaceBinding
	Resolved Resolved
}

// BindingService joins durable surface metadata to the lifecycle-aware provider
// registry. Consumers must carry Resolved.Scope into TaskEngine admission so a
// provider disable/reload between resolution and execution still fails closed.
type BindingService struct {
	bindings  SurfaceBindingRepository
	responses *Registry
}

func NewBindingService(bindings SurfaceBindingRepository, responses *Registry) *BindingService {
	return &BindingService{bindings: bindings, responses: responses}
}

func (s *BindingService) Resolve(ctx context.Context, surface Surface, alias string) (ResolvedBinding, error) {
	if s == nil || s.bindings == nil || s.responses == nil {
		return ResolvedBinding{}, ErrResolverUnavailable
	}
	surface, alias, err := normalizeSurfaceAlias(surface, alias)
	if err != nil {
		return ResolvedBinding{}, err
	}
	binding, err := s.bindings.GetBinding(ctx, surface, alias)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if binding == nil {
		return ResolvedBinding{}, ErrBindingNotFound
	}
	if !binding.Enabled {
		return ResolvedBinding{}, ErrBindingDisabled
	}
	resolved, err := s.responses.Resolve(ctx, binding.Reference)
	if err != nil {
		return ResolvedBinding{}, err
	}
	return ResolvedBinding{Binding: *binding, Resolved: resolved}, nil
}
