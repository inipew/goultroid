package ui

import (
	"testing"

	"github.com/inipew/goultroid/internal/presentation"
)

func TestP1ALegacyButtonHelpersUseCanonicalVocabulary(t *testing.T) {
	if got := NewCloseRow([]byte("close"))[0].Text; got != presentation.ButtonLabel(presentation.ButtonRoleClose) {
		t.Fatalf("close label=%q", got)
	}
	if got := NewBackRow([]byte("back"))[0].Text; got != presentation.ButtonLabel(presentation.ButtonRoleBack) {
		t.Fatalf("back label=%q", got)
	}

	confirm := NewConfirmCancelRow([]byte("yes"), []byte("no"))
	if confirm[0].Text != presentation.ButtonLabel(presentation.ButtonRoleConfirm) ||
		confirm[1].Text != presentation.ButtonLabel(presentation.ButtonRoleCancel) {
		t.Fatalf("confirm/cancel=%+v", confirm)
	}

	pages := NewPaginationRow([]byte("prev"), []byte("next"), 2, 3)
	if pages[0].Text != presentation.ButtonLabel(presentation.ButtonRolePrevious) ||
		pages[2].Text != presentation.ButtonLabel(presentation.ButtonRoleNext) {
		t.Fatalf("pagination=%+v", pages)
	}

	help := NewHelpSwitchRow("")
	if len(help) != 1 || help[0].Text != presentation.ButtonLabel(presentation.ButtonRoleHelp) {
		t.Fatalf("help row=%+v", help)
	}
}

func TestP1ALegacyAlertsDelegateToCanonicalPresentation(t *testing.T) {
	tests := []struct {
		name string
		got  string
		want string
	}{
		{"success", Success("Saved"), presentation.Success("Saved").Render()},
		{"warning", Warning("Careful"), presentation.Warning("Careful").Render()},
		{"error", Error("Failed"), presentation.Error("Failed").Render()},
		{"processing", Processing("Working"), presentation.Progress("Working").Render()},
		{"information", Information("Ready"), presentation.Information("Ready").Render()},
	}
	for _, tc := range tests {
		if tc.got != tc.want {
			t.Fatalf("%s=%q, want %q", tc.name, tc.got, tc.want)
		}
	}
}
