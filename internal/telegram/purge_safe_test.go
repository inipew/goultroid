package telegram

import (
	"testing"

	"github.com/gotd/td/tg"
)

func TestSafePurgeQueryBoundsIncludeRepliedMessage(t *testing.T) {
	minID, maxID := safePurgeQueryBounds(100, 104)
	if minID != 99 {
		t.Fatalf("query min ID = %d, want 99 so reply ID 100 is discoverable", minID)
	}
	if maxID != 103 {
		t.Fatalf("query max ID = %d, want 103 so command ID 104 is excluded", maxID)
	}
}

func TestSafePurgeCollectIDsIncludesRepliedMessage(t *testing.T) {
	ids := make(map[int]struct{})
	messages := []tg.MessageClass{
		&tg.Message{ID: 100},
		&tg.Message{ID: 101},
		&tg.Message{ID: 102},
		&tg.Message{ID: 103},
		&tg.Message{ID: 104},
	}

	lowest, newIDs := collectSafePurgeIDs(messages, 100, 103, 0, ids, 105)
	if _, ok := ids[100]; !ok {
		t.Fatalf("replied message ID 100 is missing from deletion set: %v", ids)
	}
	if _, ok := ids[103]; !ok {
		t.Fatalf("latest purgeable message ID 103 is missing from deletion set: %v", ids)
	}
	if _, ok := ids[104]; ok {
		t.Fatalf("command message ID 104 must not enter deletion set: %v", ids)
	}
	if newIDs != 4 {
		t.Fatalf("new IDs = %d, want 4", newIDs)
	}
	if lowest != 100 {
		t.Fatalf("lowest ID = %d, want 100", lowest)
	}
}

func TestSafePurgeCollectIDsSkipsForumTopicRoot(t *testing.T) {
	ids := make(map[int]struct{})
	messages := []tg.MessageClass{
		&tg.Message{ID: 200}, // topic root
		&tg.Message{ID: 201},
		&tg.Message{ID: 202},
	}

	_, newIDs := collectSafePurgeIDs(messages, 200, 202, 200, ids, 203)
	if _, ok := ids[200]; ok {
		t.Fatalf("forum topic root must not be deleted: %v", ids)
	}
	if _, ok := ids[201]; !ok {
		t.Fatalf("topic reply ID 201 is missing: %v", ids)
	}
	if _, ok := ids[202]; !ok {
		t.Fatalf("topic reply ID 202 is missing: %v", ids)
	}
	if newIDs != 2 {
		t.Fatalf("new IDs = %d, want 2", newIDs)
	}
}
