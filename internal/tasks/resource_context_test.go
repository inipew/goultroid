package tasks

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

func TestWithHeldResourcesMarksPositiveRequirementsOnly(t *testing.T) {
	ctx := WithHeldResources(context.Background(), []ResourceRequirement{
		{Name: "download", Amount: 1},
		{Name: "media", Amount: 2},
		{Name: "ignored", Amount: 0},
	})
	if !HasHeldResource(ctx, "download") || !HasHeldResource(ctx, "media") {
		t.Fatal("positive resource requirements were not marked")
	}
	if HasHeldResource(ctx, "ignored") {
		t.Fatal("non-positive resource requirement was marked")
	}
}

func TestWithHeldResourceIgnoresBlankName(t *testing.T) {
	ctx := context.Background()
	if got := WithHeldResource(ctx, "   "); got != ctx {
		t.Fatal("blank resource name should not wrap context")
	}
}
