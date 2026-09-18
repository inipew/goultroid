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
			gid := int64(idx%4) + 1
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

func TestAlbumBuffer_HardBoundEvictsOldestLiveAlbum(t *testing.T) {
	buf := NewAlbumBuffer(time.Minute)
	buf.maxSize = 2

	buf.Add(&Message{ID: 1, GroupedID: 101})
	time.Sleep(time.Millisecond)
	buf.Add(&Message{ID: 2, GroupedID: 102})
	time.Sleep(time.Millisecond)
	buf.Add(&Message{ID: 3, GroupedID: 103})

	if got := buf.Len(); got != 2 {
		t.Fatalf("album count = %d, want hard bound 2", got)
	}
	if got := buf.Get(101); got != nil {
		t.Fatalf("oldest live album was not evicted: %+v", got)
	}
	if got := buf.Get(102); len(got) != 1 || got[0].ID != 2 {
		t.Fatalf("newer album 102 missing after eviction: %+v", got)
	}
	if got := buf.Get(103); len(got) != 1 || got[0].ID != 3 {
		t.Fatalf("new album 103 missing after eviction: %+v", got)
	}
}

func TestAlbumBuffer_GetExpiresEntryWithoutExplicitPrune(t *testing.T) {
	buf := NewAlbumBuffer(10 * time.Millisecond)
	buf.Add(&Message{ID: 1, GroupedID: 777})
	time.Sleep(20 * time.Millisecond)

	if got := buf.Get(777); got != nil {
		t.Fatalf("expired album returned from Get: %+v", got)
	}
	if got := buf.Len(); got != 0 {
		t.Fatalf("expired album retained after Get: len=%d", got)
	}
}
