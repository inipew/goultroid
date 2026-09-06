package core

import (
	"sync"
	"testing"
	"time"
)

func TestAlbumBuffer_AddAndGet(t *testing.T) {
	buf := NewAlbumBuffer(1 * time.Minute)

	// Adding nil or 0 GroupedID should be no-ops
	buf.Add(nil)
	buf.Add(&Message{ID: 1, GroupedID: 0})
	if buf.Len() != 0 {
		t.Errorf("expected 0 albums, got %d", buf.Len())
	}

	// Add 3 messages belonging to grouped album 12345
	buf.Add(&Message{ID: 300, GroupedID: 12345, Text: "photo 3"})
	buf.Add(&Message{ID: 100, GroupedID: 12345, Text: "photo 1"})
	buf.Add(&Message{ID: 200, GroupedID: 12345, Text: "photo 2"})

	// Duplicate message should be ignored
	buf.Add(&Message{ID: 100, GroupedID: 12345, Text: "photo 1 duplicate"})

	if buf.Len() != 1 {
		t.Fatalf("expected 1 album, got %d", buf.Len())
	}

	msgs := buf.Get(12345)
	if len(msgs) != 3 {
		t.Fatalf("expected 3 messages, got %d", len(msgs))
	}

	// Verify messages are sorted by ID ascending
	if msgs[0].ID != 100 || msgs[1].ID != 200 || msgs[2].ID != 300 {
		t.Errorf("expected sorted IDs [100, 200, 300], got [%d, %d, %d]", msgs[0].ID, msgs[1].ID, msgs[2].ID)
	}

	// Unknown album returns nil
	if buf.Get(99999) != nil {
		t.Errorf("expected nil for non-existent album")
	}
}

func TestAlbumBuffer_Prune(t *testing.T) {
	buf := NewAlbumBuffer(50 * time.Millisecond)

	buf.Add(&Message{ID: 1, GroupedID: 111})
	if buf.Len() != 1 {
		t.Fatalf("expected 1 album, got %d", buf.Len())
	}

	time.Sleep(70 * time.Millisecond)
	buf.Prune()

	if buf.Len() != 0 {
		t.Errorf("expected 0 albums after prune, got %d", buf.Len())
	}
}

func TestAlbumBuffer_Concurrency(t *testing.T) {
	buf := NewAlbumBuffer(1 * time.Minute)
	var wg sync.WaitGroup

	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()
			gid := int64(idx % 4) + 1
			buf.Add(&Message{ID: idx, GroupedID: gid})
			_ = buf.Get(gid)
			_ = buf.Len()
		}(i)
	}

	wg.Wait()
	if buf.Len() != 4 {
		t.Errorf("expected 4 albums, got %d", buf.Len())
	}
}
