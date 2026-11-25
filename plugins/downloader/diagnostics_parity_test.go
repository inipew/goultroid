package downloader

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/services/download"
	inlineservice "github.com/inipew/goultroid/internal/services/inline"
	"github.com/inipew/goultroid/internal/services/localization"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/settings"
	"github.com/inipew/goultroid/internal/tasks"
)

type failingSubmitClient struct {
	tasks.Client
	err error
}

func (c *failingSubmitClient) Submit(context.Context, tasks.WorkSpec) (tasks.Ticket, error) {
	return nil, c.err
}

func TestDownloaderErrorClassificationAndSanitization(t *testing.T) {
	cases := []struct {
		name      string
		err       error
		wantCat   downloadErrorCategory
		retryable bool
	}{
		{"invalid_input_args", fmt.Errorf("wrap: %w", core.ErrInvalidArgs), errCatInvalidInput, false},
		{"invalid_input_scheme", fmt.Errorf("wrap: %w", download.ErrInvalidScheme), errCatInvalidInput, false},
		{"unsupported_source", fmt.Errorf("wrap: %w", download.ErrNoMatchingProvider), errCatUnsupportedSource, false},
		{"blocked_ssrf", fmt.Errorf("wrap: %w", download.ErrBlockedSSRF), errCatBlockedURL, false},
		{"unavailable_extractor", fmt.Errorf("wrap: %w", download.ErrExtractorUnavailable), errCatUnavailable, true},
		{"unavailable_core", fmt.Errorf("wrap: %w", core.ErrUnavailable), errCatUnavailable, true},
		{"timeout_deadline", fmt.Errorf("wrap: %w", context.DeadlineExceeded), errCatTimeout, true},
		{"timeout_core", fmt.Errorf("wrap: %w", core.ErrTimeout), errCatTimeout, true},
		{"too_large_limit", fmt.Errorf("wrap: %w", core.ErrResourceLimit), errCatTooLarge, false},
		{"too_large_media", fmt.Errorf("wrap: %w", core.ErrMediaTooLarge), errCatTooLarge, false},
		{"download_failure", fmt.Errorf("wrap: %w", download.ErrDownloadFailed), errCatDownloadFailure, true},
		{"probe_failed", fmt.Errorf("wrap: %w", download.ErrProbeFailed), errCatDownloadFailure, true},
		{"probe_failed_nested", fmt.Errorf("outer: %w: inner detail", download.ErrProbeFailed), errCatDownloadFailure, true},
		{"too_many_redirects", fmt.Errorf("wrap: %w", download.ErrTooManyRedirects), errCatDownloadFailure, false},
		{"retained_registration", fmt.Errorf("wrap: %w", errRetainedRegistration), errCatInternalFailure, false},
		{"delivery_failure", fmt.Errorf("wrap: %w", errDeliveryFailure), errCatDeliveryFailure, true},
		{"cancelled", fmt.Errorf("wrap: %w", context.Canceled), errCatCancelled, false},
		{"cancelled_scope", fmt.Errorf("wrap: %w", tasks.ErrScopeClosed), errCatCancelled, false},
		{"invalid_session", fmt.Errorf("wrap: %w", errInvalidSession), errCatInvalidSession, false},
		{"unknown_internal", errors.New("yt-dlp stderr: /tmp/secret/file.mp4 RPC_ERROR 500"), errCatInternalFailure, true},
	}

	sensitiveFragments := []string{"/tmp/secret", "yt-dlp stderr", "RPC_ERROR", "wrap:"}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotCat := classifyDownloaderError(tc.err)
			if gotCat != tc.wantCat {
				t.Fatalf("classifyDownloaderError(%v) = %q, want %q", tc.err, gotCat, tc.wantCat)
			}
			if gotRetry := isRetryableDownloaderError(tc.err); gotRetry != tc.retryable {
				t.Fatalf("isRetryableDownloaderError(%v) = %v, want %v", tc.err, gotRetry, tc.retryable)
			}
			for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
				msg := downloaderUserMessage(locale, tc.err)
				if msg == "" || strings.HasPrefix(msg, "downloader.") {
					t.Fatalf("downloaderUserMessage(%q) returned untranslated/empty %q", locale, msg)
				}
				for _, frag := range sensitiveFragments {
					if strings.Contains(msg, frag) {
						t.Fatalf("user message (%q) leaked sensitive fragment %q: %s", locale, frag, msg)
					}
				}
			}
		})
	}
}

