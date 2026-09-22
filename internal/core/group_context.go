package core

import "strings"

// ChatKind is the canonical transport-neutral classification used by the
// Assistant group/manager plane. In particular, a broadcast channel is not a
// supergroup even though both are represented by Telegram as channel peers.
type ChatKind string

const (
	ChatKindUnknown    ChatKind = "unknown"
	ChatKindPrivate    ChatKind = "private"
	ChatKindGroup      ChatKind = "group"
	ChatKindSupergroup ChatKind = "supergroup"
	ChatKindChannel    ChatKind = "channel"
)

// NormalizeChatKind converts legacy Chat.Type strings into the canonical kind.
func NormalizeChatKind(value string) ChatKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ChatKindPrivate):
		return ChatKindPrivate
	case string(ChatKindGroup):
		return ChatKindGroup
	case string(ChatKindSupergroup):
		return ChatKindSupergroup
	case string(ChatKindChannel):
		return ChatKindChannel
	default:
		return ChatKindUnknown
	}
}

// Kind returns the canonical kind for a chat.
func (c *Chat) Kind() ChatKind {
	if c == nil {
		return ChatKindUnknown
	}
	return NormalizeChatKind(c.Type)
}

// IsManagerGroup reports whether the chat supports the Assistant group manager
// plane. Broadcast channels deliberately fail closed until a channel-specific
// capability is introduced.
func (c *Chat) IsManagerGroup() bool {
	switch c.Kind() {
	case ChatKindGroup, ChatKindSupergroup:
		return true
	default:
		return false
	}
}

// GroupActorRole describes Telegram's chat-scoped role independently from the
// global Owner/Sudo permission tiers. P7-B is responsible for resolving it.
type GroupActorRole string

const (
	GroupActorRoleUnknown       GroupActorRole = "unknown"
	GroupActorRoleMember        GroupActorRole = "member"
	GroupActorRoleRestricted    GroupActorRole = "restricted"
	GroupActorRoleAdministrator GroupActorRole = "administrator"
	GroupActorRoleCreator       GroupActorRole = "creator"
	GroupActorRoleBanned        GroupActorRole = "banned"
	GroupActorRoleLeft          GroupActorRole = "left"
)

// GroupAdminRights is the transport-neutral subset of Telegram administrator
// rights required by manager operations. These are contextual capabilities,
// not global Goultroid permissions.
type GroupAdminRights struct {
	ChangeInfo     bool
	DeleteMessages bool
	BanUsers       bool
	InviteUsers    bool
	PinMessages    bool
	AddAdmins      bool
	ManageTopics   bool
}

// GroupActorPrincipal separates global Goultroid identity from Telegram's
// chat-scoped role. Role/rights remain unverified until P7-B resolves them.
type GroupActorPrincipal struct {
	UserID     int64
	IsOwner    bool
	IsSudo     bool
	Role       GroupActorRole
	Rights     GroupAdminRights
	CanEdit    bool
	PromotedBy int64
	Verified   bool
}

// GroupExecutionContext is the canonical contextual principal envelope for the
// Assistant manager plane. Feature identifies the canonical command/feature
// currently being evaluated; TopicID scopes forum-thread semantics.
type GroupExecutionContext struct {
	ChatID  int64
	Kind    ChatKind
	TopicID int
	Source  ExecutionSource
	Feature string
	Actor   GroupActorPrincipal
}

// GroupExecution derives the contextual manager-plane identity from a command
// Context without performing Telegram RPC. The chat-scoped role remains
// unknown/unverified until a GroupRoleResolver enriches it in P7-B.
func (c *Context) GroupExecution() (GroupExecutionContext, bool) {
	if c == nil || c.Chat == nil {
		return GroupExecutionContext{}, false
	}
	kind := c.Chat.Kind()
	if kind == ChatKindUnknown || kind == ChatKindPrivate {
		return GroupExecutionContext{}, false
	}

	actor := GroupActorPrincipal{UserID: c.SenderID(), Role: GroupActorRoleUnknown}
	if principal := c.GetPrincipal(); principal != nil {
		actor.IsOwner = principal.IsOwner
		actor.IsSudo = principal.IsSudo
	}
	if contextual := c.GroupPrincipal; contextual != nil && contextual.UserID == actor.UserID {
		actor.Role = contextual.Role
		actor.Rights = contextual.Rights
		actor.Verified = contextual.Verified
	}

	return GroupExecutionContext{
		ChatID:  c.Chat.ID,
		Kind:    kind,
		TopicID: c.TopicID(),
		Source:  c.Source,
		Feature: c.Command,
		Actor:   actor,
	}, true
}

// IsManagerGroup reports whether the current command is executing in a basic
// group or supergroup suitable for Assistant manager features.
func (c *Context) IsManagerGroup() bool {
	return c != nil && c.Chat != nil && c.Chat.IsManagerGroup()
}
