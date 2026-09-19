package core

// InvocationAccess controls which human principals may initiate a command.
// Authorization is enforced separately by Permission.
type InvocationAccess uint8

const (
	// InvocationDefault preserves source-specific compatibility defaults.
	InvocationDefault InvocationAccess = iota
	// InvocationSelfOnly permits only the configured userbot owner.
	InvocationSelfOnly
	// InvocationSelfOrSudo permits the owner and configured sudo users.
	InvocationSelfOrSudo
	// InvocationAnyone permits any human principal to initiate the command.
	InvocationAnyone
)

func (a InvocationAccess) String() string {
	switch a {
	case InvocationSelfOnly:
		return "SelfOnly"
	case InvocationSelfOrSudo:
		return "SelfOrSudo"
	case InvocationAnyone:
		return "Anyone"
	default:
		return "Default"
	}
}

// InvocationPolicy is intentionally per-surface. A command can be private on
// the userbot account while remaining publicly invokable through the Assistant
// bot, without weakening its Permission authorization tier.
type InvocationPolicy struct {
	Userbot   InvocationAccess
	Assistant InvocationAccess
}

// EffectiveInvocation returns the invocation rule used for source.
//
// Defaults are migration-safe:
//   - interactive userbot commands are SelfOnly;
//   - legacy PermissionSudo commands remain SelfOrSudo on userbot;
//   - Assistant commands remain Anyone at the invocation layer and continue to
//     rely on Permission for authorization;
//   - non-human/internal execution sources bypass invocation gating.
func (c Command) EffectiveInvocation(source ExecutionSource) InvocationAccess {
	switch source {
	case ExecutionInteractive:
		if c.Invocation.Userbot != InvocationDefault {
			return c.Invocation.Userbot
		}
		if c.Permission == PermissionSudo {
			return InvocationSelfOrSudo
		}
		return InvocationSelfOnly
	case ExecutionAssistant:
		if c.Invocation.Assistant != InvocationDefault {
			return c.Invocation.Assistant
		}
		return InvocationAnyone
	default:
		return InvocationAnyone
	}
}

// CanInvoke reports whether a human principal may initiate cmd from source.
// This does not replace PermissionMiddleware: Invocation answers "who may
// initiate?", while Permission answers "what authorization tier is required?".
func (c Command) CanInvoke(source ExecutionSource, senderID int64, outgoing bool, perms *Permissions) bool {
	switch source {
	case ExecutionInteractive, ExecutionAssistant:
		// Continue below.
	default:
		return true
	}

	switch c.EffectiveInvocation(source) {
	case InvocationAnyone:
		return true
	case InvocationSelfOnly:
		if source == ExecutionInteractive && outgoing {
			return true
		}
		return perms != nil && perms.IsOwner(senderID)
	case InvocationSelfOrSudo:
		if source == ExecutionInteractive && outgoing {
			return true
		}
		return perms != nil && perms.IsSudo(senderID)
	default:
		return false
	}
}