func TestDownloaderErrorLocalizationENAndID(t *testing.T) {
	cats := []downloadErrorCategory{
		errCatInvalidInput,
		errCatUnsupportedSource,
		errCatBlockedURL,
		errCatUnavailable,
		errCatTimeout,
		errCatTooLarge,
		errCatDownloadFailure,
		errCatDeliveryFailure,
		errCatCancelled,
		errCatInvalidSession,
		errCatInternalFailure,
	}
	for _, cat := range cats {
		key := downloaderErrorKey(cat)
		en := downloaderTR(localization.LocaleEnglish, key)
		id := downloaderTR(localization.LocaleIndonesian, key)
		if en == "" || id == "" || en == key || id == key {
			t.Fatalf("missing translation for %s: en=%q id=%q", key, en, id)
		}
		if en == id {
			t.Fatalf("expected distinct EN and ID translations for %s, both=%q", key, en)
		}
	}
}

func TestDownloaderCommandErrorDiagnostics(t *testing.T) {
	t.Run("dl_unsupported_source_and_admission_failure", func(t *testing.T) {
		p := New(download.NewRegistry(), &failingSubmitClient{err: errors.New("pool full /var/run/secret")})
		tgSvc := &mockTelegramService{}

		err := p.handleDownload(newDLDTestContext(tgSvc, "https://example.com/file.mp4"))
		if !errors.Is(err, download.ErrNoMatchingProvider) {
			t.Fatalf("err = %v, want ErrNoMatchingProvider", err)
		}
		want := downloaderUserMessage(localization.DefaultLocale, download.ErrNoMatchingProvider)
		if !strings.Contains(tgSvc.lastEdited, want) {
			t.Fatalf("status = %q, want substring %q", tgSvc.lastEdited, want)
		}

		p.registry = download.NewRegistry(download.NewDirectHTTPProvider(time.Minute, 1024*1024))
		err = p.handleDownload(newDLDTestContext(tgSvc, "https://example.com/file.mp4"))
		if err == nil {
			t.Fatal("expected admission error")
		}
		if strings.Contains(tgSvc.lastEdited, "/var/run/secret") || strings.Contains(tgSvc.lastEdited, "pool full") {
			t.Fatalf("admission status leaked internal details: %q", tgSvc.lastEdited)
		}
	})

	t.Run("yt_unexpected_search_error_sanitized_in_status_and_preserved_for_diag", func(t *testing.T) {
		sentinel := errors.New("yt-dlp stderr: /tmp/yt/cache failed")
		provider := &dlDTestProvider{name: "extractor", searchErr: sentinel}
		p := New(download.NewRegistry(provider), &youtubeSearchTaskClient{})
		tgSvc := &mockTelegramService{}

		err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, "lofi"))
		if !errors.Is(err, sentinel) {
			t.Fatalf("returned err = %v, want sentinel preserved", err)
		}
		if strings.Contains(tgSvc.lastEdited, "/tmp/yt") || strings.Contains(tgSvc.lastEdited, "yt-dlp stderr") {
			t.Fatalf("yt status leaked internal error: %q", tgSvc.lastEdited)
		}
		if !strings.Contains(tgSvc.lastEdited, downloaderUserMessage(localization.DefaultLocale, sentinel)) {
			t.Fatalf("yt status = %q, want substring %q", tgSvc.lastEdited, downloaderUserMessage(localization.DefaultLocale, sentinel))
		}
	})
}

