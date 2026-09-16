package inline

import (
	"strings"
	"testing"
	"time"
)

func TestCache_DefensiveCopy(t *testing.T) {
	c := NewCache(time.Minute)
	results := []InlineResult{
		{ID: "res-1", Title: "Original Title"},
	}

	c.SetScoped("query:key", results, time.Minute)

	// Mutate caller slice
	results[0].Title = "Mutated Title"

	cached, ok := c.GetScoped("query:key")
	if !ok || len(cached) != 1 {
		t.Fatalf("expected cached entry")
	}
	if cached[0].Title != "Original Title" {
		t.Fatalf("expected 'Original Title', got '%s' (cache corrupted by input mutation)", cached[0].Title)
	}

	// Mutate returned slice
	cached[0].Title = "Mutated Return"

	cached2, ok := c.GetScoped("query:key")
	if !ok || len(cached2) != 1 {
		t.Fatalf("expected cached entry on second get")
	}
	if cached2[0].Title != "Original Title" {
		t.Fatalf("expected 'Original Title', got '%s' (cache corrupted by return slice mutation)", cached2[0].Title)
	}
}

func TestCache_RetainedBytesTrackingAndEviction(t *testing.T) {
	c := NewCache(time.Minute)
	results := []InlineResult{
		{ID: "res-1", Title: "Title 1", Description: "Desc 1", Text: "Some content"},
	}

	c.SetScoped("k1", results, time.Minute)
	initialBytes := c.RetainedBytes()
	if initialBytes <= 0 {
		t.Fatalf("expected retainedBytes > 0, got %d", initialBytes)
	}

	c.Delete("k1")
	if c.RetainedBytes() != 0 {
		t.Fatalf("expected retainedBytes == 0 after delete, got %d", c.RetainedBytes())
	}
}

func TestCache_MaxSingleEntrySize(t *testing.T) {
	c := NewCache(time.Minute)
	// Create an enormous result that exceeds 256KB
	hugeText := strings.Repeat("A", 300*1024)
	results := []InlineResult{
		{ID: "huge", Text: hugeText},
	}

	c.SetScoped("huge:key", results, time.Minute)
	if c.Len() != 0 {
		t.Fatalf("expected huge entry to be rejected, got %d entries", c.Len())
	}
}
