package downloader

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/platform/filesystem"
)

func newTestCookieKeyBase64(t *testing.T) string {
	t.Helper()
	key := make([]byte, cookieKeyBytes)
	if _, err := rand.Read(key); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(key)
}

func newTestCookieScope(t *testing.T) *filesystem.Scope {
	t.Helper()
	base := t.TempDir()
	mgr, err := filesystem.NewManager(
		filepath.Join(base, "data"),
		filepath.Join(base, "cache"),
		filepath.Join(base, "tmp"),
		nil,
	)
	if err != nil {
		t.Fatalf("filesystem.NewManager() error = %v", err)
	}
	return mgr.ForOwner("downloader")
}

func TestYouTubeCookieStoreEncryptDecryptAndKeyFormats(t *testing.T) {
	rawKey := make([]byte, cookieKeyBytes)
	for i := range rawKey {
		rawKey[i] = byte(i + 1)
	}
	formats := map[string]string{
		"std_base64":     base64.StdEncoding.EncodeToString(rawKey),
		"raw_std_base64": base64.RawStdEncoding.EncodeToString(rawKey),
		"url_base64":     base64.URLEncoding.EncodeToString(rawKey),
		"raw_url_base64": base64.RawURLEncoding.EncodeToString(rawKey),
		"hex":            hex.EncodeToString(rawKey),
	}

	plaintext := validNetscapeCookiesFixture(time.Now())
	for name, encodedKey := range formats {
		t.Run(name, func(t *testing.T) {
			scope := newTestCookieScope(t)
			store := newYouTubeCookieStore(scope, func(k string) string {
				if k == cookieSecretEnvVar {
					return encodedKey
				}
				return ""
			})

			if got := store.Status(context.Background()); got != cookieStatusNotConfigured {
				t.Fatalf("initial Status() = %q, want %q", got, cookieStatusNotConfigured)
			}

			if err := store.SaveValidatedCookies(context.Background(), plaintext); err != nil {
				t.Fatalf("SaveValidatedCookies() error = %v", err)
			}
			if got := store.Status(context.Background()); got != cookieStatusConfigured {
				t.Fatalf("Status() after save = %q, want %q", got, cookieStatusConfigured)
			}

			decrypted, err := store.LoadDecryptedCookies(context.Background())
			if err != nil {
				t.Fatalf("LoadDecryptedCookies() error = %v", err)
			}
			if !bytes.Equal(decrypted, plaintext) {
				t.Fatalf("decrypted mismatch: got %d bytes, want %d bytes", len(decrypted), len(plaintext))
			}

			// Verify ciphertext on disk does not contain plaintext or secret values.
			dataDir, err := scope.DataDir()
			if err != nil {
				t.Fatal(err)
			}
			onDisk, err := os.ReadFile(filepath.Join(dataDir, cookieCipherFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasPrefix(onDisk, []byte(cookieEnvelopeMagic)) {
				t.Fatalf("on-disk file missing versioned magic header %q", cookieEnvelopeMagic)
			}
			if bytes.Contains(onDisk, []byte("secret_login_token_value")) || bytes.Contains(onDisk, []byte("LOGIN_INFO")) {
				t.Fatal("on-disk ciphertext leaked plaintext cookie data")
			}

			// Restart persistence: a new store instance with the same scope & key can decrypt.
			reloaded := newYouTubeCookieStore(scope, func(k string) string {
				if k == cookieSecretEnvVar {
					return encodedKey
				}
				return ""
			})
			reloadedPlain, err := reloaded.LoadDecryptedCookies(context.Background())
			if err != nil {
				t.Fatalf("reloaded LoadDecryptedCookies() error = %v", err)
			}
			if !bytes.Equal(reloadedPlain, plaintext) {
				t.Fatal("reloaded plaintext mismatch")
			}
		})
	}
}

func TestYouTubeCookieStoreFreshNoncePerEncryption(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	plaintext := validNetscapeCookiesFixture(time.Now())

	if err := store.SaveValidatedCookies(context.Background(), plaintext); err != nil {
		t.Fatal(err)
	}
	dataDir, _ := scope.DataDir()
	first, err := os.ReadFile(filepath.Join(dataDir, cookieCipherFile))
	if err != nil {
		t.Fatal(err)
	}

	if err := store.SaveValidatedCookies(context.Background(), plaintext); err != nil {
		t.Fatal(err)
	}
	second, err := os.ReadFile(filepath.Join(dataDir, cookieCipherFile))
	if err != nil {
		t.Fatal(err)
	}

	if bytes.Equal(first, second) {
		t.Fatal("encrypting identical plaintext twice produced identical ciphertext; nonce must be fresh")
	}
}

func TestYouTubeCookieStoreFailClosedKeyTamperAndTruncation(t *testing.T) {
	keyA := newTestCookieKeyBase64(t)
	keyB := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	plaintext := validNetscapeCookiesFixture(time.Now())

	currentKey := keyA
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return currentKey
		}
		return ""
	})

	if err := store.SaveValidatedCookies(context.Background(), plaintext); err != nil {
		t.Fatal(err)
	}
	dataDir, _ := scope.DataDir()
	cipherPath := filepath.Join(dataDir, cookieCipherFile)

	// 1. Missing key -> Load fails closed, Status = key_unavailable, existing file preserved.
	currentKey = ""
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieKeyMissing) {
		t.Fatalf("missing key Load error = %v, want errCookieKeyMissing", err)
	}
	if got := store.Status(context.Background()); got != cookieStatusKeyUnavailable {
		t.Fatalf("missing key Status = %q, want %q", got, cookieStatusKeyUnavailable)
	}
	// Attempting SaveValidatedCookies with missing key fails and preserves original ciphertext.
	if err := store.SaveValidatedCookies(context.Background(), []byte("new")); !errors.Is(err, errCookieKeyMissing) {
		t.Fatalf("missing key Save error = %v, want errCookieKeyMissing", err)
	}

	// 2. Malformed key -> fails closed and preserves original ciphertext.
	currentKey = "not-a-valid-32-byte-key"
	if err := store.SaveValidatedCookies(context.Background(), []byte("new")); !errors.Is(err, errCookieKeyInvalid) {
		t.Fatalf("invalid key Save error = %v, want errCookieKeyInvalid", err)
	}
	if got := store.Status(context.Background()); got != cookieStatusKeyUnavailable {
		t.Fatalf("invalid key Status = %q, want %q", got, cookieStatusKeyUnavailable)
	}

	// 3. Wrong key -> fails decryption without destroying file.
	currentKey = keyB
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieDecryptFail) {
		t.Fatalf("wrong key Load error = %v, want errCookieDecryptFail", err)
	}

	// Restore keyA and verify original credential is still intact!
	currentKey = keyA
	gotPlain, err := store.LoadDecryptedCookies(context.Background())
	if err != nil || !bytes.Equal(gotPlain, plaintext) {
		t.Fatalf("original credential corrupted after failed replacements: err=%v", err)
	}

	// 4. Tampering with header (AAD), body, and truncation.
	validBytes, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}

	// 4a. Header tamper (version mismatch).
	badHeader := append([]byte(nil), validBytes...)
	badHeader[5] = '9'
	if err := os.WriteFile(cipherPath, badHeader, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieCorrupt) {
		t.Fatalf("tampered header error = %v, want errCookieCorrupt", err)
	}
	if got := store.Status(context.Background()); got != cookieStatusCorrupt {
		t.Fatalf("tampered header Status = %q, want %q", got, cookieStatusCorrupt)
	}

	// 4b. Ciphertext bit flip.
	badBody := append([]byte(nil), validBytes...)
	badBody[len(badBody)-1] ^= 0xff
	if err := os.WriteFile(cipherPath, badBody, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieDecryptFail) {
		t.Fatalf("tampered ciphertext error = %v, want errCookieDecryptFail", err)
	}

	// 4c. Truncation.
	if err := os.WriteFile(cipherPath, validBytes[:10], 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieCorrupt) {
		t.Fatalf("truncated ciphertext error = %v, want errCookieCorrupt", err)
	}
}

