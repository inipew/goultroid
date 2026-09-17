package presentation

import (
	"context"

	"github.com/inipew/goultroid/internal/execution"
)

// Permission specifies the authorization level required to access a presentation.
type Permission uint8

const (
	PermissionPublic Permission = iota
	PermissionAuthenticated
	PermissionSudo
	PermissionOwner
)

// String returns the human-readable representation of a Permission.
func (p Permission) String() string {
	switch p {
	case PermissionPublic:
		return "public"
	case PermissionAuthenticated:
		return "authenticated"
	case PermissionSudo:
		return "sudo"
	case PermissionOwner:
		return "owner"
	default:
		return "unknown"
	}
}

// ChatTypeMask defines a bitmask for supported chat environments.
type ChatTypeMask uint8

const (
	ChatMaskPrivate ChatTypeMask = 1 << iota
	ChatMaskGroup
	ChatMaskSupergroup
	ChatMaskChannel

	ChatMaskAll = ChatMaskPrivate | ChatMaskGroup | ChatMaskSupergroup | ChatMaskChannel
)

// Matches reports whether the mask contains the specified ChatType.
func (m ChatTypeMask) Matches(ct ChatType) bool {
	if m == 0 {
		return true // unconstrained by default
	}
	switch ct {
	case ChatTypePrivate:
		return m&ChatMaskPrivate != 0
	case ChatTypeGroup:
		return m&ChatMaskGroup != 0
	case ChatTypeSupergroup:
		return m&ChatMaskSupergroup != 0
	case ChatTypeChannel:
		return m&ChatMaskChannel != 0
	default:
		return false
	}
}

// PolicyPredicate enables custom, fine-grained access logic beyond standard roles.
type PolicyPredicate func(ctx context.Context, req PolicyRequest) (bool, error)

// AccessPolicy defines the complete authorization and surface constraint for a Screen.
type AccessPolicy struct {
	Permission       Permission
	AllowedSources   execution.SurfaceMask
	AllowedChatTypes ChatTypeMask
	RequirePrivate   bool
	RequireMenuOwner bool
	Sensitive        bool
	Predicate        PolicyPredicate
}

// PublicPolicy returns a default policy open to all callers and surfaces.
func PublicPolicy() AccessPolicy {
	return AccessPolicy{
		Permission:       PermissionPublic,
		AllowedSources:   execution.SurfaceAll,
		AllowedChatTypes: ChatMaskAll,
	}
}

// OwnerOnlyPolicy returns a policy restricted to the bot owner.
func OwnerOnlyPolicy() AccessPolicy {
	return AccessPolicy{
		Permission:       PermissionOwner,
		AllowedSources:   execution.SurfaceAll,
		AllowedChatTypes: ChatMaskAll,
	}
}

// DecisionCode classifies the outcome of a policy evaluation.
type DecisionCode string

const (
	DecisionAllow          DecisionCode = "allow"
	DecisionDenySource     DecisionCode = "deny_source"
	DecisionDenyAuth       DecisionCode = "deny_unauthenticated"
	DecisionDenyPermission DecisionCode = "deny_permission"
	DecisionDenyChatType   DecisionCode = "deny_chat_type"
	DecisionDenyPrivate    DecisionCode = "deny_private_required"
	DecisionDenyMenuOwner  DecisionCode = "deny_menu_owner"
	DecisionDenyPredicate  DecisionCode = "deny_predicate"
)

// HandoffHint provides guidance when access is denied or redirected.
type HandoffHint struct {
	SuggestedMode string
	DeepLinkURL   string
}

// Decision represents the immutable outcome of evaluating an AccessPolicy.
type Decision struct {
	Allowed     bool
	Code        DecisionCode
	AuditReason string
	SafeMessage string
	PrivateLink *HandoffHint
}

// PolicyRequest captures facts needed for policy evaluation.
type PolicyRequest struct {
	Actor     execution.Actor
	Source    execution.Source
	ChatType  ChatType
	Screen    ScreenKey
	MenuOwner int64
}

// Evaluator verifies whether a PolicyRequest complies with an AccessPolicy.
type Evaluator struct {
	ownerID    int64
	sudoGetter func() []int64
}

