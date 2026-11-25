package downloader

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/internal/tasks"
)

type dlDTestProvider struct {
	name            string
	matchFn         func(string) bool
	searchResults   []download.SearchResult
	searchErr       error
	searchCalls     int
	lastQuery       string
	downloadCalls   int
	lastURL         string
	downloadAsset   *storage.Asset
	downloadErr     error
	createdAssetIDs []string
}

func (p *dlDTestProvider) Name() string { return p.name }

func (p *dlDTestProvider) Match(rawURL string) bool {
	if p.matchFn != nil {
		return p.matchFn(rawURL)
	}
	return false
}

func (p *dlDTestProvider) Download(ctx context.Context, rawURL string, store storage.Storage, _ download.DownloadOptions) (*storage.Asset, error) {
	p.downloadCalls++
	p.lastURL = rawURL
	if p.downloadErr != nil {
		return nil, p.downloadErr
	}
	if p.downloadAsset != nil {
		return p.downloadAsset, nil
	}
	if store == nil {
		store = storage.NewMemoryStorage()
	}
	asset, err := store.Put(ctx, strings.NewReader("video-bytes"), storage.Metadata{
		Name: "video.mp4",
		MIME: "video/mp4",
	})
	if err == nil && asset != nil {
		p.createdAssetIDs = append(p.createdAssetIDs, asset.ID)
	}
	return asset, err
}

func (p *dlDTestProvider) Search(_ context.Context, query string, _ download.SearchOptions) ([]download.SearchResult, error) {
	p.searchCalls++
	p.lastQuery = query
	return p.searchResults, p.searchErr
}

func newDLDTestContext(tgSvc core.TelegramServicer, args ...string) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Svc:     tgSvc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
		Args:    args,
	}
}

func TestYouTubeFeature_CommandsAndSpecPreserved(t *testing.T) {
	p := New()
	cmds := p.Commands()
	byName := make(map[string]core.Command, len(cmds))
	for _, cmd := range cmds {
		byName[cmd.Name] = cmd
	}

	dlCmd, ok := byName["download"]
	if !ok {
		t.Fatal("missing download command")
	}
	if len(dlCmd.Aliases) != 1 || dlCmd.Aliases[0] != "dl" {
		t.Fatalf("download aliases = %v, want [dl]", dlCmd.Aliases)
	}

	ytCmd, ok := byName["yt"]
	if !ok {
		t.Fatal("missing yt command")
	}
	if ytCmd.Permission != core.PermissionSudo {
		t.Fatalf("yt permission = %v, want PermissionSudo", ytCmd.Permission)
	}
	if len(ytCmd.Resources) != 0 {
		t.Fatalf("yt command must not reserve heavy resources directly: %+v", ytCmd.Resources)
	}
	if ytCmd.Handler == nil {
		t.Fatal("yt command handler is nil")
	}

	if spec := p.FeatureSpec(); spec.ID != "downloader" {
		t.Fatalf("FeatureSpec().ID = %q, want downloader", spec.ID)
	}
	if got := len(p.InlineBindings()); got != 1 {
		t.Fatalf("InlineBindings() = %d, want 1", got)
	}
}

func TestYouTubeFeature_StrictURLValidation(t *testing.T) {
	valid := []string{
		"https://youtube.com/watch?v=dQw4w9WgXcQ",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ",
		"http://www.youtube.com./watch?v=dQw4w9WgXcQ",
	}
	for _, raw := range valid {
		normalized, err := validateYouTubeURL(raw)
		if err != nil || normalized == "" {
			t.Fatalf("validateYouTubeURL(%q) = (%q, %v), want valid", raw, normalized, err)
		}
	}

	invalid := []string{
		"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be.evil.example/dQw4w9WgXcQ",
		"https://www.tiktok.com/@user/video/12345",
		"https://www.instagram.com/reel/abc123/",
		"ftp://youtube.com/watch?v=dQw4w9WgXcQ",
		"javascript://youtube.com/%0aalert(1)",
		"www.tiktok.com/video",
		"tiktok.com/video",
		"youtube.com",
		"youtube.com?list=PL123",
		"//youtube.com/watch?v=dQw4w9WgXcQ",
		"youtu.be",
		"tiktok.com?foo=1",
		"youtube.com:443",
		"youtube.com:443/watch?v=dQw4w9WgXcQ",
		"tiktok.com:8080/video",
		"youtube.com:abc",
		"youtube.com:99999",
		"youtube.com:",
	}
	for _, raw := range invalid {
		if !looksLikeURL(raw) {
			t.Fatalf("looksLikeURL(%q) = false, want true", raw)
		}
		if normalized, err := validateYouTubeURL(raw); err == nil {
			t.Fatalf("validateYouTubeURL(%q) = (%q, nil), want error", raw, normalized)
		}
	}

	for _, query := range []string{"lofi hip hop", "ac/dc", "v1.2"} {
		if looksLikeURL(query) {
			t.Fatalf("looksLikeURL(%q) = true, want false for search query", query)
		}
	}
}

