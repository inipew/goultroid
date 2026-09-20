package core

import (
	"context"
	"testing"
)

func TestHeldResourceContextIsImmutableAndScoped(t *testing.T) {
	base := context.Background()
	download := WithHeldResource(base, "download")
	media := WithHeldResource(download, "media")

	if HasHeldResource(base, "download") {
		t.Fatal("base context unexpectedly inherited held resource")
	}
	if !HasHeldResource(download, "download") {
		t.Fatal("download lease marker missing")
	}
	if HasHeldResource(download, "media") {
		t.Fatal("child resource marker mutated parent context")
	}
	if !HasHeldResource(media, "download") || !HasHeldResource(media, "media") {
		t.Fatal("nested held resources were not preserved")
	}
	if HasHeldResource(media, "process") {
		t.Fatal("unexpected process lease marker")
	}
}

func TestWithHeldResourceIgnoresBlankName(t *testing.T) {
	ctx := context.Background()
	if got := WithHeldResource(ctx, "   "); got != ctx {
		t.Fatal("blank resource name should not wrap context")
	}
}
