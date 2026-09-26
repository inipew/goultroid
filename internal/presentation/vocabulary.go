package presentation

// ButtonRole is the canonical product vocabulary for common UI actions.
// It describes intent only; callback/session ownership remains with the
// presentation surface that builds the button.
type ButtonRole uint8

const (
	ButtonRoleUnspecified ButtonRole = iota
	ButtonRoleAction
	ButtonRoleBack
	ButtonRoleHome
	ButtonRoleClose
	ButtonRoleConfirm
	ButtonRoleCancel
	ButtonRoleEdit
	ButtonRoleOpen
	ButtonRoleHelp
	ButtonRolePrevious
	ButtonRoleNext
	ButtonRoleSearch
	ButtonRoleRefresh
	ButtonRoleSave
)

// ButtonLabel returns the canonical English label for a common action role.
// Localized surfaces may use ActionButton with translated text while retaining
// the same action identity.
func ButtonLabel(role ButtonRole) string {
	switch role {
	case ButtonRoleAction:
		return "Action"
	case ButtonRoleBack:
		return "🔙 Back"
	case ButtonRoleHome:
		return "🏠 Home"
	case ButtonRoleClose:
		return "❌ Close"
	case ButtonRoleConfirm:
		return "✅ Confirm"
	case ButtonRoleCancel:
		return "❌ Cancel"
	case ButtonRoleEdit:
		return "✏️ Edit"
	case ButtonRoleOpen:
		return "🔗 Open"
	case ButtonRoleHelp:
		return "🔍 Help"
	case ButtonRolePrevious:
		return "◀ Prev"
	case ButtonRoleNext:
		return "Next ▶"
	case ButtonRoleSearch:
		return "🔍 Search"
	case ButtonRoleRefresh:
		return "🔄 Refresh"
	case ButtonRoleSave:
		return "💾 Save"
	default:
		return ""
	}
}

// ActionButton constructs an a2/inline action button with caller-owned text.
func ActionButton(text, actionID string) Button {
	return Button{Type: ButtonAction, Text: text, ActionID: actionID}
}

// RoleActionButton constructs an action button using the canonical role label.
func RoleActionButton(role ButtonRole, actionID string) Button {
	return ActionButton(ButtonLabel(role), actionID)
}

// URLButton constructs a stateless HTTP/HTTPS button.
func URLButton(text, url string) Button {
	return Button{Type: ButtonURL, Text: text, URL: url}
}

// RoleURLButton constructs a URL button using the canonical role label.
func RoleURLButton(role ButtonRole, url string) Button {
	return URLButton(ButtonLabel(role), url)
}

// SwitchInlineButton constructs a stateless switch-inline button.
func SwitchInlineButton(text, query string, samePeer bool) Button {
	return Button{
		Type:        ButtonSwitchInline,
		Text:        text,
		InlineQuery: query,
		SamePeer:    samePeer,
	}
}

// RoleSwitchInlineButton constructs a switch-inline button with a canonical role label.
func RoleSwitchInlineButton(role ButtonRole, query string, samePeer bool) Button {
	return SwitchInlineButton(ButtonLabel(role), query, samePeer)
}

// RowOf keeps row construction concise while preserving the existing View model.
func RowOf(buttons ...Button) Row {
	return Row(buttons)
}
