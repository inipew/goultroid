package downloader

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/feature"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"
	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/services/download"
	"github.com/inipew/goultroid/internal/services/storage"
	"github.com/inipew/goultroid/internal/tasks"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type cookieRecordingProvider struct {
	name          string
	matchFn       func(string) bool
	mu            sync.Mutex
	searchCookies []string
	probeCookies  []string
	dlCookies     []string
	cookiePerms   []os.FileMode
	cookieBodies  [][]byte
	probeHook     func(ctx context.Context, cookiesFile string) error
	dlHook        func(ctx context.Context, cookiesFile string) error
	probeErr      error
	dlErr         error
}

func (p *cookieRecordingProvider) Name() string { return p.name }

func (p *cookieRecordingProvider) Match(rawURL string) bool {
	if p.matchFn != nil {
		return p.matchFn(rawURL)
	}
	return false
}

func (p *cookieRecordingProvider) inspectCookieFile(path string) {
	if path == "" {
		return
	}
	if info, err := os.Stat(path); err == nil {
		p.cookiePerms = append(p.cookiePerms, info.Mode().Perm())
	}
	if body, err := os.ReadFile(path); err == nil {
		p.cookieBodies = append(p.cookieBodies, body)
		// Simulate yt-dlp mutating the cookie file in place (must never affect master).
		_ = os.WriteFile(path, append(body, []byte("# mutated-by-ytdlp\n")...), 0600)
	}
}

func (p *cookieRecordingProvider) Search(_ context.Context, _ string, opts download.SearchOptions) ([]download.SearchResult, error) {
	p.mu.Lock()
	p.searchCookies = append(p.searchCookies, opts.CookiesFile)
	p.inspectCookieFile(opts.CookiesFile)
	p.mu.Unlock()
	return []download.SearchResult{{
		Provider: "extractor",
		Source:   "youtube",
		SourceID: "abcdefghijk",
		URL:      "https://www.youtube.com/watch?v=abcdefghijk",
		Title:    "Cookie Search Result",
	}}, nil
}

func (p *cookieRecordingProvider) Probe(ctx context.Context, _ string, opts download.ProbeOptions) (download.ProbeResult, error) {
	p.mu.Lock()
	p.probeCookies = append(p.probeCookies, opts.CookiesFile)
	p.inspectCookieFile(opts.CookiesFile)
	hook := p.probeHook
	err := p.probeErr
	p.mu.Unlock()
	if hook != nil {
		if hookErr := hook(ctx, opts.CookiesFile); hookErr != nil {
			return download.ProbeResult{}, hookErr
		}
	}
	if err != nil {
		return download.ProbeResult{}, err
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return download.ProbeResult{}, ctxErr
	}
	return download.ProbeResult{
		Title:          "Cookie Probe Video",
		VideoQualities: []download.VideoQuality{{Height: 720, Size: 1024}},
	}, nil
}

func (p *cookieRecordingProvider) Download(ctx context.Context, _ string, store storage.Storage, opts download.DownloadOptions) (*storage.Asset, error) {
	p.mu.Lock()
	p.dlCookies = append(p.dlCookies, opts.CookiesFile)
	p.inspectCookieFile(opts.CookiesFile)
	hook := p.dlHook
	dlErr := p.dlErr
	p.mu.Unlock()
	if hook != nil {
		if hookErr := hook(ctx, opts.CookiesFile); hookErr != nil {
			return nil, hookErr
		}
	}
	if dlErr != nil {
		return nil, dlErr
	}
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return store.Put(context.Background(), bytes.NewReader([]byte("ok")), storage.Metadata{Name: "video.mp4"})
}