func TestYouTubeFeature_CommandNoArgsShowsUsage(t *testing.T) {
	provider := &dlDTestProvider{name: "extractor"}
	taskClient := &youtubeSearchTaskClient{}
	p := New(download.NewRegistry(provider), taskClient)
	tgSvc := &mockTelegramService{}

	if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc)); err != nil {
		t.Fatalf("handleYouTubeCommand() error = %v", err)
	}
	if provider.searchCalls != 0 || len(taskClient.specs) != 0 {
		t.Fatalf("empty .yt unexpectedly triggered search=%d tasks=%d", provider.searchCalls, len(taskClient.specs))
	}
	if !strings.Contains(tgSvc.lastEdited, "Usage:") || !strings.Contains(tgSvc.lastEdited, ".yt") {
		t.Fatalf("unexpected usage text: %q", tgSvc.lastEdited)
	}
}

func TestYouTubeFeature_CommandNonYouTubeURLRejectedAndNotSearched(t *testing.T) {
	rejectedURLs := []string{
		"https://www.tiktok.com/@user/video/12345",
		"https://youtube.com.evil.example/watch?v=dQw4w9WgXcQ",
		"https://notyoutube.com/watch?v=dQw4w9WgXcQ",
		"https://youtu.be.evil.example/dQw4w9WgXcQ",
		"ftp://youtube.com/watch?v=dQw4w9WgXcQ",
		"youtube.com",
		"youtube.com?list=PL123",
		"//youtube.com/watch?v=dQw4w9WgXcQ",
		"youtube.com:443",
		"youtube.com:443/watch?v=dQw4w9WgXcQ",
		"tiktok.com:8080/video",
		"youtube.com:abc",
		"youtube.com:99999",
	}

	for _, raw := range rejectedURLs {
		t.Run(raw, func(t *testing.T) {
			provider := &dlDTestProvider{name: "extractor"}
			taskClient := &youtubeSearchTaskClient{}
			p := New(download.NewRegistry(provider), taskClient)
			tgSvc := &mockTelegramService{}

			if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, raw)); err != nil {
				t.Fatalf("handleYouTubeCommand(%q) error = %v", raw, err)
			}
			if provider.searchCalls != 0 || provider.downloadCalls != 0 || len(taskClient.specs) != 0 {
				t.Fatalf("rejected URL triggered work: search=%d download=%d tasks=%d", provider.searchCalls, provider.downloadCalls, len(taskClient.specs))
			}
			if !strings.Contains(tgSvc.lastEdited, "Invalid or non-YouTube URL") || !strings.Contains(tgSvc.lastEdited, ".dl") {
				t.Fatalf("unexpected rejection message: %q", tgSvc.lastEdited)
			}
		})
	}
}

