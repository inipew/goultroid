package storage

import (
	"bytes"
	"context"
	"testing"
	"time"
)

func TestStoragePreservesMediaIdentityMetadata(t *testing.T) {
	stores := map[string]Storage{
		"memory": NewMemoryStorage(),
	}
	fileStore, err := NewFileStorage(t.TempDir(), 16*1024*1024)
	if err != nil {
		t.Fatal(err)
	}
	stores["file"] = fileStore

	for name, store := range stores {
		t.Run(name, func(t *testing.T) {
			asset, err := store.Put(context.Background(), bytes.NewBufferString("media"), Metadata{
				Name:      "training-season.mp3",
				MIME:      "audio/mpeg",
				Title:     "Training Season",
				Performer: "Dua Lipa",
				Duration:  4*time.Minute + 12*time.Second,
			})
			if err != nil {
				t.Fatal(err)
			}
			stat, err := store.Stat(context.Background(), asset.ID)
			if err != nil {
				t.Fatal(err)
			}
			if stat.Title != "Training Season" || stat.Performer != "Dua Lipa" || stat.Duration != 4*time.Minute+12*time.Second {
				t.Fatalf("metadata round-trip = %+v", stat)
			}
		})
	}
}