func TestYouTubeCookieStorePermissionsAndDelete(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permission bits not applicable on Windows")
	}
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	plaintext := validNetscapeCookiesFixture(time.Now())

	if err := store.SaveValidatedCookies(context.Background(), plaintext); err != nil {
		t.Fatal(err)
	}
	dataDir, _ := scope.DataDir()
	dirInfo, err := os.Stat(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := dirInfo.Mode().Perm(); perm != 0700 {
		t.Fatalf("DataDir perm = %o, want 0700", perm)
	}

	filePath := filepath.Join(dataDir, cookieCipherFile)
	fileInfo, err := os.Stat(filePath)
	if err != nil {
		t.Fatal(err)
	}
	if perm := fileInfo.Mode().Perm(); perm != 0600 {
		t.Fatalf("yt_cookies.enc perm = %o, want 0600", perm)
	}

	// Explicit delete removes ciphertext and returns Status to not_configured.
	deleted, err := store.Delete(context.Background())
	if err != nil || !deleted {
		t.Fatalf("Delete() = (%v, %v), want (true, nil)", deleted, err)
	}
	if _, err := os.Stat(filePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected %s to be removed, stat err = %v", filePath, err)
	}
	if got := store.Status(context.Background()); got != cookieStatusNotConfigured {
		t.Fatalf("Status() after Delete = %q, want %q", got, cookieStatusNotConfigured)
	}
	// Idempotent delete succeeds with deleted=false.
	deleted2, err := store.Delete(context.Background())
	if err != nil || deleted2 {
		t.Fatalf("second Delete() = (%v, %v), want (false, nil)", deleted2, err)
	}
}