func TestYouTubeCookiesIntegrationSearchDownloadAndIsolation(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	currentKey := key
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return currentKey
		}
		return ""
	})
	fixture := validNetscapeCookiesFixture(time.Now())
	if err := store.SaveValidatedCookies(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}

	t.Run("youtube_search_uses_0600_snapshot_and_cleans_up", func(t *testing.T) {
		prov := &cookieRecordingProvider{name: "extractor"}
		p := New(download.NewRegistry(prov), &youtubeSearchTaskClient{})
		p.files = scope
		p.cookieStore = store

		results, err := p.searchYouTube(context.Background(), "lofi")
		if err != nil || len(results) != 1 {
			t.Fatalf("searchYouTube() = (%d, %v), want (1, nil)", len(results), err)
		}
		if len(prov.searchCookies) != 1 || prov.searchCookies[0] == "" {
			t.Fatalf("searchCookies = %v, want non-empty snapshot path", prov.searchCookies)
		}
		if runtime.GOOS != "windows" && (len(prov.cookiePerms) != 1 || prov.cookiePerms[0] != 0600) {
			t.Fatalf("snapshot perms = %v, want [0600]", prov.cookiePerms)
		}
		if !bytes.Equal(prov.cookieBodies[0], fixture) {
			t.Fatal("snapshot body did not match decrypted fixture")
		}
		assertTempDirClean(t, p)

		// Master encrypted store must remain untouched (no write-back of "# mutated-by-ytdlp").
		masterPlain, err := store.LoadDecryptedCookies(context.Background())
		if err != nil || !bytes.Equal(masterPlain, fixture) {
			t.Fatalf("master cookies mutated or unreadable: err=%v", err)
		}
	})

	t.Run("youtube_download_uses_snapshot_and_non_youtube_is_independent", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		extProv := &cookieRecordingProvider{
			name:    "extractor",
			matchFn: func(u string) bool { return isYouTubeURL(u) || bytes.Contains([]byte(u), []byte("tiktok.com")) },
		}
		httpProv := &cookieRecordingProvider{
			name:    "http",
			matchFn: func(u string) bool { return bytes.Contains([]byte(u), []byte("example.com")) },
		}
		memStore := storage.NewMemoryStorage()
		mediaReg, _ := newDownloaderRegistryTest(t)
		p := New(download.NewRegistry(extProv, httpProv), engine, memStore, mediaReg)
		p.files = scope
		p.cookieStore = store

		runOne := func(rawURL, provider string) error {
			done := make(chan error, 1)
			if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
				URL:      rawURL,
				Provider: provider,
			}, urlPipelineHooks{
				Delivery: func(context.Context, presentation.Media) error {
					done <- nil
					return nil
				},
				DownloadFailureWithErr: func(_ context.Context, err error) error {
					done <- err
					return nil
				},
			}); err != nil {
				return err
			}
			select {
			case err := <-done:
				return err
			case <-time.After(3 * time.Second):
				t.Fatal("timed out waiting for pipeline")
				return nil
			}
		}

		// 1. YouTube download gets cookie snapshot.
		if err := runOne("https://www.youtube.com/watch?v=abcdefghijk", "extractor"); err != nil {
			t.Fatalf("YouTube download error = %v", err)
		}
		// 2. Non-YouTube extractor (TikTok) and DirectHTTP work even when key is missing!
		currentKey = ""
		if err := runOne("https://www.tiktok.com/@user/video/12345", "extractor"); err != nil {
			t.Fatalf("TikTok download error = %v", err)
		}
		if err := runOne("https://example.com/file.mp4", "http"); err != nil {
			t.Fatalf("DirectHTTP download error = %v", err)
		}
		// 3. YouTube download fails closed when cookies exist but key is missing.
		if err := runOne("https://www.youtube.com/watch?v=abcdefghijk", "extractor"); !errors.Is(err, core.ErrUnavailable) {
			t.Fatalf("YouTube download with missing key = %v, want ErrUnavailable", err)
		}

		currentKey = key
		if len(extProv.dlCookies) != 2 || extProv.dlCookies[0] == "" || extProv.dlCookies[1] != "" {
			t.Fatalf("dlCookies = %v, want [non-empty, empty]", extProv.dlCookies)
		}
		assertTempDirClean(t, p)
	})

	t.Run("concurrent_youtube_snapshots_are_isolated_and_cleaned", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		prov := &cookieRecordingProvider{name: "extractor"}
		p := New(download.NewRegistry(prov), engine)
		p.files = scope
		p.cookieStore = store

		const n = 5
		var wg sync.WaitGroup
		wg.Add(n)
		for range n {
			go func() {
				defer wg.Done()
				_, _ = p.searchYouTube(context.Background(), "lofi")
			}()
		}
		wg.Wait()

		if len(prov.searchCookies) != n {
			t.Fatalf("searchCookies = %d, want %d", len(prov.searchCookies), n)
		}
		seen := make(map[string]struct{}, n)
		for _, path := range prov.searchCookies {
			if path == "" {
				t.Fatal("expected non-empty snapshot path")
			}
			if _, dup := seen[path]; dup {
				t.Fatalf("concurrent operations shared snapshot path %q", path)
			}
			seen[path] = struct{}{}
		}
		assertTempDirClean(t, p)
	})

	t.Run("startup_sweep_cleans_stale_cookie_snapshots", func(t *testing.T) {
		p := New()
		p.files = scope
		tempDir, err := scope.TempDir()
		if err != nil {
			t.Fatal(err)
		}
		stale := filepath.Join(tempDir, "ytdlp-cookies-stale123.txt")
		if err := os.WriteFile(stale, []byte("stale"), 0600); err != nil {
			t.Fatal(err)
		}
		p.cleanupStaleCookieSnapshots()
		assertTempDirClean(t, p)
	})
}

