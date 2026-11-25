package downloader

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/inipew/goultroid/internal/platform/filesystem"
)

const (
	cookieSecretEnvVar     = "GOULTROID_YTDLP_COOKIES_KEY"
	cookieCipherFile       = "yt_cookies.enc"
	cookieEnvelopeMagic    = "GLTCK1" // 6-byte versioned header bound as AES-GCM AAD
	cookieKeyBytes         = 32       // AES-256
	cookieNonceBytes       = 12       // Standard GCM nonce size
	cookieGCMTagBytes      = 16       // AES-GCM authentication tag size
	maxCookieEnvelopeBytes = int(maxCookieFileBytes) + len(cookieEnvelopeMagic) + cookieNonceBytes + cookieGCMTagBytes
)

var (
	errCookieKeyMissing               = errors.New("youtube cookies encryption key is not configured")
	errCookieKeyInvalid               = errors.New("youtube cookies encryption key is invalid")
	errCookieDecryptFail              = errors.New("youtube cookies decryption failed")
	errCookieCorrupt                  = errors.New("youtube cookies ciphertext is corrupt or invalid")
	errCookieNotFound                 = errors.New("youtube cookies are not configured")
	errCookieStoreMissing             = errors.New("youtube cookies filesystem scope is unavailable")
	errCookieDirSyncUnconfirmed       = errors.New("youtube cookies replaced, but directory sync could not be confirmed")
	errCookieDeleteDirSyncUnconfirmed = errors.New("youtube cookies deleted, but directory sync could not be confirmed")
)

type cookieStoreStatus string

const (
	cookieStatusNotConfigured  cookieStoreStatus = "not_configured"
	cookieStatusConfigured     cookieStoreStatus = "configured"
	cookieStatusKeyUnavailable cookieStoreStatus = "key_unavailable"
	cookieStatusCorrupt        cookieStoreStatus = "corrupt"
	cookieStatusUnavailable    cookieStoreStatus = "unavailable"
)

// youtubeCookieStore manages encrypted-at-rest persistence for validated
// YouTube cookies under the plugin's scoped DataDir.
type youtubeCookieStore struct {
	mu           sync.RWMutex
	files        *filesystem.Scope
	lookupEnv    func(string) string
	chmod        func(string, os.FileMode) error
	syncDirFn    func(string) error
	beforeRename func(tmpPath string) error
}

func newYouTubeCookieStore(files *filesystem.Scope, lookupEnv func(string) string) *youtubeCookieStore {
	if lookupEnv == nil {
		lookupEnv = os.Getenv
	}
	return &youtubeCookieStore{
		files:     files,
		lookupEnv: lookupEnv,
		chmod:     os.Chmod,
		syncDirFn: syncDir,
	}
}

func (s *youtubeCookieStore) chmodPath(path string, mode os.FileMode) error {
	if s != nil && s.chmod != nil {
		return s.chmod(path, mode)
	}
	return os.Chmod(path, mode)
}

func (s *youtubeCookieStore) syncDirPath(dir string) error {
	if s != nil && s.syncDirFn != nil {
		return s.syncDirFn(dir)
	}
	return syncDir(dir)
}

func (s *youtubeCookieStore) ciphertextPath() (string, string, error) {
	if s == nil || s.files == nil {
		return "", "", errCookieStoreMissing
	}
	dataDir, err := s.files.DataDir()
	if err != nil {
		return "", "", fmt.Errorf("%w: %v", errCookieStoreMissing, err)
	}
	fullPath, err := s.files.SafePath(dataDir, cookieCipherFile)
	if err != nil {
		return "", "", err
	}
	return dataDir, fullPath, nil
}

func (s *youtubeCookieStore) loadKey() ([]byte, error) {
	if s == nil || s.lookupEnv == nil {
		return nil, errCookieKeyMissing
	}
	raw := strings.TrimSpace(s.lookupEnv(cookieSecretEnvVar))
	if raw == "" {
		return nil, errCookieKeyMissing
	}
	return decodeCookieEncryptionKey(raw)
}

