package downloader

import (
	"testing"

	"github.com/inipew/goultroid/internal/services/download"
)

func TestDLC_LegacyInteractiveStateFixturesDecodeCleanly(t *testing.T) {
	fixtures := []struct {
		name      string
		raw       string
		wantURL   string
		wantProv  string
		wantPhase interactivePhase
	}{
		{
			name:      "http_choose",
			raw:       `{"u":"https://example.com/file.zip","p":"http","s":"choose"}`,
			wantURL:   "https://example.com/file.zip",
			wantProv:  "http",
			wantPhase: phaseChoose,
		},
		{
			name:      "extractor_audio_format",
			raw:       `{"u":"https://youtube.com/watch?v=abc123","p":"extractor","s":"audio_format","m":"audio","f":"mp3"}`,
			wantURL:   "https://youtube.com/watch?v=abc123",
			wantProv:  "extractor",
			wantPhase: phaseAudioFormat,
		},
		{
			name:      "extractor_video_quality",
			raw:       `{"u":"https://tiktok.com/@u/video/1","p":"extractor","s":"video_format","m":"video","h":720}`,
			wantURL:   "https://tiktok.com/@u/video/1",
			wantProv:  "extractor",
			wantPhase: phaseVideoFormat,
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			st, err := decodeInteractiveState([]byte(tc.raw))
			if err != nil {
				t.Fatalf("decodeInteractiveState() error = %v", err)
			}
			if st.URL != tc.wantURL || st.Provider != tc.wantProv || st.Phase != tc.wantPhase {
				t.Fatalf("decoded = %+v, want URL=%q Prov=%q Phase=%q", st, tc.wantURL, tc.wantProv, tc.wantPhase)
			}
		})
	}
}

func TestDLC_PrepareInteractiveURLStatePreservesGeneralExtractorAndHTTP(t *testing.T) {
	reg := download.NewRegistry(
		download.NewExtractorProvider(nil, 500*1024*1024),
		download.NewDirectHTTPProvider(0, 500*1024*1024),
	)
	p := New(reg)

	cases := []struct {
		url      string
		wantProv string
	}{
		{"https://example.com/archive.tar.gz", "http"},
		{"https://www.tiktok.com/@user/video/12345", "extractor"},
		{"https://www.youtube.com/watch?v=dQw4w9WgXcQ", "extractor"},
	}

	for _, tc := range cases {
		st, encoded, prov, err := p.prepareInteractiveURLState(tc.url, "en")
		if err != nil {
			t.Fatalf("prepareInteractiveURLState(%q) error = %v", tc.url, err)
		}
		if prov.Name() != tc.wantProv || st.Provider != tc.wantProv || st.Phase != phaseChoose || len(encoded) == 0 {
			t.Fatalf("prepareInteractiveURLState(%q) = (prov=%s, state=%+v, len=%d)", tc.url, prov.Name(), st, len(encoded))
		}
	}
}

func TestDLC_FeatureSpecAndActionIDsRemainUnchanged(t *testing.T) {
	p := New()
	spec := p.FeatureSpec()
	if spec.ID != "downloader" {
		t.Fatalf("FeatureSpec().ID = %q, want downloader", spec.ID)
	}
	wantIDs := []string{
		"interactive",
		"select_audio",
		"select_video",
		"format_m4a",
		"format_mp3",
		"format_opus",
		"format_mp4",
		"format_best",
		"video_360",
		"video_480",
		"video_720",
		"video_1080",
		"video_1440",
		"video_2160",
		"download_file",
		"back",
		"retry",
		"cancel",
	}
	if len(spec.Interactions) != len(wantIDs) {
		t.Fatalf("Interactions len = %d, want %d", len(spec.Interactions), len(wantIDs))
	}
	gotSet := make(map[string]struct{}, len(spec.Interactions))
	for _, inter := range spec.Interactions {
		gotSet[inter.ID] = struct{}{}
	}
	for _, want := range wantIDs {
		if _, ok := gotSet[want]; !ok {
			t.Fatalf("missing expected interaction ID %q in FeatureSpec", want)
		}
	}
}
