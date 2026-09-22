package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
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
	ErrBindingStale    = errors.New("saved response: prepared surface binding is stale")
	ErrBindingReserved = errors.New("saved response: surface alias is reserved by canonical runtime")
)

// SurfaceBinding exposes one provider-owned SavedResponse on a stable external
// surface identity. It contains no response text/media and therefore cannot
// become a second source of truth for SavedResponse content.
type SurfaceBinding struct {
	Surface   Surface
	Alias     string
	Reference Reference
	Enabled     bool
	Revision    uint64
	Incarnation string
	CreatedAt   time.Time
	UpdatedAt time.Time
}

func (b SurfaceBinding) Normalize() (SurfaceBinding, error) {
	b.Surface = Surface(strings.ToLower(strings.TrimSpace(string(b.Surface))))
	b.Alias = strings.ToLower(strings.TrimSpace(b.Alias))
	b.Reference.Provider = normalizeProvider(b.Reference.Provider)
	b.Reference.Key = strings.TrimSpace(b.Reference.Key)
	b.Incarnation = strings.TrimSpace(b.Incarnation)
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
	UpdateBinding(context.Context, Surface, string, Reference, bool, uint64, string) (SurfaceBinding, error)
	SetBindingEnabled(context.Context, Surface, string, bool, uint64, string) (SurfaceBinding, error)
	DeleteBinding(context.Context, Surface, string, uint64, string) error
}

type ResolvedBinding struct {
	Binding  SurfaceBinding
	Resolved Resolved
}

// PreparedBinding freezes routing identity and provider generation before
// TaskEngine admission without retaining a response payload across queue wait.
type PreparedBinding struct {
	binding  SurfaceBinding
	scope    tasks.ScopeIdentity
	hasMedia bool
}

func (p PreparedBinding) Binding() SurfaceBinding    { return p.binding }
func (p PreparedBinding) Scope() tasks.ScopeIdentity { return p.scope }
func (p PreparedBinding) HasMedia() bool             { return p.hasMedia }

// BindingService joins durable surface metadata to the lifecycle-aware provider
// registry. Consumers must carry Resolved.Scope into TaskEngine admission so a
// provider disable/reload between resolution and execution still fails closed.
// AliasGuard rejects enabled aliases shadowed by canonical runtime surfaces.
type AliasGuard func(Surface, string) error

type BindingService struct {
	bindings  SurfaceBindingRepository
	responses *Registry
	guard     AliasGuard
}

func NewBindingService(bindings SurfaceBindingRepository, responses *Registry) *BindingService {
	return &BindingService{bindings: bindings, responses: responses}
}

// SetAliasGuard installs the composition-owned collision policy. Durable aliases
// remain namespaced by Surface; this hook only prevents an enabled binding from
// being silently shadowed by a canonical handler on the same surface.
func (s *BindingService) SetAliasGuard(guard AliasGuard) {
	if s != nil {
		s.guard = guard
	}
}

func (s *BindingService) checkAlias(surface Surface, alias string, enabled bool) error {
	if s == nil || !enabled || s.guard == nil {
		return nil
	}
	if err := s.guard(surface, alias); err != nil {
		if errors.Is(err, ErrBindingReserved) {
			return err
		}
		return fmt.Errorf("%w: %v", ErrBindingReserved, err)
	}
	return nil
}

func (s *BindingService) Create(ctx context.Context, binding SurfaceBinding) (SurfaceBinding, error) {
	if s == nil || s.bindings == nil || s.responses == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	normalized, err := binding.Normalize()
	if err != nil {
		return SurfaceBinding{}, err
	}
	if err := s.checkAlias(normalized.Surface, normalized.Alias, normalized.Enabled); err != nil {
		return SurfaceBinding{}, err
	}
	if _, err := s.responses.Resolve(ctx, normalized.Reference); err != nil {
		return SurfaceBinding{}, err
	}
	return s.bindings.CreateBinding(ctx, normalized)
}

func (s *BindingService) Get(ctx context.Context, surface Surface, alias string) (*SurfaceBinding, error) {
	if s == nil || s.bindings == nil {
		return nil, ErrResolverUnavailable
	}
	return s.bindings.GetBinding(ctx, surface, alias)
}

func (s *BindingService) List(ctx context.Context, surface Surface, includeDisabled bool, limit int) ([]SurfaceBinding, error) {
	if s == nil || s.bindings == nil {
		return nil, ErrResolverUnavailable
	}
	return s.bindings.ListBindings(ctx, surface, includeDisabled, limit)
}

// ValidateEnabledCollisions rechecks durable enabled aliases against the live
// canonical runtime after plugin/inline registration has settled. This closes
// the restart window where a binding persisted before a new canonical handler
// existed and would otherwise become silently shadowed.
func (s *BindingService) ValidateEnabledCollisions(ctx context.Context) error {
	if s == nil || s.bindings == nil {
		return ErrResolverUnavailable
	}
	for _, surface := range []Surface{
		SurfaceAssistantCommand,
		SurfaceInline,
		SurfaceDeepLink,
		SurfaceCallback,
	} {
		items, err := s.bindings.ListBindings(ctx, surface, false, MaxBindingList)
		if err != nil {
			return err
		}
		for _, item := range items {
			if err := s.checkAlias(item.Surface, item.Alias, true); err != nil {
				return fmt.Errorf("%s:%s: %w", item.Surface, item.Alias, err)
			}
		}
	}
	return nil
}