func TestYouTubeCookiesAbsentFallback(t *testing.T) {
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return "" })
	prov := &cookieRecordingProvider{name: "extractor"}
	p := New(download.NewRegistry(prov), &youtubeSearchTaskClient{})
	p.files = scope
	p.cookieStore = store

	// No yt_cookies.enc exists -> unauthenticated fallback (CookiesFile == "").
	results, err := p.searchYouTube(context.Background(), "lofi")
	if err != nil || len(results) != 1 {
		t.Fatalf("searchYouTube() = (%d, %v), want (1, nil)", len(results), err)
	}
	if len(prov.searchCookies) != 1 || prov.searchCookies[0] != "" {
		t.Fatalf("searchCookies = %v, want [\"\"] when no cookies configured", prov.searchCookies)
	}
}

func TestYouTubeCookiesProbeAndLifecycleCleanup(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return key
		}
		return ""
	})
	fixture := validNetscapeCookiesFixture(time.Now())
	if err := store.SaveValidatedCookies(context.Background(), fixture); err != nil {
		t.Fatal(err)
	}

	dispatchVideoProbe := func(prov *cookieRecordingProvider, dispatchCtx context.Context, customPort ...*deadlineAwarePort) error {
		p := New(download.NewRegistry(prov))
		p.files = scope
		p.cookieStore = store

		catalog := feature.NewRegistry()
		spec := p.FeatureSpec()
		ownerScope := tasks.ScopeIdentity{Owner: "plugin:downloader", Generation: 1}
		registration, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: ownerScope}, spec)
		if err != nil {
			return err
		}
		defer registration.Close()

		sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
		if err != nil {
			return err
		}
		defer sessions.Close()

		port := &deadlineAwarePort{}
		if len(customPort) > 0 && customPort[0] != nil {
			port = customPort[0]
		}
		engine, err := orchestration.New(sessions, rootinteraction.NewDispatcher(sessions), port)
		if err != nil {
			return err
		}
		cleanup, err := p.BindAssistant(assistantinteraction.DriverRuntime{
			Engine: engine, Catalog: catalog,
			Admit: func(string, feature.InteractionKind, string, int64, presentation.Target) error { return nil },
		})
		if err != nil {
			return err
		}
		defer cleanup()

		state, err := encodeInteractiveState(interactiveState{
			URL: "https://www.youtube.com/watch?v=abcdefghijk", Provider: "extractor", Phase: phaseChoose,
		})
		if err != nil {
			return err
		}
		created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
			FeatureID: p.Name(), Binding: rootinteraction.Binding{ActorID: 42}, State: state, TTL: interactiveTTL,
		})
		if err != nil {
			return err
		}
		data, err := sessions.CallbackData(context.Background(), created.Session.ID, actionVideo)
		if err != nil {
			return err
		}
		prepared, err := engine.PrepareCallback(dispatchCtx, orchestration.CallbackRequest{
			Data: data, ActorID: 42, QueryID: 99,
			Target: presentationtelegram.InlineTarget{BindingID: "inline:downloader:probe"},
		})
		if err != nil {
			return err
		}
		return prepared.Dispatch(dispatchCtx)
	}

	t.Run("probe_success_receives_0600_snapshot_and_cleans_up", func(t *testing.T) {
		prov := &cookieRecordingProvider{name: "extractor", matchFn: isYouTubeURL}
		if err := dispatchVideoProbe(prov, context.Background()); err != nil {
			t.Fatalf("probe dispatch error = %v", err)
		}
		if len(prov.probeCookies) != 1 || prov.probeCookies[0] == "" {
			t.Fatalf("probeCookies = %v, want non-empty snapshot path", prov.probeCookies)
		}
		if runtime.GOOS != "windows" && (len(prov.cookiePerms) != 1 || prov.cookiePerms[0] != 0600) {
			t.Fatalf("probe snapshot perms = %v, want [0600]", prov.cookiePerms)
		}
		assertTempDirClean(t, &Plugin{files: scope})
	})

	t.Run("probe_process_error_cleans_up_snapshot", func(t *testing.T) {
		prov := &cookieRecordingProvider{
			name:     "extractor",
			matchFn:  isYouTubeURL,
			probeErr: fmt.Errorf("%w: yt-dlp exited 1", download.ErrProbeFailed),
		}
		err := dispatchVideoProbe(prov, context.Background())
		if !errors.Is(err, download.ErrProbeFailed) {
			t.Fatalf("err = %v, want ErrProbeFailed", err)
		}
		assertTempDirClean(t, &Plugin{files: scope})
	})

	t.Run("probe_timeout_and_cancellation_after_snapshot_clean_up_snapshot", func(t *testing.T) {
		// 1. Cancellation after snapshot is created and provider has started.
		cancelStarted := make(chan string, 1)
		cancelProv := &cookieRecordingProvider{
			name:    "extractor",
			matchFn: isYouTubeURL,
			probeHook: func(ctx context.Context, cookiesFile string) error {
				cancelStarted <- cookiesFile
				<-ctx.Done()
				return ctx.Err()
			},
		}
		cancelCtx, cancel := context.WithCancel(context.Background())
		cancelDone := make(chan error, 1)
		go func() {
			cancelDone <- dispatchVideoProbe(cancelProv, cancelCtx)
		}()

		var cancelSnapPath string
		select {
		case cancelSnapPath = <-cancelStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for probe to receive snapshot")
		}
		if _, err := os.Stat(cancelSnapPath); err != nil {
			t.Fatalf("expected active snapshot %s before cancel: %v", cancelSnapPath, err)
		}
		cancel()

		select {
		case err := <-cancelDone:
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("probe cancel err = %v, want context.Canceled", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for cancelled probe")
		}
		if _, err := os.Stat(cancelSnapPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("snapshot %s still exists after cancel: %v", cancelSnapPath, err)
		}
		assertTempDirClean(t, &Plugin{files: scope})

		// 2. Timeout after snapshot is created and provider has started.
		timeoutStarted := make(chan string, 1)
		timeoutProv := &cookieRecordingProvider{
			name:    "extractor",
			matchFn: isYouTubeURL,
			probeHook: func(ctx context.Context, cookiesFile string) error {
				timeoutStarted <- cookiesFile
				<-ctx.Done()
				return ctx.Err()
			},
		}
		timeoutCtx, cancelTimeout := context.WithTimeout(context.Background(), 80*time.Millisecond)
		defer cancelTimeout()
		timeoutDone := make(chan error, 1)
		go func() {
			timeoutDone <- dispatchVideoProbe(timeoutProv, timeoutCtx)
		}()

		var timeoutSnapPath string
		select {
		case timeoutSnapPath = <-timeoutStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for probe to receive snapshot before timeout")
		}
		if _, err := os.Stat(timeoutSnapPath); err != nil {
			t.Fatalf("expected active snapshot %s before timeout: %v", timeoutSnapPath, err)
		}

		select {
		case err := <-timeoutDone:
			if !errors.Is(err, context.DeadlineExceeded) {
				t.Fatalf("probe timeout err = %v, want context.DeadlineExceeded", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for timed-out probe")
		}
		if _, err := os.Stat(timeoutSnapPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("snapshot %s still exists after timeout: %v", timeoutSnapPath, err)
		}
		assertTempDirClean(t, &Plugin{files: scope})
	})

	t.Run("download_process_error_and_cancellation_clean_up_snapshot", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		// 1. Process error cleans up snapshot.
		prov := &cookieRecordingProvider{
			name:    "extractor",
			matchFn: isYouTubeURL,
			dlErr:   download.ErrDownloadFailed,
		}
		p := New(download.NewRegistry(prov), engine, storage.NewMemoryStorage())
		p.files = scope
		p.cookieStore = store

		done := make(chan error, 1)
		if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL: "https://www.youtube.com/watch?v=abcdefghijk", Provider: "extractor",
		}, urlPipelineHooks{
			Delivery:               func(context.Context, presentation.Media) error { done <- nil; return nil },
			DownloadFailureWithErr: func(_ context.Context, err error) error { done <- err; return nil },
		}); err != nil {
			t.Fatal(err)
		}
		select {
		case err := <-done:
			if !errors.Is(err, download.ErrDownloadFailed) {
				t.Fatalf("err = %v, want ErrDownloadFailed", err)
			}
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for failed download")
		}
		assertTempDirClean(t, p)

		// 2. Cancellation after snapshot is created cleans up snapshot.
		dlStarted := make(chan string, 1)
		cancelProv := &cookieRecordingProvider{
			name:    "extractor",
			matchFn: isYouTubeURL,
			dlHook: func(ctx context.Context, cookiesFile string) error {
				dlStarted <- cookiesFile
				<-ctx.Done()
				return ctx.Err()
			},
		}
		pCancel := New(download.NewRegistry(cancelProv), engine, storage.NewMemoryStorage())
		pCancel.files = scope
		pCancel.cookieStore = store

		cancelDone := make(chan error, 1)
		const taskRoot = "cancel-cookie-dl-test"
		if err := pCancel.submitURLPipeline(context.Background(), urlDownloadRequest{
			URL: "https://www.youtube.com/watch?v=abcdefghijk", Provider: "extractor", TaskRoot: taskRoot,
		}, urlPipelineHooks{
			Delivery:               func(context.Context, presentation.Media) error { cancelDone <- nil; return nil },
			DownloadFailureWithErr: func(_ context.Context, err error) error { cancelDone <- err; return nil },
			Cancel:                 func() bool { cancelDone <- context.Canceled; return true },
		}); err != nil {
			t.Fatal(err)
		}

		var dlSnapPath string
		select {
		case dlSnapPath = <-dlStarted:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for download to receive snapshot")
		}
		if _, err := os.Stat(dlSnapPath); err != nil {
			t.Fatalf("expected active download snapshot %s before cancel: %v", dlSnapPath, err)
		}
		if err := pCancel.cancelInteractivePipeline(taskRoot); err != nil {
			t.Fatalf("cancelInteractivePipeline() error = %v", err)
		}

		select {
		case <-cancelDone:
		case <-time.After(3 * time.Second):
			t.Fatal("timed out waiting for cancelled download")
		}
		if _, err := os.Stat(dlSnapPath); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("download snapshot %s still exists after cancel: %v", dlSnapPath, err)
		}
		assertTempDirClean(t, pCancel)
	})

	t.Run("concurrent_probe_and_download_use_distinct_snapshots", func(t *testing.T) {
		engine := newP5DownloaderTaskEngine(t)
		t.Cleanup(func() { _ = engine.Stop(context.Background()) })

		prov := &cookieRecordingProvider{name: "extractor", matchFn: isYouTubeURL}
		p := New(download.NewRegistry(prov), engine, storage.NewMemoryStorage())
		p.files = scope
		p.cookieStore = store

		const pairs = 3
		errCh := make(chan error, pairs*2)
		var wg sync.WaitGroup
		wg.Add(pairs * 2)
		for range pairs {
			go func() {
				defer wg.Done()
				errCh <- dispatchVideoProbe(prov, context.Background())
			}()
			go func() {
				defer wg.Done()
				done := make(chan error, 1)
				if err := p.submitURLPipeline(context.Background(), urlDownloadRequest{
					URL: "https://www.youtube.com/watch?v=abcdefghijk", Provider: "extractor",
				}, urlPipelineHooks{
					TargetKind:             "message",
					Delivery:               func(context.Context, presentation.Media) error { return nil },
					Delivered:              func(context.Context) error { done <- nil; return nil },
					DownloadFailureWithErr: func(_ context.Context, err error) error { done <- err; return nil },
				}); err != nil {
					errCh <- err
					return
				}
				select {
				case err := <-done:
					errCh <- err
				case <-time.After(5 * time.Second):
					errCh <- errors.New("timed out waiting for concurrent download")
				}
			}()
		}
		wg.Wait()
		close(errCh)

		for err := range errCh {
			if err != nil {
				t.Fatalf("concurrent Probe/Download failed: %v", err)
			}
		}

		prov.mu.Lock()
		all := append(append([]string(nil), prov.probeCookies...), prov.dlCookies...)
		prov.mu.Unlock()
		if len(all) != pairs*2 {
			t.Fatalf("total cookie snapshots = %d, want %d", len(all), pairs*2)
		}
		seen := make(map[string]struct{}, len(all))
		for _, path := range all {
			if path == "" {
				t.Fatal("expected non-empty snapshot path")
			}
			if _, dup := seen[path]; dup {
				t.Fatalf("concurrent Probe/Download shared snapshot path %q", path)
			}
			seen[path] = struct{}{}
		}
		assertTempDirClean(t, p)
	})
}

