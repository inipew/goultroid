package download

import (
	"context"
	"strings"
	"testing"

	"github.com/inipew/goultroid/internal/services/process"
	"github.com/inipew/goultroid/internal/tasks"
)

type extractorProbeFakeRunner struct {
	requests []process.Request
}

func (r *extractorProbeFakeRunner) Run(_ context.Context, req process.Request) (*process.Result, error) {
	r.requests = append(r.requests, req)
	return &process.Result{
		Stdout: `{
			"title":"Training Season",
			"artist":"Dua Lipa",
			"uploader":"Dua Lipa",
			"duration":245,
			"formats":[
				{"ext":"m4a","vcodec":"none","acodec":"mp4a.40.2","filesize":5000000},
				{"ext":"mp4","vcodec":"avc1","acodec":"none","height":360,"tbr":500,"filesize":10000000},
				{"ext":"mp4","vcodec":"avc1","acodec":"none","height":720,"tbr":1500,"filesize_approx":25000000},
				{"ext":"webm","vcodec":"vp9","acodec":"none","height":1080,"tbr":2500,"filesize":50000000},
				{"ext":"mp4","vcodec":"avc1","acodec":"none","height":1080,"tbr":2200,"filesize":45000000}
			]
		}`,
	}, nil
}

func TestExtractorProbeReturnsBoundedMP4Qualities(t *testing.T) {
	runner := &extractorProbeFakeRunner{}
	provider := NewExtractorProvider(runner, 500*1024*1024)
	provider.lookPath = func(string) (string, error) { return "/usr/bin/yt-dlp", nil }

	result, err := provider.Probe(
		tasks.WithHeldResource(context.Background(), "process"),
		"https://www.youtube.com/watch?v=abcdefghijk",
		ProbeOptions{},
	)
	if err != nil {
		t.Fatalf("Probe() error = %v", err)
	}
	if result.Title != "Training Season" || result.Performer != "Dua Lipa" || result.DurationSeconds != 245 {
		t.Fatalf("probe metadata = %+v", result)
	}
	if len(result.VideoQualities) != 3 {
		t.Fatalf("qualities = %+v, want 3", result.VideoQualities)
	}
	if result.VideoQualities[0].Height != 360 || result.VideoQualities[0].Size != 15000000 {
		t.Fatalf("360p quality = %+v", result.VideoQualities[0])
	}
	if result.VideoQualities[1].Height != 720 || result.VideoQualities[1].Size != 30000000 {
		t.Fatalf("720p quality = %+v", result.VideoQualities[1])
	}
	if result.VideoQualities[2].Height != 1080 || result.VideoQualities[2].Size != 50000000 {
		t.Fatalf("1080p quality = %+v", result.VideoQualities[2])
	}
	if len(runner.requests) != 1 {
		t.Fatalf("runner requests = %d, want 1", len(runner.requests))
	}
	args := strings.Join(runner.requests[0].Args, "\x00")
	for _, want := range []string{"--ignore-config", "--simulate", "--dump-single-json"} {
		if !strings.Contains(args, want) {
			t.Fatalf("probe args %q missing %q", runner.requests[0].Args, want)
		}
	}
}

func TestNormalizeExtractorProbeOmitsNonCanonicalOrNonMP4Heights(t *testing.T) {
	result := normalizeExtractorProbe(extractorProbePayload{
		Formats: []extractorProbeFormat{
			{Ext: "mp4", VCodec: "avc1", ACodec: "none", Height: 240, FileSize: 1},
			{Ext: "webm", VCodec: "vp9", ACodec: "none", Height: 2160, FileSize: 2},
			{Ext: "mp4", VCodec: "avc1", ACodec: "none", Height: 2160, FileSize: 3},
		},
	})
	if len(result.VideoQualities) != 1 || result.VideoQualities[0].Height != 2160 {
		t.Fatalf("qualities = %+v, want only canonical MP4 2160p", result.VideoQualities)
	}
}