func TestYouTubeCookiesCommandWithEncryptedStore(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(k string) string {
		if k == cookieSecretEnvVar {
			return key
		}
		return ""
	})

	validData := validNetscapeCookiesFixture(time.Now())
	svc := newCookiesTestTelegramService(validData)
	p := New()
	p.files = scope
	p.cookieSink = store

	ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
	ctx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")

	if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
		t.Fatalf("handleYouTubeCookiesCommand() error = %v", err)
	}
	if !strings.Contains(svc.lastEdit, "validated and accepted") {
		t.Fatalf("unexpected response = %q", svc.lastEdit)
	}
	if len(svc.deleteCalls) != 1 {
		t.Fatalf("deleteCalls = %d, want 1", len(svc.deleteCalls))
	}
	assertTempDirClean(t, p)

	loaded, err := store.LoadDecryptedCookies(context.Background())
	if err != nil {
		t.Fatalf("LoadDecryptedCookies() error = %v", err)
	}
	if !bytes.Equal(loaded, validData) {
		t.Fatal("stored decrypted cookies do not match uploaded fixture")
	}
}

func TestYouTubeCookieStoreBoundedEnvelopeRead(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })

	// 1. Exact boundary: maxCookieFileBytes plaintext -> maxCookieEnvelopeBytes envelope succeeds.
	exactPlaintext := bytes.Repeat([]byte("a"), int(maxCookieFileBytes))
	if err := store.SaveValidatedCookies(context.Background(), exactPlaintext); err != nil {
		t.Fatalf("SaveValidatedCookies(exact boundary) error = %v", err)
	}
	dataDir, _ := scope.DataDir()
	cipherPath := filepath.Join(dataDir, cookieCipherFile)
	info, err := os.Stat(cipherPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() != int64(maxCookieEnvelopeBytes) {
		t.Fatalf("exact boundary envelope size = %d, want %d", info.Size(), maxCookieEnvelopeBytes)
	}
	got, err := store.LoadDecryptedCookies(context.Background())
	if err != nil || !bytes.Equal(got, exactPlaintext) {
		t.Fatalf("LoadDecryptedCookies(exact boundary) failed: err=%v", err)
	}

	// 2. Oversized ciphertext (maxCookieEnvelopeBytes + 1) is rejected as errCookieCorrupt.
	oversized := append([]byte(cookieEnvelopeMagic), make([]byte, maxCookieEnvelopeBytes+1-len(cookieEnvelopeMagic))...)
	if err := os.WriteFile(cipherPath, oversized, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := store.LoadDecryptedCookies(context.Background()); !errors.Is(err, errCookieCorrupt) {
		t.Fatalf("LoadDecryptedCookies(oversized) error = %v, want errCookieCorrupt", err)
	}
	if status := store.Status(context.Background()); status != cookieStatusCorrupt {
		t.Fatalf("Status(oversized) = %q, want %q", status, cookieStatusCorrupt)
	}
}

func TestYouTubeCookieStorePermissionFailureFailsClosed(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	initial := validNetscapeCookiesFixture(time.Now())

	if err := store.SaveValidatedCookies(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	dataDir, _ := scope.DataDir()
	cipherPath := filepath.Join(dataDir, cookieCipherFile)
	initialOnDisk, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}

	// 1. Directory chmod failure fails closed and preserves existing credential.
	store.chmod = func(path string, mode os.FileMode) error {
		if path == dataDir {
			return errors.New("chmod dir denied")
		}
		return os.Chmod(path, mode)
	}
	if err := store.SaveValidatedCookies(context.Background(), []byte("replacement")); err == nil || !strings.Contains(err.Error(), "restrict cookie directory permissions") {
		t.Fatalf("expected dir permission error, got: %v", err)
	}
	afterDirFail, err := os.ReadFile(cipherPath)
	if err != nil || !bytes.Equal(afterDirFail, initialOnDisk) {
		t.Fatal("existing ciphertext changed after dir permission failure")
	}

	// 2. Staging file chmod failure before rename fails closed, removes staging file, and preserves existing ciphertext.
	store.chmod = func(path string, mode os.FileMode) error {
		if strings.Contains(filepath.Base(path), ".tmp.") {
			return errors.New("chmod staging denied")
		}
		return os.Chmod(path, mode)
	}
	if err := store.SaveValidatedCookies(context.Background(), []byte("replacement")); err == nil || !strings.Contains(err.Error(), "restrict temp ciphertext permissions") {
		t.Fatalf("expected staging permission error, got: %v", err)
	}
	afterStagingFail, err := os.ReadFile(cipherPath)
	if err != nil || !bytes.Equal(afterStagingFail, initialOnDisk) {
		t.Fatal("existing ciphertext changed after staging permission failure")
	}
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("leftover staging file found after chmod failure: %s", entry.Name())
		}
	}

	// 3. Successful replacement preserves final 0600 mode.
	store.chmod = os.Chmod
	replacement := []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tnew_val\n")
	if err := store.SaveValidatedCookies(context.Background(), replacement); err != nil {
		t.Fatalf("successful replacement error = %v", err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(cipherPath)
		if err != nil {
			t.Fatal(err)
		}
		if perm := info.Mode().Perm(); perm != 0600 {
			t.Fatalf("replaced yt_cookies.enc perm = %o, want 0600", perm)
		}
	}
}

