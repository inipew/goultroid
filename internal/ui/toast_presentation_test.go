package ui

import (
	"errors"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
)

func TestPresentUserErrorInteractionSemantics(t *testing.T) {
	tests := []struct {
		name      string
		err       error
		contains  string
		wantAlert bool
	}{
		{name: "binding", err: rootinteraction.ErrBindingMismatch, contains: "only be used by the user", wantAlert: true},
		{name: "expired", err: rootinteraction.ErrExpired, contains: "Interaction expired"},
		{name: "input expired", err: rootinteraction.ErrInputExpired, contains: "Input session expired"},
		{name: "revision", err: rootinteraction.ErrRevisionConflict, contains: "changed while this action was pending"},
		{name: "input busy", err: rootinteraction.ErrInputBusy, contains: "Another input session"},
		{name: "capacity", err: rootinteraction.ErrCapacity, contains: "Too many active interactions"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := PresentUserError(tc.err)
			if !strings.Contains(got.Text, tc.contains) || got.Alert != tc.wantAlert {
				t.Fatalf("PresentUserError(%v)=%+v", tc.err, got)
			}
		})
	}
}

func TestPresentUserErrorUnifiesInteractionAndCoreErrors(t *testing.T) {
	rate := PresentUserError(core.ErrRateLimited)
	if rate.Alert || !strings.Contains(rate.Text, "Too many requests") {
		t.Fatalf("core rate limit=%+v", rate)
	}

	internal := PresentUserError(errors.New("sqlite password=/tmp/secret"))
	if internal.Alert || internal.Text != "❌ Action failed. Please try again." {
		t.Fatalf("internal error leaked=%+v", internal)
	}
}