func TestDownloaderInlineErrorDiagnostics(t *testing.T) {
	t.Run("dl_invalid_and_unsupported_urls", func(t *testing.T) {
		p := New(download.NewRegistry())
		h := &interactiveInlineHandler{plugin: p}

		resp, err := h.HandleInlineV2(&inlineservice.InlineContext{
			Ctx:  context.Background(),
			Args: []string{"ftp://example.com/file.mp4"},
		})
		if err != nil || len(resp.Results) != 1 {
			t.Fatalf("invalid url inline = (%+v, %v)", resp, err)
		}
		if resp.Results[0].Text != downloaderUserMessage(localization.DefaultLocale, core.ErrInvalidArgs) {
			t.Fatalf("invalid url text = %q", resp.Results[0].Text)
		}

		resp, err = h.HandleInlineV2(&inlineservice.InlineContext{
			Ctx:  context.Background(),
			Args: []string{"https://example.com/file.mp4"},
		})
		if err != nil || len(resp.Results) != 1 {
			t.Fatalf("unsupported inline = (%+v, %v)", resp, err)
		}
		if resp.Results[0].Text != downloaderUserMessage(localization.DefaultLocale, download.ErrNoMatchingProvider) {
			t.Fatalf("unsupported text = %q", resp.Results[0].Text)
		}
	})

	t.Run("yt_inline_unexpected_error_sanitized", func(t *testing.T) {
		provider := &youtubeSearchProvider{err: errors.New("stderr: /home/user/.cache/yt-dlp")}
		p := New(&youtubeSearchTaskClient{})
		p.registry = download.NewRegistry(provider)
		h := &interactiveInlineHandler{plugin: p}

		resp, err := h.HandleInlineV2(&inlineservice.InlineContext{
			Ctx:      context.Background(),
			RawQuery: "yt lofi",
			Args:     []string{"lofi"},
		})
		if err != nil || len(resp.Results) != 1 {
			t.Fatalf("yt inline unexpected err = (%+v, %v)", resp, err)
		}
		if strings.Contains(resp.Results[0].Text, "/home/user") || strings.Contains(resp.Results[0].Text, "stderr:") {
			t.Fatalf("inline yt error leaked internal details: %q", resp.Results[0].Text)
		}
		if resp.Results[0].Text != downloaderUserMessage(localization.DefaultLocale, provider.err) {
			t.Fatalf("inline yt error text = %q", resp.Results[0].Text)
		}
	})
}

func TestDownloaderA2ErrorDiagnostics(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"timeout", context.DeadlineExceeded},
		{"blocked", download.ErrBlockedSSRF},
		{"too_large", core.ErrResourceLimit},
		{"unavailable", download.ErrExtractorUnavailable},
		{"invalid_session", errInvalidSession},
	} {
		view := failedView(tc.err, localization.LocaleIndonesian)
		want := downloaderUserMessage(localization.LocaleIndonesian, tc.err)
		if view.Text != want {
			t.Fatalf("failedView(%s) = %q, want %q", tc.name, view.Text, want)
		}
	}

	retryView := failedRetryView(interactiveState{
		Provider: "extractor",
		Mode:     download.MediaModeVideo,
		Format:   download.MediaFormatMP4,
		Locale:   localization.LocaleEnglish,
	}, context.DeadlineExceeded)
	if !strings.Contains(retryView.Text, downloaderUserMessage(localization.LocaleEnglish, context.DeadlineExceeded)) {
		t.Fatalf("failedRetryView missing mapped timeout message: %q", retryView.Text)
	}
	if len(retryView.Rows) != 1 || len(retryView.Rows[0]) != 2 || retryView.Rows[0][0].ActionID != actionRetry {
		t.Fatalf("failedRetryView rows = %+v, want retry+close", retryView.Rows)
	}
}