func TestYouTubeCookieStoreDirectorySyncFailureSemantics(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	initial := validNetscapeCookiesFixture(time.Now())
	if err := store.SaveValidatedCookies(context.Background(), initial); err != nil {
		t.Fatal(err)
	}

	// Simulate directory sync failure after atomic rename.
	store.syncDirFn = func(string) error {
		return errors.New("dir fsync EIO")
	}
	replacement := []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t0\tLOGIN_INFO\tdurable_check\n")
	err := store.SaveValidatedCookies(context.Background(), replacement)
	if !errors.Is(err, errCookieDirSyncUnconfirmed) {
		t.Fatalf("SaveValidatedCookies() error = %v, want errCookieDirSyncUnconfirmed", err)
	}

	// Rename already committed the replacement; loading returns the new payload.
	loaded, loadErr := store.LoadDecryptedCookies(context.Background())
	if loadErr != nil || !bytes.Equal(loaded, replacement) {
		t.Fatalf("expected committed replacement to be readable, err=%v", loadErr)
	}
}

func TestYouTubeCookieStoreDeleteDirSyncFailureSemantics(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	if err := store.SaveValidatedCookies(context.Background(), validNetscapeCookiesFixture(time.Now())); err != nil {
		t.Fatal(err)
	}

	dataDir, _ := scope.DataDir()
	cipherPath := filepath.Join(dataDir, cookieCipherFile)

	// Simulate directory sync failure after os.Remove succeeds.
	store.syncDirFn = func(string) error {
		return errors.New("dir fsync EIO after remove")
	}
	deleted, err := store.Delete(context.Background())
	if !deleted || !errors.Is(err, errCookieDeleteDirSyncUnconfirmed) {
		t.Fatalf("Delete() = (%v, %v), want (true, errCookieDeleteDirSyncUnconfirmed)", deleted, err)
	}
	if _, statErr := os.Stat(cipherPath); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("expected %s to be removed despite sync failure, stat err = %v", cipherPath, statErr)
	}
	if got := store.Status(context.Background()); got != cookieStatusNotConfigured {
		t.Fatalf("Status() after delete = %q, want %q", got, cookieStatusNotConfigured)
	}

	// File already absent + syncDirFn fails must NOT return errCookieDeleteDirSyncUnconfirmed.
	deletedAbsent, errAbsent := store.Delete(context.Background())
	if deletedAbsent || errAbsent != nil {
		t.Fatalf("Delete(absent with syncDir error) = (%v, %v), want (false, nil)", deletedAbsent, errAbsent)
	}
}

