package download

import (
	"errors"
	"reflect"
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestExtractorSelectionArgsAreBoundedAndTyped(t *testing.T) {
	tests := []struct {
		name string
		opts DownloadOptions
		want []string
	}{
		{name: "legacy default", opts: DownloadOptions{}, want: nil},
		{name: "audio m4a", opts: DownloadOptions{Mode: MediaModeAudio, Format: MediaFormatM4A}, want: []string{"-f", "bestaudio/best", "-x", "--audio-format", "m4a"}},
		{name: "audio mp3", opts: DownloadOptions{Mode: MediaModeAudio, Format: MediaFormatMP3}, want: []string{"-f", "bestaudio/best", "-x", "--audio-format", "mp3"}},
		{name: "audio opus", opts: DownloadOptions{Mode: MediaModeAudio, Format: MediaFormatOpus}, want: []string{"-f", "bestaudio/best", "-x", "--audio-format", "opus"}},
		{name: "video mp4", opts: DownloadOptions{Mode: MediaModeVideo, Format: MediaFormatMP4}, want: []string{"-f", "bestvideo[ext=mp4]+bestaudio[ext=m4a]/best[ext=mp4]", "--merge-output-format", "mp4"}},
		{name: "video mp4 720p", opts: DownloadOptions{Mode: MediaModeVideo, Format: MediaFormatMP4, MaxHeight: 720}, want: []string{"-f", "bestvideo[height<=720][ext=mp4]+bestaudio[ext=m4a]/best[height<=720][ext=mp4]", "--merge-output-format", "mp4"}},
		{name: "video best", opts: DownloadOptions{Mode: MediaModeVideo, Format: MediaFormatBest}, want: []string{"-f", "bestvideo+bestaudio/best"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := extractorSelectionArgs(tt.opts)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("args=%q, want %q", got, tt.want)
			}
		})
	}
}

func TestExtractorSelectionArgsRejectInvalidCombinations(t *testing.T) {
	for _, opts := range []DownloadOptions{
		{Format: MediaFormatMP4},
		{Mode: MediaModeAudio, Format: MediaFormatMP4},
		{Mode: MediaModeVideo, Format: MediaFormatMP3},
		{Mode: MediaModeAudio, Format: MediaFormatMP3, MaxHeight: 720},
		{Mode: MediaModeVideo, Format: MediaFormatMP4, MaxHeight: 721},
		{Mode: MediaModeVideo, Format: MediaFormatBest, MaxHeight: 720},
		{Mode: MediaMode("other"), Format: MediaFormatBest},
	} {
		if _, err := extractorSelectionArgs(opts); !errors.Is(err, core.ErrInvalidArgs) {
			t.Fatalf("opts=%+v error=%v, want ErrInvalidArgs", opts, err)
		}
	}
}
