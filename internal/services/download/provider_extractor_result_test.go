package download

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseExtractorResultUsesAfterMoveMetadata(t *testing.T) {
	stdout := "noise\n" +
		extractorResultPrefix +
		"\"/tmp/media/song.mp3\"\t\"Training Season\"\t\"Dua Lipa\"\t\"Dua Lipa\"\t241.5\tnull\tnull\t\"mp3\"\n"

	result := parseExtractorResult(stdout)
	if result.Path != "/tmp/media/song.mp3" || result.Title != "Training Season" || result.Artist != "Dua Lipa" {
		t.Fatalf("result = %+v", result)
	}
	if result.Duration != 241.5 || result.Ext != "mp3" {
		t.Fatalf("duration/ext = %v/%q", result.Duration, result.Ext)
	}
}

func TestResolveExtractorOutputPrefersReportedFinalPathAndIgnoresSidecars(t *testing.T) {
	dir := t.TempDir()
	sidecar := filepath.Join(dir, "metadata.json")
	if err := os.WriteFile(sidecar, []byte("larger sidecar content"), 0o600); err != nil {
		t.Fatal(err)
	}
	media := filepath.Join(dir, "final.mp4")
	if err := os.WriteFile(media, []byte("video"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := resolveExtractorOutput(dir, media)
	if err != nil {
		t.Fatal(err)
	}
	if got != media {
		t.Fatalf("output = %q, want %q", got, media)
	}

	got, err = resolveExtractorOutput(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if got != media {
		t.Fatalf("fallback output = %q, want %q", got, media)
	}
}

func TestExtractorStorageMetadataCarriesTelegramAudioIdentity(t *testing.T) {
	meta := extractorStorageMetadata(
		"/tmp/Dua Lipa - Training Season.mp3",
		extractorResultMetadata{
			Title:    "Training Season",
			Artist:   "Dua Lipa",
			Uploader: "Dua Lipa",
			Duration: 241.5,
		},
		MediaModeAudio,
	)
	if meta.Name != "Dua Lipa - Training Season.mp3" || meta.MIME != "audio/mpeg" {
		t.Fatalf("name/mime = %q/%q", meta.Name, meta.MIME)
	}
	if meta.Title != "Training Season" || meta.Performer != "Dua Lipa" {
		t.Fatalf("title/performer = %q/%q", meta.Title, meta.Performer)
	}
	if meta.Duration != 241500*time.Millisecond {
		t.Fatalf("duration = %v", meta.Duration)
	}
}