func decodeCookieEncryptionKey(raw string) ([]byte, error) {
	decoders := []func(string) ([]byte, error){
		base64.StdEncoding.DecodeString,
		base64.RawStdEncoding.DecodeString,
		base64.URLEncoding.DecodeString,
		base64.RawURLEncoding.DecodeString,
		hex.DecodeString,
	}
	for _, decode := range decoders {
		b, err := decode(raw)
		if err == nil && len(b) == cookieKeyBytes {
			return b, nil
		}
		clear(b)
	}
	return nil, errCookieKeyInvalid
}

func sealCookieEnvelope(key, plaintext []byte) ([]byte, error) {
	if len(key) != cookieKeyBytes {
		return nil, errCookieKeyInvalid
	}
	if len(plaintext) == 0 {
		return nil, errCookieEmpty
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: cipher init failed", errCookieKeyInvalid)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm init failed", errCookieKeyInvalid)
	}
	if aead.NonceSize() != cookieNonceBytes {
		return nil, errCookieKeyInvalid
	}

	nonce := make([]byte, cookieNonceBytes)
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return nil, fmt.Errorf("generate nonce: %w", err)
	}

	header := []byte(cookieEnvelopeMagic)
	out := make([]byte, 0, len(header)+len(nonce)+len(plaintext)+aead.Overhead())
	out = append(out, header...)
	out = append(out, nonce...)
	out = aead.Seal(out, nonce, plaintext, header)
	return out, nil
}

func openCookieEnvelope(key, envelope []byte) ([]byte, error) {
	if len(key) != cookieKeyBytes {
		return nil, errCookieKeyInvalid
	}
	header := []byte(cookieEnvelopeMagic)
	if len(envelope) < len(header)+cookieNonceBytes {
		return nil, errCookieCorrupt
	}
	if !bytes.Equal(envelope[:len(header)], header) {
		return nil, errCookieCorrupt
	}

	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, fmt.Errorf("%w: cipher init failed", errCookieKeyInvalid)
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, fmt.Errorf("%w: gcm init failed", errCookieKeyInvalid)
	}
	minLen := len(header) + cookieNonceBytes + aead.Overhead()
	if len(envelope) < minLen {
		return nil, errCookieCorrupt
	}

	nonce := envelope[len(header) : len(header)+cookieNonceBytes]
	ciphertext := envelope[len(header)+cookieNonceBytes:]
	plaintext, err := aead.Open(nil, nonce, ciphertext, header)
	if err != nil {
		return nil, errCookieDecryptFail
	}
	if len(plaintext) == 0 {
		return nil, errCookieCorrupt
	}
	return plaintext, nil
}

// SaveValidatedCookies encrypts and atomically persists validated cookies.
// Any failure before atomic rename (key errors, permission errors, staging
// write/sync/close, or rename itself) leaves existing credentials untouched.
// Once os.Rename succeeds, the replacement is committed; if directory metadata
// sync fails afterward, errCookieDirSyncUnconfirmed is returned to indicate
// the replacement is active but crash-durability could not be confirmed.
func (s *youtubeCookieStore) SaveValidatedCookies(ctx context.Context, raw []byte) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if len(raw) == 0 {
		return errCookieEmpty
	}
	if int64(len(raw)) > maxCookieFileBytes {
		return errCookieTooLarge
	}

	key, err := s.loadKey()
	if err != nil {
		return err
	}
	defer clear(key)

	envelope, err := sealCookieEnvelope(key, raw)
	if err != nil {
		return err
	}

	dataDir, targetPath, err := s.ciphertextPath()
	if err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := s.chmodPath(dataDir, 0700); err != nil {
		return fmt.Errorf("restrict cookie directory permissions: %w", err)
	}

	var suffix [8]byte
	if _, err := io.ReadFull(rand.Reader, suffix[:]); err != nil {
		return fmt.Errorf("generate temp suffix: %w", err)
	}
	tmpPath, err := s.files.SafePath(dataDir, fmt.Sprintf("%s.tmp.%x", cookieCipherFile, suffix))
	if err != nil {
		return fmt.Errorf("resolve temp ciphertext path: %w", err)
	}
	tmpFile, err := os.OpenFile(tmpPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return fmt.Errorf("create temp ciphertext file: %w", err)
	}
	cleanupTmp := true
	defer func() {
		if cleanupTmp {
			_ = os.Remove(tmpPath)
		}
	}()

	if err := tmpFile.Chmod(0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("restrict temp ciphertext permissions: %w", err)
	}
	if err := s.chmodPath(tmpPath, 0600); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("restrict temp ciphertext permissions: %w", err)
	}
	if _, err := tmpFile.Write(envelope); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("write ciphertext: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		_ = tmpFile.Close()
		return fmt.Errorf("sync ciphertext: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("close ciphertext: %w", err)
	}
	if s.beforeRename != nil {
		if err := s.beforeRename(tmpPath); err != nil {
			return err
		}
	}
	if err := os.Rename(tmpPath, targetPath); err != nil {
		return fmt.Errorf("replace ciphertext atomically: %w", err)
	}
	cleanupTmp = false
	if err := s.syncDirPath(dataDir); err != nil {
		return fmt.Errorf("%w: %v", errCookieDirSyncUnconfirmed, err)
	}
	return nil
}

