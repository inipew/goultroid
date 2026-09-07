package execution_test

import (
	"context"
	"testing"

	"github.com/inipew/goultroid/internal/execution"
)

func TestSourceAndMask(t *testing.T) {
	mask := execution.SurfaceUserbot | execution.SurfaceAssistant

	if !mask.Supports(execution.SourceUserbot) {
		t.Fatalf("expected mask to support Userbot")
	}
	if !mask.Supports(execution.SourceAssistant) {
		t.Fatalf("expected mask to support Assistant")
	}
	if mask.Supports(execution.SourceInline) {
		t.Fatalf("expected mask to NOT support Inline")
	}

	allMask := execution.SurfaceAll
	if !allMask.Supports(execution.SourceInline) {
		t.Fatalf("expected SurfaceAll to support Inline")
	}
}

func TestActor_Privileges(t *testing.T) {
	owner := execution.NewActor(123, 456, true, false)
	sudo := execution.NewActor(234, 456, false, true)
	regular := execution.NewActor(345, 456, false, false)

	// Require Owner
	if !owner.CanExecute(true, false) {
		t.Fatalf("owner must pass owner check")
	}
	if sudo.CanExecute(true, false) {
		t.Fatalf("sudo must fail owner check")
	}
	if regular.CanExecute(true, false) {
		t.Fatalf("regular must fail owner check")
	}

	// Require Sudo
	if !owner.CanExecute(false, true) {
		t.Fatalf("owner must pass sudo check")
	}
	if !sudo.CanExecute(false, true) {
		t.Fatalf("sudo must pass sudo check")
	}
	if regular.CanExecute(false, true) {
		t.Fatalf("regular must fail sudo check")
	}
}

func TestExecutionContext_ResponseAdapters(t *testing.T) {
	ctx := context.Background()
	actor := execution.NewActor(100, 200, true, false)
	execCtx := execution.NewExecutionContext(ctx, execution.SourceAssistant, actor, 200, 10, "/test hello", []string{"hello"})

	var lastReply string
	var lastEdit string
	var lastToast string
	var lastAlert bool

	execCtx.SetHandlers(
		func(text string) error {
			lastReply = text
			return nil
		},
		func(text string) error {
			lastEdit = text
			return nil
		},
		func(text string, alert bool) error {
			lastToast = text
			lastAlert = alert
			return nil
		},
	)

	if err := execCtx.Reply("reply message"); err != nil {
		t.Fatalf("unexpected reply error: %v", err)
	}
	if lastReply != "reply message" {
		t.Fatalf("expected reply captured, got %q", lastReply)
	}

	if err := execCtx.Edit("edit message"); err != nil {
		t.Fatalf("unexpected edit error: %v", err)
	}
	if lastEdit != "edit message" {
		t.Fatalf("expected edit captured, got %q", lastEdit)
	}

	if err := execCtx.SendToast("toast message", true); err != nil {
		t.Fatalf("unexpected toast error: %v", err)
	}
	if lastToast != "toast message" || !lastAlert {
		t.Fatalf("expected toast captured, got text=%q alert=%v", lastToast, lastAlert)
	}
}