func TestYouTubeFeature_CommandQuerySearchAndFiltering(t *testing.T) {
	provider := &dlDTestProvider{
		name: "extractor",
		searchResults: []download.SearchResult{
			{
				Provider:        "extractor",
				Source:          "youtube",
				SourceID:        "yt123",
				URL:             "https://www.youtube.com/watch?v=yt123",
				Title:           "Lofi Beats <Live>",
				Channel:         "Chill Channel",
				DurationSeconds: 125,
				Views:           4200,
			},
			{
				Provider: "extractor",
				Source:   "soundcloud",
				SourceID: "sc999",
				URL:      "https://soundcloud.com/artist/track",
				Title:    "Non-YouTube Track",
			},
			{
				Provider: "extractor",
				Source:   "youtube",
				SourceID: "",
				URL:      "https://www.youtube.com/watch?v=missing",
				Title:    "Missing SourceID",
			},
		},
	}
	taskClient := &youtubeSearchTaskClient{}
	p := New(download.NewRegistry(provider), taskClient)
	tgSvc := &mockTelegramService{}

	if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, "lofi", "beats")); err != nil {
		t.Fatalf("handleYouTubeCommand() error = %v", err)
	}
	if provider.searchCalls != 1 || provider.lastQuery != "lofi beats" {
		t.Fatalf("search calls=%d query=%q", provider.searchCalls, provider.lastQuery)
	}
	for _, want := range []string{"Lofi Beats &lt;Live&gt;", "Chill Channel", "2:05", "4200 views", "https://www.youtube.com/watch?v=yt123"} {
		if !strings.Contains(tgSvc.lastEdited, want) {
			t.Fatalf("rendered output missing %q:\n%s", want, tgSvc.lastEdited)
		}
	}
	if strings.Contains(tgSvc.lastEdited, "Non-YouTube Track") || strings.Contains(tgSvc.lastEdited, "Missing SourceID") {
		t.Fatalf("rendered output leaked filtered results:\n%s", tgSvc.lastEdited)
	}

	// If all results are non-YouTube, report no usable results.
	provider.searchResults = []download.SearchResult{{
		Provider: "extractor",
		Source:   "vimeo",
		SourceID: "v1",
		URL:      "https://vimeo.com/1",
		Title:    "Vimeo Clip",
	}}
	if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, "vimeo")); err != nil {
		t.Fatalf("handleYouTubeCommand() error = %v", err)
	}
	if !strings.Contains(tgSvc.lastEdited, "No usable YouTube results found") {
		t.Fatalf("expected no usable results message, got %q", tgSvc.lastEdited)
	}
}

func TestYouTubeFeature_CommandQueryErrorPaths(t *testing.T) {
	cases := []struct {
		name      string
		args      []string
		searchErr error
		wantText  string
		wantCalls int
	}{
		{
			name:      "empty_results",
			args:      []string{"unknown"},
			searchErr: download.ErrSearchNoResults,
			wantText:  "No YouTube results found",
			wantCalls: 1,
		},
		{
			name:      "extractor_unavailable",
			args:      []string{"music"},
			searchErr: download.ErrExtractorUnavailable,
			wantText:  "YouTube search is unavailable",
			wantCalls: 1,
		},
		{
			name:      "timeout",
			args:      []string{"music"},
			searchErr: context.DeadlineExceeded,
			wantText:  "YouTube search timed out",
			wantCalls: 1,
		},
		{
			name:      "oversized_query",
			args:      []string{strings.Repeat("a", download.MaxSearchQueryBytes+1)},
			wantText:  "YouTube search query too long",
			wantCalls: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			provider := &dlDTestProvider{name: "extractor", searchErr: tc.searchErr}
			taskClient := &youtubeSearchTaskClient{}
			p := New(download.NewRegistry(provider), taskClient)
			tgSvc := &mockTelegramService{}

			if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, tc.args...)); err != nil {
				t.Fatalf("handleYouTubeCommand() error = %v", err)
			}
			if provider.searchCalls != tc.wantCalls {
				t.Fatalf("searchCalls = %d, want %d", provider.searchCalls, tc.wantCalls)
			}
			if !strings.Contains(tgSvc.lastEdited, tc.wantText) {
				t.Fatalf("status = %q, want substring %q", tgSvc.lastEdited, tc.wantText)
			}
		})
	}
}