func TestYouTubeCookieStoreStatusNonIntegrityErrors(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	if err := store.SaveValidatedCookies(context.Background(), validNetscapeCookiesFixture(time.Now())); err != nil {
		t.Fatal(err)
	}

	// 1. Cancelled context -> cookieStatusUnavailable (not corrupt).
	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := store.Status(cancelledCtx); got != cookieStatusUnavailable {
		t.Fatalf("Status(cancelled) = %q, want %q", got, cookieStatusUnavailable)
	}

	// 2. Timed-out context -> cookieStatusUnavailable (not corrupt).
	timeoutCtx, cancelTimeout := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancelTimeout()
	if got := store.Status(timeoutCtx); got != cookieStatusUnavailable {
		t.Fatalf("Status(timeout) = %q, want %q", got, cookieStatusUnavailable)
	}

	// 3. Missing filesystem scope -> cookieStatusUnavailable (not corrupt).
	nilScopeStore := newYouTubeCookieStore(nil, func(string) string { return key })
	if got := nilScopeStore.Status(context.Background()); got != cookieStatusUnavailable {
		t.Fatalf("Status(nil scope) = %q, want %q", got, cookieStatusUnavailable)
	}

	// 4. Unreadable file (permission denied) -> cookieStatusUnavailable (not corrupt).
	if runtime.GOOS != "windows" && os.Geteuid() != 0 {
		dataDir, _ := scope.DataDir()
		cipherPath := filepath.Join(dataDir, cookieCipherFile)
		if err := os.Chmod(cipherPath, 0000); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(cipherPath, 0600) })
		if got := store.Status(context.Background()); got != cookieStatusUnavailable {
			t.Fatalf("Status(permission denied) = %q, want %q", got, cookieStatusUnavailable)
		}
	}
}

func TestYouTubeCookieStoreAtomicReplacementFailurePreservesExisting(t *testing.T) {
	key := newTestCookieKeyBase64(t)
	scope := newTestCookieScope(t)
	store := newYouTubeCookieStore(scope, func(string) string { return key })
	initial := validNetscapeCookiesFixture(time.Now())

	if err := store.SaveValidatedCookies(context.Background(), initial); err != nil {
		t.Fatal(err)
	}
	dataDir, _ := scope.DataDir()
	cipherPath := filepath.Join(dataDir, cookieCipherFile)
	initialOnDisk, err := os.ReadFile(cipherPath)
	if err != nil {
		t.Fatal(err)
	}

	// Simulate failure after staging write/sync and before atomic rename.
	store.beforeRename = func(tmpPath string) error {
		if _, statErr := os.Stat(tmpPath); statErr != nil {
			t.Fatalf("expected staging file %s to exist before rename: %v", tmpPath, statErr)
		}
		return errors.New("simulated sync/write failure before rename")
	}
	if err := store.SaveValidatedCookies(context.Background(), []byte("new-secret-payload")); err == nil {
		t.Fatal("expected SaveValidatedCookies to fail on beforeRename error")
	}

	// Existing ciphertext must be byte-for-byte identical.
	afterFail, err := os.ReadFile(cipherPath)
	if err != nil || !bytes.Equal(afterFail, initialOnDisk) {
		t.Fatal("existing ciphertext was modified despite pre-rename failure")
	}

	// Staging temp file must be removed.
	entries, err := os.ReadDir(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp.") {
			t.Fatalf("leftover staging file found: %s", entry.Name())
		}
	}
}