func (s *BindingService) Update(
	ctx context.Context,
	surface Surface,
	alias string,
	reference Reference,
	enabled bool,
	expectedRevision uint64,
	expectedIncarnation string,
) (SurfaceBinding, error) {
	if s == nil || s.bindings == nil || s.responses == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	normalized, err := (SurfaceBinding{
		Surface:   surface,
		Alias:     alias,
		Reference: reference,
		Enabled:   enabled,
	}).Normalize()
	if err != nil {
		return SurfaceBinding{}, err
	}
	current, err := s.bindings.GetBinding(ctx, normalized.Surface, normalized.Alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	if current == nil {
		return SurfaceBinding{}, ErrBindingNotFound
	}
	expectedIncarnation = strings.TrimSpace(expectedIncarnation)
	if expectedRevision == 0 || expectedIncarnation == "" ||
		current.Revision != expectedRevision ||
		current.Incarnation != expectedIncarnation {
		return SurfaceBinding{}, ErrBindingConflict
	}
	if err := s.checkAlias(normalized.Surface, normalized.Alias, normalized.Enabled); err != nil {
		return SurfaceBinding{}, err
	}
	if _, err := s.responses.Resolve(ctx, normalized.Reference); err != nil {
		return SurfaceBinding{}, err
	}
	return s.bindings.UpdateBinding(
		ctx,
		normalized.Surface,
		normalized.Alias,
		normalized.Reference,
		normalized.Enabled,
		expectedRevision,
		expectedIncarnation,
	)
}

func (s *BindingService) SetEnabled(
	ctx context.Context,
	surface Surface,
	alias string,
	enabled bool,
	expectedRevision uint64,
	expectedIncarnation string,
) (SurfaceBinding, error) {
	if s == nil || s.bindings == nil {
		return SurfaceBinding{}, ErrResolverUnavailable
	}
	current, err := s.bindings.GetBinding(ctx, surface, alias)
	if err != nil {
		return SurfaceBinding{}, err
	}
	if current == nil {
		return SurfaceBinding{}, ErrBindingNotFound
	}
	expectedIncarnation = strings.TrimSpace(expectedIncarnation)
	if expectedRevision == 0 || expectedIncarnation == "" ||
		current.Revision != expectedRevision ||
		current.Incarnation != expectedIncarnation {
		return SurfaceBinding{}, ErrBindingConflict
	}
	if enabled {
		if err := s.checkAlias(current.Surface, current.Alias, true); err != nil {
			return SurfaceBinding{}, err
		}
		if s.responses == nil {
			return SurfaceBinding{}, ErrResolverUnavailable
		}
		if _, err := s.responses.Resolve(ctx, current.Reference); err != nil {
			return SurfaceBinding{}, err
		}
	}
	return s.bindings.SetBindingEnabled(
		ctx,
		current.Surface,
		current.Alias,
		enabled,
		expectedRevision,
		expectedIncarnation,
	)
}

func (s *BindingService) Delete(
	ctx context.Context,
	surface Surface,
	alias string,
	expectedRevision uint64,
	expectedIncarnation string,
) error {
	if s == nil || s.bindings == nil {
		return ErrResolverUnavailable
	}
	expectedIncarnation = strings.TrimSpace(expectedIncarnation)
	if expectedRevision == 0 || expectedIncarnation == "" {
		return ErrBindingConflict
	}
	return s.bindings.DeleteBinding(ctx, surface, alias, expectedRevision, expectedIncarnation)
}

func (s *BindingService) Prepare(ctx context.Context, surface Surface, alias string) (PreparedBinding, error) {
	if s == nil || s.bindings == nil || s.responses == nil {
		return PreparedBinding{}, ErrResolverUnavailable
	}
	surface, alias, err := normalizeSurfaceAlias(surface, alias)
	if err != nil {
		return PreparedBinding{}, err
	}
	binding, err := s.bindings.GetBinding(ctx, surface, alias)
	if err != nil {
		return PreparedBinding{}, err
	}
	if binding == nil {
		return PreparedBinding{}, ErrBindingNotFound
	}
	if !binding.Enabled {
		return PreparedBinding{}, ErrBindingDisabled
	}
	resolved, err := s.responses.Resolve(ctx, binding.Reference)
	if err != nil {
		return PreparedBinding{}, err
	}
	return PreparedBinding{
		binding:  *binding,
		scope:    resolved.Scope,
		hasMedia: resolved.Response.HasMedia(),
	}, nil
}

// ResolvePrepared revalidates the exact durable binding revision and provider
// generation after queueing, then loads the latest authoritative response.
func (s *BindingService) ResolvePrepared(ctx context.Context, prepared PreparedBinding) (ResolvedBinding, error) {
	if s == nil || s.bindings == nil || s.responses == nil || prepared.scope.IsZero() {
		return ResolvedBinding{}, ErrResolverUnavailable
	}
	current, err := s.bindings.GetBinding(ctx, prepared.binding.Surface, prepared.binding.Alias)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if current == nil || !current.Enabled ||
		current.Revision != prepared.binding.Revision ||
		current.Incarnation != prepared.binding.Incarnation ||
		current.Reference != prepared.binding.Reference {
		return ResolvedBinding{}, ErrBindingStale
	}
	resolved, err := s.responses.Resolve(ctx, current.Reference)
	if err != nil {
		return ResolvedBinding{}, err
	}
	if resolved.Scope != prepared.scope || resolved.Response.HasMedia() != prepared.hasMedia {
		return ResolvedBinding{}, ErrBindingStale
	}
	return ResolvedBinding{Binding: *current, Resolved: resolved}, nil
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