// NewEvaluator creates an initialized Evaluator.
func NewEvaluator(ownerID int64, sudoGetter func() []int64) *Evaluator {
	return &Evaluator{
		ownerID:    ownerID,
		sudoGetter: sudoGetter,
	}
}

// Evaluate evaluates the policy fail-closed.
func (e *Evaluator) Evaluate(ctx context.Context, policy AccessPolicy, req PolicyRequest) Decision {
	// 1. Validate surface source
	if policy.AllowedSources != 0 && !policy.AllowedSources.Supports(req.Source) {
		return Decision{
			Allowed:     false,
			Code:        DecisionDenySource,
			AuditReason: "source surface not permitted by policy",
			SafeMessage: "This screen is not supported on this surface.",
		}
	}

	// 2. Validate authentication if not public
	if policy.Permission != PermissionPublic && req.Actor.UserID == 0 {
		return Decision{
			Allowed:     false,
			Code:        DecisionDenyAuth,
			AuditReason: "caller user ID is zero for non-public screen",
			SafeMessage: "Authentication required.",
		}
	}

	// 3. Validate permissions (owner / sudo)
	isOwner := req.Actor.IsOwner || (e.ownerID != 0 && req.Actor.UserID == e.ownerID)
	isSudo := isOwner || req.Actor.IsSudo
	if !isSudo && e.sudoGetter != nil && req.Actor.UserID != 0 {
		for _, id := range e.sudoGetter() {
			if id == req.Actor.UserID {
				isSudo = true
				break
			}
		}
	}

	switch policy.Permission {
	case PermissionOwner:
		if !isOwner {
			return Decision{
				Allowed:     false,
				Code:        DecisionDenyPermission,
				AuditReason: "owner permission required",
				SafeMessage: "This feature is restricted to the bot owner.",
			}
		}
	case PermissionSudo:
		if !isSudo {
			return Decision{
				Allowed:     false,
				Code:        DecisionDenyPermission,
				AuditReason: "sudo permission required",
				SafeMessage: "This feature requires sudo authorization.",
			}
		}
	case PermissionAuthenticated:
		// UserID checked above
	case PermissionPublic:
		// Allowed for all
	}

	// 4. Validate chat type / private requirement
	if policy.RequirePrivate && req.ChatType != ChatTypePrivate {
		return Decision{
			Allowed:     false,
			Code:        DecisionDenyPrivate,
			AuditReason: "private chat required for sensitive screen",
			SafeMessage: "This feature is only available in a private chat with the assistant.",
			PrivateLink: &HandoffHint{SuggestedMode: "deep_link"},
		}
	}

	if policy.AllowedChatTypes != 0 && req.ChatType != "" && !policy.AllowedChatTypes.Matches(req.ChatType) {
		return Decision{
			Allowed:     false,
			Code:        DecisionDenyChatType,
			AuditReason: "chat type not allowed by policy mask",
			SafeMessage: "This screen cannot be displayed in this type of chat.",
		}
	}

	// 5. Validate menu instance owner if requested
	if policy.RequireMenuOwner {
		if req.MenuOwner == 0 {
			return Decision{
				Allowed:     false,
				Code:        DecisionDenyMenuOwner,
				AuditReason: "menu owner required but not provided in request",
				SafeMessage: "Menu session owner is missing or unverified.",
			}
		}
		if req.Actor.UserID != req.MenuOwner && !isOwner {
			return Decision{
				Allowed:     false,
				Code:        DecisionDenyMenuOwner,
				AuditReason: "caller does not own the active menu session",
				SafeMessage: "You are not the owner of this menu session.",
			}
		}
	}

	// 6. Validate custom predicate if configured
	if policy.Predicate != nil {
		allowed, err := policy.Predicate(ctx, req)
		if err != nil || !allowed {
			reason := "predicate rejected request"
			if err != nil {
				reason = "predicate returned error"
			}
			return Decision{
				Allowed:     false,
				Code:        DecisionDenyPredicate,
				AuditReason: reason,
				SafeMessage: "Access denied by feature policy.",
			}
		}
	}

	return Decision{
		Allowed: true,
		Code:    DecisionAllow,
	}
}