func TestYouTubeCookieSnapshotLogsDoNotLeakPaths(t *testing.T) {
	scope := newTestCookieScope(t)
	key := newTestCookieKeyBase64(t)
	currentKey := key
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return currentKey
		}
		return ""
	})
	if err := store.SaveValidatedCookies(context.Background(), validNetscapeCookiesFixture(time.Now())); err != nil {
		t.Fatal(err)
	}

	coreObs, logs := observer.New(zap.WarnLevel)
	p := New()
	p.SetLogger(zap.New(coreObs))
	p.files = scope
	p.cookieStore = store

	// 1. Trigger LoadDecryptedCookies failure (missing key).
	currentKey = ""
	if _, _, err := p.acquireYouTubeCookieSnapshot(context.Background()); !errors.Is(err, core.ErrUnavailable) {
		t.Fatalf("err = %v, want ErrUnavailable", err)
	}

	// 2. Trigger real cleanup failure (non-ErrNotExist) via seam and verify cleanup_failed log.
	currentKey = key
	dataDir, _ := scope.DataDir()
	tempDir, _ := scope.TempDir()
	injectedErrPath := filepath.Join(tempDir, "secret-injected-path")
	p.removeCookieSnapshot = func(string) error {
		return fmt.Errorf("remove %s: permission denied", injectedErrPath)
	}
	snapPath, cleanup, err := p.acquireYouTubeCookieSnapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cleanup()
	// Remove real file afterward so t.TempDir is clean.
	_ = scope.RemoveTempFile(snapPath)

	if logs.FilterMessage("downloader: failed to load encrypted youtube cookies").Len() != 1 {
		t.Fatal("expected 1 sanitized load failure log")
	}
	cleanupLogs := logs.FilterMessage("downloader: failed to remove cookie snapshot").All()
	if len(cleanupLogs) != 1 {
		t.Fatalf("expected 1 cleanup failure log, got %d", len(cleanupLogs))
	}
	if gotCat := cleanupLogs[0].ContextMap()["category"]; gotCat != "cleanup_failed" {
		t.Fatalf("cleanup failure log category = %v, want cleanup_failed", gotCat)
	}

	for _, entry := range logs.All() {
		msgAndFields := entry.Message
		for _, f := range entry.Context {
			msgAndFields += " " + f.Key + "=" + f.String
		}
		for _, forbidden := range []string{dataDir, tempDir, injectedErrPath, "yt_cookies.enc", "ytdlp-cookies-", "secret_login_token_value", "permission denied"} {
			if strings.Contains(msgAndFields, forbidden) {
				t.Fatalf("snapshot log leaked sensitive string %q: %s", forbidden, msgAndFields)
			}
		}
	}
}

