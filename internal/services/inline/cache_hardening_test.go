package inline

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/ui"
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

func TestCache_DefensiveCopyMarkup(t *testing.T) {
	c := NewCache(time.Minute)
	data := []byte("action")
	markup := ui.NewMarkup(ui.ButtonRow{ui.NewCallbackButton("Run", data)})
	results := []InlineResult{{ID: "res-1", Markup: &markup}}
	c.SetScoped("markup", results, time.Minute)
	data[0] = 'X'
	results[0].Markup.Rows[0][0].Text = "Changed"
	cached, ok := c.GetScoped("markup")
	if !ok {
		t.Fatal("expected cached markup")
	}
	if cached[0].Markup.Rows[0][0].Text != "Run" || string(cached[0].Markup.Rows[0][0].Data) != "action" {
		t.Fatalf("input mutation reached cache: %+v", cached[0].Markup.Rows[0][0])
	}
	cached[0].Markup.Rows[0][0].Data[0] = 'Y'
	again, _ := c.GetScoped("markup")
	if string(again[0].Markup.Rows[0][0].Data) != "action" {
		t.Fatal("returned markup mutation reached cache")
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

func TestCache_DeadlineDrivenExpiryReclaimsRetainedBytes(t *testing.T) {
	cache := NewCache(time.Minute)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := cache.Start(ctx); err != nil {
		t.Fatalf("start cache: %v", err)
	}
	t.Cleanup(func() {
		stopCtx, stopCancel := context.WithTimeout(context.Background(), time.Second)
		defer stopCancel()
		_ = cache.Stop(stopCtx)
	})

	cache.SetScoped("expires", []InlineResult{{ID: "1", Text: "payload"}}, 20*time.Millisecond)
	if cache.RetainedBytes() == 0 {
		t.Fatal("expected retained bytes before expiry")
	}

	deadline := time.Now().Add(500 * time.Millisecond)
	for cache.Len() != 0 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	if cache.Len() != 0 {
		t.Fatalf("expired cache entry was not reclaimed; len=%d", cache.Len())
	}
	if got := cache.RetainedBytes(); got != 0 {
		t.Fatalf("expired cache retained %d bytes", got)
	}
}