// LoadDecryptedCookies reads and decrypts the active cookie payload into memory.
// Callers are responsible for zeroing (clear) the returned slice after use.
func (s *youtubeCookieStore) LoadDecryptedCookies(ctx context.Context) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	_, targetPath, err := s.ciphertextPath()
	if err != nil {
		return nil, err
	}

	s.mu.RLock()
	defer s.mu.RUnlock()

	f, err := os.Open(targetPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, errCookieNotFound
		}
		return nil, fmt.Errorf("read ciphertext: %w", err)
	}
	defer func() { _ = f.Close() }()

	stat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat ciphertext: %w", err)
	}
	if stat.Size() > int64(maxCookieEnvelopeBytes) {
		return nil, errCookieCorrupt
	}

	envelope, err := io.ReadAll(io.LimitReader(f, int64(maxCookieEnvelopeBytes)+1))
	if err != nil {
		return nil, fmt.Errorf("read ciphertext: %w", err)
	}
	if len(envelope) > maxCookieEnvelopeBytes {
		return nil, errCookieCorrupt
	}

	key, err := s.loadKey()
	if err != nil {
		return nil, err
	}
	defer clear(key)

	return openCookieEnvelope(key, envelope)
}

// Status checks the local health state of the encrypted cookie store without
// exposing any secret material. Non-integrity errors (cancelled/timed-out
// context, missing scope, or filesystem I/O/permission errors) map to
// cookieStatusUnavailable rather than cookieStatusCorrupt.
func (s *youtubeCookieStore) Status(ctx context.Context) cookieStoreStatus {
	plaintext, err := s.LoadDecryptedCookies(ctx)
	if plaintext != nil {
		clear(plaintext)
	}
	switch {
	case err == nil:
		return cookieStatusConfigured
	case errors.Is(err, errCookieNotFound):
		return cookieStatusNotConfigured
	case errors.Is(err, errCookieKeyMissing), errors.Is(err, errCookieKeyInvalid):
		return cookieStatusKeyUnavailable
	case errors.Is(err, errCookieCorrupt), errors.Is(err, errCookieDecryptFail):
		return cookieStatusCorrupt
	default:
		return cookieStatusUnavailable
	}
}

// Delete explicitly removes the active encrypted cookie file and reports
// whether a file was actually removed.
// If the file does not exist (os.ErrNotExist), Delete returns (false, nil)
// without syncing the directory.
// If os.Remove succeeds and directory metadata sync fails afterward, Delete
// returns (true, errCookieDeleteDirSyncUnconfirmed).
func (s *youtubeCookieStore) Delete(ctx context.Context) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	dataDir, targetPath, err := s.ciphertextPath()
	if err != nil {
		return false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	if err := os.Remove(targetPath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("remove ciphertext: %w", err)
	}
	if err := s.syncDirPath(dataDir); err != nil {
		return true, fmt.Errorf("%w: %v", errCookieDeleteDirSyncUnconfirmed, err)
	}
	return true, nil
}

func syncDir(dir string) error {
	d, err := os.Open(filepath.Clean(dir))
	if err != nil {
		return err
	}
	defer func() { _ = d.Close() }()
	return d.Sync()
}

var _ cookieIngestionSink = (*youtubeCookieStore)(nil)