func TestYouTubeCookieProbeFailureLogsDoNotLeakSnapshotPath(t *testing.T) {
	scope := newTestCookieScope(t)
	key := newTestCookieKeyBase64(t)
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return key
		}
		return ""
	})
	if err := store.SaveValidatedCookies(context.Background(), validNetscapeCookiesFixture(time.Now())); err != nil {
		t.Fatal(err)
	}

	coreObs, logs := observer.New(zap.WarnLevel)
	var seenSnapPath string
	prov := &cookieRecordingProvider{
		name:    "extractor",
		matchFn: isYouTubeURL,
		probeHook: func(_ context.Context, cookiesFile string) error {
			seenSnapPath = cookiesFile
			return fmt.Errorf("%w: yt-dlp probe failed: ERROR: unable to open --cookies %s: permission denied", download.ErrProbeFailed, cookiesFile)
		},
	}
	p := New(download.NewRegistry(prov))
	p.SetLogger(zap.New(coreObs))
	p.files = scope
	p.cookieStore = store

	catalog := feature.NewRegistry()
	ownerScope := tasks.ScopeIdentity{Owner: "plugin:downloader", Generation: 1}
	registration, err := catalog.Register(feature.Owner{ID: p.Name(), Scope: ownerScope}, p.FeatureSpec())
	if err != nil {
		t.Fatal(err)
	}
	defer registration.Close()

	sessions, err := rootinteraction.NewRuntime(catalog, rootinteraction.Config{})
	if err != nil {
		t.Fatal(err)
	}
	defer sessions.Close()

	port := &deadlineAwarePort{}
	engine, err := orchestration.New(sessions, rootinteraction.NewDispatcher(sessions), port)
	if err != nil {
		t.Fatal(err)
	}
	cleanup, err := p.BindAssistant(assistantinteraction.DriverRuntime{
		Engine: engine, Catalog: catalog,
		Admit: func(string, feature.InteractionKind, string, int64, presentation.Target) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()

	state, err := encodeInteractiveState(interactiveState{
		URL: "https://www.youtube.com/watch?v=abcdefghijk", Provider: "extractor", Phase: phaseChoose,
	})
	if err != nil {
		t.Fatal(err)
	}
	created, err := sessions.Create(context.Background(), rootinteraction.CreateRequest{
		FeatureID: p.Name(), Binding: rootinteraction.Binding{ActorID: 42}, State: state, TTL: interactiveTTL,
	})
	if err != nil {
		t.Fatal(err)
	}
	data, err := sessions.CallbackData(context.Background(), created.Session.ID, actionVideo)
	if err != nil {
		t.Fatal(err)
	}
	prepared, err := engine.PrepareCallback(context.Background(), orchestration.CallbackRequest{
		Data: data, ActorID: 42, QueryID: 99,
		Target: presentationtelegram.InlineTarget{BindingID: "inline:downloader:probe-log-sanitize"},
	})
	if err != nil {
		t.Fatal(err)
	}
	err = prepared.Dispatch(context.Background())
	if !errors.Is(err, download.ErrProbeFailed) {
		t.Fatalf("Dispatch() err = %v, want ErrProbeFailed", err)
	}
	if seenSnapPath == "" {
		t.Fatal("expected probeHook to receive snapshot path")
	}

	probeLogs := logs.FilterMessage("downloader: probe failed").All()
	if len(probeLogs) != 1 {
		t.Fatalf("expected 1 probe failed log, got %d", len(probeLogs))
	}
	if gotCat := probeLogs[0].ContextMap()["category"]; gotCat != string(errCatDownloadFailure) {
		t.Fatalf("probe failed log category = %v, want %q", gotCat, errCatDownloadFailure)
	}

	tempDir, _ := scope.TempDir()
	forbidden := []string{seenSnapPath, tempDir, "ytdlp-cookies-", "yt-dlp probe failed", "permission denied", "secret_login_token_value"}
	for _, entry := range logs.All() {
		msgAndFields := entry.Message
		for _, f := range entry.Context {
			msgAndFields += " " + f.Key + "=" + f.String
		}
		for _, bad := range forbidden {
			if strings.Contains(msgAndFields, bad) {
				t.Fatalf("probe log leaked %q: %s", bad, msgAndFields)
			}
		}
	}
	for _, bad := range forbidden {
		if strings.Contains(port.edited.Text, bad) {
			t.Fatalf("user-facing failure view leaked %q: %s", bad, port.edited.Text)
		}
	}
	assertTempDirClean(t, p)
}