func TestYouTubeFeature_CommandYouTubeURLUsesSharedPipelineAndDelivery(t *testing.T) {
	t.Run("renderer_unavailable_uses_native_default_download", func(t *testing.T) {
		engine := taskengine.NewEngine(taskengine.Config{
			Pools: map[tasks.PoolID]taskengine.PoolEngineConfig{
				"download": {
					Concurrency:    2,
					MinConcurrency: 0,
					ZeroIdle:       true,
					IdleTimeout:    20 * time.Millisecond,
					BacklogLimit:   8,
					PayloadBudget:  1 << 20,
				},
				"general": {
					Concurrency:    2,
					MinConcurrency: 0,
					ZeroIdle:       true,
					IdleTimeout:    20 * time.Millisecond,
					BacklogLimit:   8,
					PayloadBudget:  1 << 20,
				},
			},
			ResultCapacity: 16,
			ResourceCapacities: map[string]int64{
				"download": 2,
				"process":  2,
				"media":    2,
			},
		})
		if err := engine.Start(context.Background()); err != nil {
			t.Fatal(err)
		}
		defer func() {
			stopCtx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			_ = engine.Stop(stopCtx)
		}()

		store := storage.NewMemoryStorage()
		mediaReg, _ := newDownloaderRegistryTest(t)
		provider := &dlDTestProvider{
			name: "extractor",
			matchFn: func(rawURL string) bool {
				return strings.Contains(rawURL, "youtube.com") || strings.Contains(rawURL, "youtu.be")
			},
		}
		p := New(download.NewRegistry(provider), engine, store, mediaReg)
		attachDownloaderTestFilesystem(t, p)

		mediaSends := make(chan core.MessageSendContext, 2)
		tgSvc := &mockTelegramService{mediaSends: mediaSends}

		ytURL := "https://youtu.be/dQw4w9WgXcQ"
		if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, ytURL)); err != nil {
			t.Fatalf("handleYouTubeCommand(.yt url) error = %v", err)
		}

		select {
		case <-mediaSends:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for .yt retained delivery")
		}
		if provider.downloadCalls != 1 || provider.lastURL != ytURL {
			t.Fatalf("provider downloadCalls=%d lastURL=%q, want 1 / %q", provider.downloadCalls, provider.lastURL, ytURL)
		}

		// Verify .dl <youtube-url> still uses the exact same shared pipeline.
		if err := p.handleDownload(newDLDTestContext(tgSvc, ytURL)); err != nil {
			t.Fatalf("handleDownload(.dl url) error = %v", err)
		}
		select {
		case <-mediaSends:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for .dl retained delivery")
		}
		if provider.downloadCalls != 2 {
			t.Fatalf("provider downloadCalls=%d after .dl, want 2", provider.downloadCalls)
		}

		if len(provider.createdAssetIDs) != 2 {
			t.Fatalf("createdAssetIDs=%d, want 2", len(provider.createdAssetIDs))
		}
		for _, id := range provider.createdAssetIDs {
			rec, err := mediaReg.Asset(context.Background(), id)
			if err != nil {
				t.Fatalf("mediaReg.Asset(%q) error = %v", id, err)
			}
			if rec.Owner != downloaderRegistryOwner {
				t.Fatalf("retained media owner=%q, want %q", rec.Owner, downloaderRegistryOwner)
			}
		}
	})

	t.Run("renderer_available_uses_interactive_flow", func(t *testing.T) {
		provider := &dlDTestProvider{
			name: "extractor",
			matchFn: func(rawURL string) bool {
				return strings.Contains(rawURL, "youtube.com")
			},
		}
		renderer := &downloaderFakeRenderer{}
		taskClient := &capturedClient{}
		p := New(download.NewRegistry(provider), taskClient)
		p.SetSelfInlineRenderer(renderer)

		ytURL := "https://www.youtube.com/watch?v=dQw4w9WgXcQ"
		if err := p.handleYouTubeCommand(newDLDTestContext(&mockTelegramService{}, ytURL)); err != nil {
			t.Fatalf("handleYouTubeCommand() error = %v", err)
		}
		if renderer.calls != 1 {
			t.Fatalf("renderer.calls = %d, want 1", renderer.calls)
		}
		if renderer.request.ResultID != "downloader" || renderer.request.Query != "dl "+ytURL {
			t.Fatalf("renderer.request = %+v, want ResultID=downloader Query=%q", renderer.request, "dl "+ytURL)
		}
		if taskClient.Count() != 0 {
			t.Fatalf("interactive path submitted %d native tasks before user selection", taskClient.Count())
		}
	})
}

func TestYouTubeFeature_UnexpectedSearchErrorPropagates(t *testing.T) {
	sentinel := errors.New("unexpected search failure")
	provider := &dlDTestProvider{name: "extractor", searchErr: sentinel}
	p := New(download.NewRegistry(provider), &youtubeSearchTaskClient{})

	err := p.handleYouTubeCommand(newDLDTestContext(&mockTelegramService{}, "test"))
	if !errors.Is(err, sentinel) {
		t.Fatalf("err = %v, want %v", err, sentinel)
	}
}
