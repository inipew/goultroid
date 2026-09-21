package feature

import (
	"errors"
	"fmt"
	"strings"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

var (
	ErrInvalidSpec            = errors.New("feature: invalid spec")
	ErrDuplicateSurface       = errors.New("feature: duplicate surface")
	ErrCommandSurfaceDeclared = errors.New("feature: command surfaces are derived from core commands")
)

// InteractionKind identifies a non-command interaction exposed by a feature.
type InteractionKind string

const (
	InteractionInline   InteractionKind = "inline"
	InteractionAction   InteractionKind = "action"
	InteractionScreen   InteractionKind = "screen"
	InteractionDeepLink InteractionKind = "deep_link"
)

// InvocationPolicy declares who may initiate an interaction on each transport
// surface. Interaction declarations must be explicit for every enabled surface;
// InvocationDefault is intentionally rejected to prevent accidental exposure.
type InvocationPolicy struct {
	Userbot   core.InvocationAccess
	Assistant core.InvocationAccess
	Inline    core.InvocationAccess
}

// For returns the invocation rule declared for source.
func (p InvocationPolicy) For(source execution.Source) core.InvocationAccess {
	switch source {
	case execution.SourceUserbot:
		return p.Userbot
	case execution.SourceAssistant:
		return p.Assistant
	case execution.SourceInline:
		return p.Inline
	default:
		return core.InvocationDefault
	}
}

// Policy is transport-neutral authorization metadata shared by interactive
// surfaces. Permission and invocation remain separate just like core.Command:
// invocation answers who may initiate, while permission answers the required
// authorization tier.
type Policy struct {
	Permission  core.Permission
	Invocation  InvocationPolicy
	PrivateOnly bool
	GroupOnly   bool
}

// OwnerPolicy returns an explicit owner-only policy for the selected surfaces.
func OwnerPolicy(surfaces execution.SurfaceMask) Policy {
	return policyForAccess(core.PermissionOwner, core.InvocationSelfOnly, surfaces)
}

// SudoPolicy returns an explicit owner-or-sudo policy for the selected surfaces.
func SudoPolicy(surfaces execution.SurfaceMask) Policy {
	return policyForAccess(core.PermissionSudo, core.InvocationSelfOrSudo, surfaces)
}

// PublicPolicy returns an explicit public policy for the selected surfaces.
func PublicPolicy(surfaces execution.SurfaceMask) Policy {
	return policyForAccess(core.PermissionEveryone, core.InvocationAnyone, surfaces)
}

func policyForAccess(permission core.Permission, access core.InvocationAccess, surfaces execution.SurfaceMask) Policy {
	policy := Policy{Permission: permission}
	if surfaces.Supports(execution.SourceUserbot) {
		policy.Invocation.Userbot = access
	}
	if surfaces.Supports(execution.SourceAssistant) {
		policy.Invocation.Assistant = access
	}
	if surfaces.Supports(execution.SourceInline) {
		policy.Invocation.Inline = access
	}
	return policy
}

// CommandSurface is a catalog projection of the canonical core.Command. It is
// never an execution registry: handlers continue to live exclusively in
// core.Router and are derived here by the plugin manager.
type CommandSurface struct {
	Name        string
	Aliases     []string
	Description string
	Usage       string
	Category    string
	Surfaces    execution.SurfaceMask
	Policy      Policy
}

// Interaction declares a non-command entry point owned by a feature. P0 only
// defines identity, reachability, and policy; later phases bind these entries to
// typed inline/action/screen/deep-link handlers without changing the contract.
type Interaction struct {
	ID          string
	Kind        InteractionKind
	Description string
	Surfaces    execution.SurfaceMask
	Policy      Policy
}

// Spec is the canonical catalog declaration for one feature. Commands are
// always populated from Plugin.Commands by BindCanonicalCommands; providers
// declare only identity and non-command interactions during the migration.
type Spec struct {
	ID           string
	Name         string
	Description  string
	Category     string
	Commands     []CommandSurface
	Interactions []Interaction
}

// BindCanonicalCommands returns a validated copy of spec with command surfaces
// projected from the canonical core.Command declarations.
func BindCanonicalCommands(spec Spec, commands []core.Command) (Spec, error) {
	if len(spec.Commands) != 0 {
		return Spec{}, ErrCommandSurfaceDeclared
	}

	bound := cloneSpec(spec)
	bound.ID = normalizeID(bound.ID)
	if strings.TrimSpace(bound.Name) == "" {
		bound.Name = bound.ID
	}
	bound.Commands = make([]CommandSurface, 0, len(commands))
	for _, command := range commands {
		surfaces := command.Surfaces
		if surfaces == 0 {
			surfaces = execution.SurfaceUserbot
		}
		bound.Commands = append(bound.Commands, CommandSurface{
			Name:        strings.ToLower(strings.TrimSpace(command.Name)),
			Aliases:     normalizeAliases(command.Aliases),
			Description: command.Description,
			Usage:       command.Usage,
			Category:    command.Category,
			Surfaces:    surfaces,
			Policy: Policy{
				Permission: command.Permission,
				Invocation: InvocationPolicy{
					Userbot:   command.EffectiveInvocation(core.ExecutionInteractive),
					Assistant: command.EffectiveInvocation(core.ExecutionAssistant),
				},
				PrivateOnly: command.PrivateOnly,
				GroupOnly:   command.GroupOnly,
			},
		})
	}
	if err := bound.Validate(); err != nil {
		return Spec{}, err
	}
	return bound, nil
}

// Validate checks identity, surface uniqueness, and explicit interaction policy.
func (s Spec) Validate() error {
	if !validID(s.ID) {
		return fmt.Errorf("%w: invalid feature id %q", ErrInvalidSpec, s.ID)
	}
	if strings.TrimSpace(s.Name) == "" {
		return fmt.Errorf("%w: feature name is empty", ErrInvalidSpec)
	}

	commands := make(map[string]struct{}, len(s.Commands))
	for _, command := range s.Commands {
		name := strings.ToLower(strings.TrimSpace(command.Name))
		if !validID(name) {
			return fmt.Errorf("%w: invalid command name %q", ErrInvalidSpec, command.Name)
		}
		if _, exists := commands[name]; exists {
			return fmt.Errorf("%w: command %s", ErrDuplicateSurface, name)
		}
		commands[name] = struct{}{}
		if err := validateSurfaceMask(command.Surfaces); err != nil {
			return fmt.Errorf("%w: command %s: %v", ErrInvalidSpec, name, err)
		}
		if command.Policy.PrivateOnly && command.Policy.GroupOnly {
			return fmt.Errorf("%w: command %s cannot be both private-only and group-only", ErrInvalidSpec, name)
		}
	}

	interactions := make(map[string]struct{}, len(s.Interactions))
	for _, interaction := range s.Interactions {
		id := normalizeID(interaction.ID)
		if !validID(id) {
			return fmt.Errorf("%w: invalid %s id %q", ErrInvalidSpec, interaction.Kind, interaction.ID)
		}
		if !validInteractionKind(interaction.Kind) {
			return fmt.Errorf("%w: invalid interaction kind %q", ErrInvalidSpec, interaction.Kind)
		}
		key := string(interaction.Kind) + ":" + id
		if _, exists := interactions[key]; exists {
			return fmt.Errorf("%w: %s", ErrDuplicateSurface, key)
		}
		interactions[key] = struct{}{}
		if err := validateInteraction(interaction); err != nil {
			return fmt.Errorf("%w: %s: %v", ErrInvalidSpec, key, err)
		}
	}
	return nil
}

func validateInteraction(interaction Interaction) error {
	if err := validateSurfaceMask(interaction.Surfaces); err != nil {
		return err
	}
	if interaction.Policy.PrivateOnly && interaction.Policy.GroupOnly {
		return errors.New("cannot be both private-only and group-only")
	}

	switch interaction.Kind {
	case InteractionInline:
		if !interaction.Surfaces.Supports(execution.SourceInline) {
			return errors.New("inline interaction must expose the inline surface")
		}
	case InteractionAction:
		if !interaction.Surfaces.Supports(execution.SourceAssistant) && !interaction.Surfaces.Supports(execution.SourceInline) {
			return errors.New("action interaction must expose assistant or inline surface")
		}
	case InteractionDeepLink:
		if !interaction.Surfaces.Supports(execution.SourceAssistant) {
			return errors.New("deep-link interaction must expose the assistant surface")
		}
	}

	for _, source := range []execution.Source{execution.SourceUserbot, execution.SourceAssistant, execution.SourceInline} {
		if interaction.Surfaces.Supports(source) && interaction.Policy.Invocation.For(source) == core.InvocationDefault {
			return fmt.Errorf("invocation policy for %s surface is not explicit", source)
		}
	}
	return nil
}

func validateSurfaceMask(mask execution.SurfaceMask) error {
	if mask == 0 {
		return errors.New("surface mask is empty")
	}
	if mask&^execution.SurfaceAll != 0 {
		return fmt.Errorf("surface mask contains unsupported bits: %d", mask)
	}
	return nil
}

func validInteractionKind(kind InteractionKind) bool {
	switch kind {
	case InteractionInline, InteractionAction, InteractionScreen, InteractionDeepLink:
		return true
	default:
		return false
	}
}

func validID(value string) bool {
	value = strings.TrimSpace(value)
	if value == "" || value != strings.ToLower(value) {
		return false
	}
	for _, r := range value {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '.' || r == '_' || r == '-' {
			continue
		}
		return false
	}
	return true
}

func normalizeID(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func normalizeAliases(aliases []string) []string {
	if len(aliases) == 0 {
		return nil
	}
	result := make([]string, 0, len(aliases))
	for _, alias := range aliases {
		alias = normalizeID(alias)
		if alias != "" {
			result = append(result, alias)
		}
	}
	return result
}

func cloneSpec(spec Spec) Spec {
	copy := spec
	copy.Commands = append([]CommandSurface(nil), spec.Commands...)
	for i := range copy.Commands {
		copy.Commands[i].Aliases = append([]string(nil), copy.Commands[i].Aliases...)
	}
	copy.Interactions = append([]Interaction(nil), spec.Interactions...)
	return copy
}