func TestDownloaderCrossSurfaceParity(t *testing.T) {
	p := New(download.NewRegistry())
	h := &interactiveInlineHandler{plugin: p}
	tgSvc := &mockTelegramService{}

	// Invalid URL across command, inline, and a2.
	_ = p.handleDownload(newDLDTestContext(tgSvc, "ftp://example.com/a.mp4"))
	cmdMsg := strings.TrimPrefix(tgSvc.lastEdited, "ℹ️ <b>Status:</b> ")
	inlineResp, err := h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{"ftp://example.com/a.mp4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a2Msg := failedView(download.ErrInvalidScheme, localization.DefaultLocale).Text
	if cmdMsg != inlineResp.Results[0].Text || cmdMsg != a2Msg {
		t.Fatalf("invalid input parity mismatch:\n cmd=%q\n inline=%q\n a2=%q", cmdMsg, inlineResp.Results[0].Text, a2Msg)
	}

	// Unsupported source across command, inline, and a2.
	_ = p.handleDownload(newDLDTestContext(tgSvc, "https://example.com/a.mp4"))
	cmdMsg = strings.TrimPrefix(tgSvc.lastEdited, "ℹ️ <b>Status:</b> ")
	inlineResp, err = h.HandleInlineV2(&inlineservice.InlineContext{
		Ctx:  context.Background(),
		Args: []string{"https://example.com/a.mp4"},
	})
	if err != nil {
		t.Fatal(err)
	}
	a2Msg = failedView(download.ErrNoMatchingProvider, localization.DefaultLocale).Text
	if cmdMsg != inlineResp.Results[0].Text || cmdMsg != a2Msg {
		t.Fatalf("unsupported source parity mismatch:\n cmd=%q\n inline=%q\n a2=%q", cmdMsg, inlineResp.Results[0].Text, a2Msg)
	}

	// Unavailable, timeout, and internal failure parity across native .yt, inline yt, and a2.
	for _, tc := range []struct {
		name string
		err  error
	}{
		{"unavailable", download.ErrExtractorUnavailable},
		{"timeout", context.DeadlineExceeded},
		{"internal", errors.New("yt-dlp stderr: /tmp/secret")},
	} {
		provider := &dlDTestProvider{name: "extractor", searchErr: tc.err}
		ytPlugin := New(download.NewRegistry(provider), &youtubeSearchTaskClient{})
		ytInline := &interactiveInlineHandler{plugin: ytPlugin}

		_ = ytPlugin.handleYouTubeCommand(newDLDTestContext(tgSvc, "lofi"))
		ytCmdMsg := strings.TrimPrefix(tgSvc.lastEdited, "ℹ️ <b>Status:</b> ")
		ytInlineResp, err := ytInline.HandleInlineV2(&inlineservice.InlineContext{
			Ctx:      context.Background(),
			RawQuery: "yt lofi",
			Args:     []string{"lofi"},
		})
		if err != nil {
			t.Fatal(err)
		}
		ytA2Msg := failedView(tc.err, localization.DefaultLocale).Text
		if ytCmdMsg != ytInlineResp.Results[0].Text || ytCmdMsg != ytA2Msg {
			t.Fatalf("%s parity mismatch:\n cmd=%q\n inline=%q\n a2=%q", tc.name, ytCmdMsg, ytInlineResp.Results[0].Text, ytA2Msg)
		}
	}
}

func newTestSettingsLocaleService(t *testing.T, locale string) *settings.Service {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	repo := settings.NewSQLiteRepository(db.DB)
	if err := repo.InitSchema(context.Background()); err != nil {
		t.Fatal(err)
	}
	reg := settings.NewRegistry()
	if err := settings.RegisterDefaultDefinitions(reg); err != nil {
		t.Fatal(err)
	}
	svc := settings.NewService(repo, reg, nil)
	if err := svc.Set(context.Background(), settings.ScopeGlobal, 0, localization.LocaleSettingNamespace, localization.LocaleSettingKey, locale, 0); err != nil {
		t.Fatal(err)
	}
	return svc
}

func TestDownloaderYouTubeOversizedQueryENAndID(t *testing.T) {
	oversized := strings.Repeat("q", download.MaxSearchQueryBytes+1)
	for _, locale := range []string{localization.LocaleEnglish, localization.LocaleIndonesian} {
		t.Run(locale, func(t *testing.T) {
			settingsSvc := newTestSettingsLocaleService(t, locale)
			p := New(&youtubeSearchTaskClient{})
			p.SetSettingsService(settingsSvc)
			h := &interactiveInlineHandler{plugin: p}
			tgSvc := &mockTelegramService{}

			if err := p.handleYouTubeCommand(newDLDTestContext(tgSvc, oversized)); err != nil {
				t.Fatalf("handleYouTubeCommand() error = %v", err)
			}
			want := downloaderUserMessage(locale, core.ErrInvalidArgs)
			if strings.TrimPrefix(tgSvc.lastEdited, "ℹ️ <b>Status:</b> ") != want {
				t.Fatalf("cmd msg (%s) = %q, want %q", locale, tgSvc.lastEdited, want)
			}

			resp, err := h.HandleInlineV2(&inlineservice.InlineContext{
				Ctx:      context.Background(),
				RawQuery: "yt " + oversized,
				Args:     []string{oversized},
			})
			if err != nil || len(resp.Results) != 1 || resp.Results[0].Text != want {
				t.Fatalf("inline (%s) = (%+v, %v), want %q", locale, resp, err, want)
			}
		})
	}
}

func TestDownloaderPipelineRetryAndDeliveryPaths(t *testing.T) {
	t.Run("download_failure_before_asset_is_retryable", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		provider := &dlDTestProvider{
			name:        "http",
			matchFn:     func(string) bool { return true },
			downloadErr: download.ErrDownloadFailed,
		}
		p := New(download.NewRegistry(provider), engine, storage.NewMemoryStorage())

		gotErrCh := make(chan error, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL:      "https://example.com/file.mp4",
			Provider: "http",
		}, urlPipelineHooks{
			Delivery: func(context.Context, presentation.Media) error { return nil },
			DownloadFailureWithErr: func(_ context.Context, err error) error {
				gotErrCh <- err
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}

		select {
		case gotErr := <-gotErrCh:
			if !errors.Is(gotErr, download.ErrDownloadFailed) || !isRetryableDownloaderError(gotErr) {
				t.Fatalf("gotErr = %v (retryable=%v), want retryable ErrDownloadFailed", gotErr, isRetryableDownloaderError(gotErr))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for download failure callback")
		}
	})

	t.Run("too_many_redirects_is_permanent_not_retryable", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		provider := &dlDTestProvider{
			name:        "http",
			matchFn:     func(string) bool { return true },
			downloadErr: download.ErrTooManyRedirects,
		}
		p := New(download.NewRegistry(provider), engine, storage.NewMemoryStorage())

		gotErrCh := make(chan error, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL:      "https://example.com/loop",
			Provider: "http",
		}, urlPipelineHooks{
			Delivery: func(context.Context, presentation.Media) error { return nil },
			DownloadFailureWithErr: func(_ context.Context, err error) error {
				gotErrCh <- err
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}

		select {
		case gotErr := <-gotErrCh:
			if !errors.Is(gotErr, download.ErrTooManyRedirects) || isRetryableDownloaderError(gotErr) {
				t.Fatalf("gotErr = %v (retryable=%v), want non-retryable ErrTooManyRedirects", gotErr, isRetryableDownloaderError(gotErr))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for redirect failure callback")
		}
	})

	t.Run("retained_registration_failure_after_download_is_not_retryable", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		store := storage.NewMemoryStorage()
		mediaReg, db := newDownloaderRegistryTest(t)
		fixedAsset, err := store.Put(context.Background(), strings.NewReader("x"), storage.Metadata{Name: "dup.mp4"})
		if err != nil {
			t.Fatal(err)
		}
		// Close DB so registerRetainedAsset fails after provider returns the asset.
		_ = db.Close()

		provider := &dlDTestProvider{
			name:          "http",
			matchFn:       func(string) bool { return true },
			downloadAsset: fixedAsset,
		}
		p := New(download.NewRegistry(provider), engine, store, mediaReg)

		gotErrCh := make(chan error, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL:      "https://example.com/dup.mp4",
			Provider: "http",
		}, urlPipelineHooks{
			Delivery: func(context.Context, presentation.Media) error { return nil },
			DownloadFailureWithErr: func(_ context.Context, err error) error {
				gotErrCh <- err
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}

		select {
		case gotErr := <-gotErrCh:
			if !errors.Is(gotErr, errRetainedRegistration) || isRetryableDownloaderError(gotErr) {
				t.Fatalf("gotErr = %v (retryable=%v), want non-retryable errRetainedRegistration", gotErr, isRetryableDownloaderError(gotErr))
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for retained registration failure callback")
		}
	})

	t.Run("delivery_failure_after_retention_preserves_asset_and_sanitizes_rpc_error", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		store := storage.NewMemoryStorage()
		mediaReg, _ := newDownloaderRegistryTest(t)
		provider := &dlDTestProvider{
			name:    "http",
			matchFn: func(string) bool { return true },
		}
		p := New(download.NewRegistry(provider), engine, store, mediaReg)
		attachDownloaderTestFilesystem(t, p)

		var (
			mu          sync.Mutex
			failureText string
		)
		doneCh := make(chan struct{}, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL:      "https://example.com/ok.mp4",
			Provider: "http",
		}, urlPipelineHooks{
			Delivery: func(context.Context, presentation.Media) error {
				return errors.New("RPC_ERROR_SECRET_420_FLOOD")
			},
			DeliveryFailure: func(context.Context) error {
				mu.Lock()
				failureText = deliveryFailedView(localization.LocaleEnglish).Text
				mu.Unlock()
				doneCh <- struct{}{}
				return nil
			},
		}); err != nil {
			t.Fatal(err)
		}

		select {
		case <-doneCh:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for delivery failure callback")
		}
		mu.Lock()
		gotText := failureText
		mu.Unlock()
		if gotText != downloaderUserMessage(localization.LocaleEnglish, errDeliveryFailure) {
			t.Fatalf("delivery failure text = %q", gotText)
		}
		if strings.Contains(gotText, "RPC_ERROR_SECRET_420_FLOOD") {
			t.Fatalf("delivery failure leaked RPC error: %q", gotText)
		}
		if len(provider.createdAssetIDs) != 1 {
			t.Fatalf("createdAssetIDs = %d, want 1", len(provider.createdAssetIDs))
		}
		rec, err := mediaReg.Asset(context.Background(), provider.createdAssetIDs[0])
		if err != nil {
			t.Fatalf("retained asset missing after delivery failure: %v", err)
		}
		if rec.Owner != downloaderRegistryOwner {
			t.Fatalf("retained asset owner = %q, want %q", rec.Owner, downloaderRegistryOwner)
		}
	})
}
