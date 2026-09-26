package presentation

import "testing"

func TestP1ACanonicalButtonVocabulary(t *testing.T) {
	tests := []struct {
		role ButtonRole
		want string
	}{
		{ButtonRoleAction, "Action"},
		{ButtonRoleBack, "🔙 Back"},
		{ButtonRoleHome, "🏠 Home"},
		{ButtonRoleClose, "❌ Close"},
		{ButtonRoleConfirm, "✅ Confirm"},
		{ButtonRoleCancel, "❌ Cancel"},
		{ButtonRoleEdit, "✏️ Edit"},
		{ButtonRoleOpen, "🔗 Open"},
		{ButtonRoleHelp, "🔍 Help"},
		{ButtonRolePrevious, "◀ Prev"},
		{ButtonRoleNext, "Next ▶"},
		{ButtonRoleSearch, "🔍 Search"},
		{ButtonRoleRefresh, "🔄 Refresh"},
		{ButtonRoleSave, "💾 Save"},
	}
	for _, tc := range tests {
		if got := ButtonLabel(tc.role); got != tc.want {
			t.Fatalf("ButtonLabel(%d)=%q, want %q", tc.role, got, tc.want)
		}
	}
	if got := ButtonLabel(ButtonRoleUnspecified); got != "" {
		t.Fatalf("unspecified label=%q, want empty", got)
	}
}

func TestP1ACanonicalButtonBuildersPreserveTypedSemantics(t *testing.T) {
	action := RoleActionButton(ButtonRoleClose, "close")
	if action.Type != ButtonAction || action.Text != "❌ Close" || action.ActionID != "close" {
		t.Fatalf("action=%+v", action)
	}
	if err := action.Validate(); err != nil {
		t.Fatalf("action Validate()=%v", err)
	}

	url := RoleURLButton(ButtonRoleOpen, "https://example.com")
	if url.Type != ButtonURL || url.Text != "🔗 Open" || url.URL != "https://example.com" {
		t.Fatalf("url=%+v", url)
	}
	if err := url.Validate(); err != nil {
		t.Fatalf("url Validate()=%v", err)
	}

	inline := RoleSwitchInlineButton(ButtonRoleHelp, "help", true)
	if inline.Type != ButtonSwitchInline || inline.Text != "🔍 Help" || inline.InlineQuery != "help" || !inline.SamePeer {
		t.Fatalf("inline=%+v", inline)
	}
	if err := inline.Validate(); err != nil {
		t.Fatalf("inline Validate()=%v", err)
	}
}

func TestP1ASemanticWarningAndInformationRendering(t *testing.T) {
	if got := Warning("Low disk").Render(); got != "⚠️ <b>Warning:</b> Low disk" {
		t.Fatalf("Warning Render()=%q", got)
	}
	if got := Information("Ready").Render(); got != "ℹ️ <b>Info:</b> Ready" {
		t.Fatalf("Information Render()=%q", got)
	}
}
