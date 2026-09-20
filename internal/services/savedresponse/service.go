package savedresponse

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/services/storage"
)

const (
	MaxPersistentMediaBytes int64 = 32 << 20
	MaxCaptionRunes               = 1024
)

var (
	ErrNilContext                  = errors.New("saved response: context is nil")
	ErrReplyNotFound               = errors.New("saved response: replied message not found")
	ErrEmptyResponse               = errors.New("saved response: response is empty")
	ErrMediaPersistenceUnavailable = errors.New("saved response: media persistence is not configured")
	ErrMediaDeliveryUnavailable    = errors.New("saved response: media delivery is not configured")
	ErrMediaTooLarge               = errors.New("saved response: media exceeds persistent limit")
	ErrNilPersist                  = errors.New("saved response: persist callback is nil")
	ErrNilDelete                   = errors.New("saved response: delete callback is nil")
)

type Service struct {
	store   storage.Storage
	files   *filesystem.Scope
	cleanup *cleanupJournal
	assets  *assetLedger
}

func NewService(store storage.Storage, dbs ...*database.DB) *Service {
	s := &Service{store: store}
	if len(dbs) > 0 && dbs[0] != nil {
		s.cleanup = newCleanupJournal(dbs[0])
		s.assets = newAssetLedger(dbs[0])
	}
	return s
}

func (s *Service) SetFiles(files *filesystem.Scope) {
	if s != nil {
		s.files = files
	}
}

func (s *Service) CaptureReply(ctx *core.Context) (Response, error) {
	if ctx == nil {
		return Response{}, ErrNilContext
	}
	reply, err := ctx.GetReply()
	if err != nil {
		return Response{}, err
	}
	if reply == nil {
		return Response{}, ErrReplyNotFound
	}
	response := NewPlainText(reply.Text)
	if reply.Media != nil && reply.Media.Location != nil {
		media, err := s.captureMedia(ctx, reply.Media)
		if err != nil {
			return Response{}, err
		}
		response.Media = media
	}
	if response.Empty() {
		return Response{}, ErrEmptyResponse
	}
	if err := Validate(response); err != nil {
		_ = s.DeleteMedia(ctx.Ctx, response)
		return Response{}, err
	}
	return response, nil
}

func (s *Service) captureMedia(ctx *core.Context, media *core.MediaInfo) (*MediaRef, error) {
	if s == nil || s.store == nil || s.files == nil {
		return nil, ErrMediaPersistenceUnavailable
	}
	if media.Size > MaxPersistentMediaBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}
	workspace, err := s.files.CreateTempDir("saved-response-capture-*")
	if err != nil {
		return nil, err
	}
	defer s.files.RemoveTempDir(workspace)

	path, err := ctx.DownloadMedia(workspace)
	if err != nil {
		return nil, err
	}
	stat, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if stat.Size() > MaxPersistentMediaBytes {
		return nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}
	mediaType := deliveryMediaType(media.Type)
	capturedName := media.FileName
	if mediaType == "sticker" {
		capturedName = filepath.Base(path)
	}
	capturedRef := &MediaRef{
		MediaType: mediaType,
		Name:      capturedName,
		MIMEType:  media.MimeType,
	}
	if capturedRef.MediaType == "sticker" {
		if err := validateCapturedStickerMetadata(media, capturedRef); err != nil {
			return nil, err
		}
		if err := validateStickerFile(path, capturedRef); err != nil {
			return nil, err
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	asset, err := s.store.Put(ctx.Ctx, f, storage.Metadata{
		Name:     capturedRef.Name,
		MIME:     media.MimeType,
		Width:    media.Width,
		Height:   media.Height,
		Duration: time.Duration(media.Duration) * time.Second,
	})
	if err != nil {
		return nil, err
	}
	if s.assets != nil {
		if err := s.assets.register(ctx.Ctx, asset.ID); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			deleteErr := s.store.Delete(cleanupCtx, asset.ID)
			cancel()
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
				return nil, errors.Join(err, fmt.Errorf("saved response: rollback untracked media asset %q: %w", asset.ID, deleteErr))
			}
			return nil, err
		}
	}
	if s.cleanup != nil {
		if err := s.cleanup.prepare(ctx.Ctx, asset.ID); err != nil {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			deleteErr := s.store.Delete(cleanupCtx, asset.ID)
			cancel()
			if s.assets != nil {
				_ = s.assets.remove(context.Background(), asset.ID)
			}
			if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
				return nil, errors.Join(err, fmt.Errorf("saved response: rollback unjournaled media asset %q: %w", asset.ID, deleteErr))
			}
			return nil, err
		}
	}
	return &MediaRef{
		AssetID:   asset.ID,
		MediaType: capturedRef.MediaType,
		Name:      asset.Name,
		MIMEType:  asset.MIME,
	}, nil
}

func (s *Service) CommitReplacement(ctx context.Context, previous, next Response, persist func() error) error {
	if persist == nil {
		return ErrNilPersist
	}
	oldID := previous.MediaAssetID()
	newID := next.MediaAssetID()
	needsOldCleanup := oldID != "" && oldID != newID

	if needsOldCleanup && s != nil && s.cleanup != nil {
		if err := s.cleanup.prepare(ctx, oldID); err != nil {
			// Refuse to drop the DB reference when we cannot durably record how
			// to reclaim the old asset.
			return err
		}
	}

	if err := persist(); err != nil {
		if needsOldCleanup && s != nil && s.cleanup != nil {
			_ = s.cleanup.remove(context.Background(), oldID)
		}
		if newID != "" && newID != oldID {
			s.cleanupUncommitted(next)
		}
		return err
	}

	if newID != "" && s != nil && s.assets != nil {
		// The feature row is already durable. Marking the ledger live is
		// best-effort; global reconciliation can reconstruct this from references.
		_ = s.assets.markSeen(ctx, newID)
	}
	if newID != "" && s != nil && s.cleanup != nil {
		// Capture installs a prepared orphan intent before handing the asset to
		// the caller. Durable feature persistence disarms that intent.
		_ = s.cleanup.remove(ctx, newID)
	}

	if needsOldCleanup {
		if s != nil && s.cleanup != nil {
			// Activation is best-effort after the DB mutation. The prepared
			// intent remains durable with a grace deadline even if activation
			// itself is interrupted, so startup reconciliation can still finish it.
			_ = s.cleanup.activate(ctx, oldID)
			_, _ = s.ReconcileCleanup(ctx, defaultCleanupBatch)
			return nil
		}
		return s.DeleteMedia(ctx, previous)
	}
	if s != nil && s.cleanup != nil {
		_, _ = s.ReconcileCleanup(ctx, defaultCleanupBatch)
	}
	return nil
}

func (s *Service) CommitDelete(ctx context.Context, response Response, remove func() error) error {
	if remove == nil {
		return ErrNilDelete
	}
	assetID := response.MediaAssetID()
	if assetID != "" && s != nil && s.cleanup != nil {
		if err := s.cleanup.prepare(ctx, assetID); err != nil {
			return err
		}
	}
	if err := remove(); err != nil {
		if assetID != "" && s != nil && s.cleanup != nil {
			_ = s.cleanup.remove(context.Background(), assetID)
		}
		return err
	}
	if assetID == "" {
		return nil
	}
	if s != nil && s.cleanup != nil {
		_ = s.cleanup.activate(ctx, assetID)
		_, _ = s.ReconcileCleanup(ctx, defaultCleanupBatch)
		return nil
	}
	return s.DeleteMedia(ctx, response)
}

func (s *Service) cleanupUncommitted(response Response) {
	if response.MediaAssetID() == "" {
		return
	}
	if s != nil && s.cleanup != nil {
		if err := s.cleanup.enqueue(context.Background(), response.MediaAssetID()); err == nil {
			_, _ = s.ReconcileCleanup(context.Background(), defaultCleanupBatch)
			return
		}
	}
	_ = s.DeleteMedia(context.Background(), response)
}

func (s *Service) PendingCleanupCount(ctx context.Context) (int, error) {
	if s == nil || s.cleanup == nil {
		return 0, nil
	}
	return s.cleanup.pendingCount(ctx)
}

type PersistentMediaReconcileStats struct {
	ReferencesBackfilled int
	ReferencesChecked    int
	MissingAssets        int
	ReferencesDetached   int
	ResponsesRemoved     int
	OrphansDiscovered    int
	CleanupScheduled     int
	Cleanup              CleanupStats
}

func (s *Service) TrackedMediaCount(ctx context.Context) (int, error) {
	if s == nil || s.assets == nil {
		return 0, nil
	}
	return s.assets.count(ctx)
}

// ReconcilePersistentMedia repairs the SavedResponse media ledger during
// quiescent startup, discovers durable orphan candidates across every known
// SavedResponse reference table, and delegates physical deletion to the
// existing cleanup journal. It intentionally ignores storage assets that are
// not in the SavedResponse ledger because the underlying storage is shared by
// other media subsystems.
func (s *Service) ReconcilePersistentMedia(ctx context.Context, limit int) (PersistentMediaReconcileStats, error) {
	var stats PersistentMediaReconcileStats
	if s == nil {
		return stats, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)

	if s.assets != nil {
		sources, schemaComplete, err := s.assets.resolveReferenceSources(ctx)
		if err != nil {
			return stats, err
		}
		backfilled, err := s.assets.backfillReferencesFromSources(ctx, sources, limit)
		if err != nil {
			return stats, err
		}
		stats.ReferencesBackfilled = backfilled

		if s.store != nil {
			referenced, err := s.assets.referencedCandidatesFromSources(ctx, sources, limit)
			if err != nil {
				return stats, err
			}
			for _, assetID := range referenced {
				stats.ReferencesChecked++
				_, statErr := s.store.Stat(ctx, assetID)
				switch {
				case statErr == nil:
					if err := s.assets.markSeen(ctx, assetID); err != nil {
						return stats, err
					}
				case errors.Is(statErr, storage.ErrNotFound):
					detached, removed, err := s.assets.repairMissingReferencesFromSources(ctx, sources, assetID)
					if err != nil {
						return stats, err
					}
					stats.MissingAssets++
					stats.ReferencesDetached += detached
					stats.ResponsesRemoved += removed
					if s.cleanup != nil {
						if err := s.cleanup.remove(ctx, assetID); err != nil {
							return stats, err
						}
					}
					if err := s.assets.remove(ctx, assetID); err != nil {
						return stats, err
					}
				default:
					return stats, fmt.Errorf("saved response: inspect persistent media asset %q: %w", assetID, statErr)
				}
			}
		}

		candidates, err := s.assets.orphanCandidatesFromSources(ctx, sources, schemaComplete, limit)
		if err != nil {
			return stats, err
		}
		stats.OrphansDiscovered = len(candidates)
		for _, assetID := range candidates {
			if s.cleanup == nil {
				break
			}
			scheduled, err := s.cleanup.scheduleDiscovered(ctx, assetID)
			if err != nil {
				return stats, err
			}
			if scheduled {
				stats.CleanupScheduled++
			}
		}
	}

	cleanupStats, err := s.ReconcileCleanup(ctx, limit)
	stats.Cleanup = cleanupStats
	return stats, err
}

func (s *Service) ReconcileCleanup(ctx context.Context, limit int) (CleanupStats, error) {
	var stats CleanupStats
	if s == nil || s.cleanup == nil {
		return stats, nil
	}
	items, err := s.cleanup.due(ctx, limit)
	if err != nil {
		return stats, err
	}
	for _, item := range items {
		stats.Scanned++
		referenced, err := s.cleanup.referenced(ctx, item.AssetID)
		if err != nil {
			if recordErr := s.cleanup.recordFailure(ctx, item, err); recordErr != nil {
				return stats, recordErr
			}
			stats.Deferred++
			continue
		}
		if referenced {
			if s.assets != nil {
				if err := s.assets.markSeen(ctx, item.AssetID); err != nil {
					if recordErr := s.cleanup.recordFailure(ctx, item, err); recordErr != nil {
						return stats, recordErr
					}
					stats.Deferred++
					continue
				}
			}
			if err := s.cleanup.remove(ctx, item.AssetID); err != nil {
				return stats, err
			}
			stats.Referenced++
			continue
		}
		if s.store == nil {
			cause := ErrMediaPersistenceUnavailable
			if err := s.cleanup.recordFailure(ctx, item, cause); err != nil {
				return stats, err
			}
			stats.Deferred++
			continue
		}

		deleteCtx := ctx
		if deleteCtx == nil {
			deleteCtx = context.Background()
		}
		deleteCtx, cancel := context.WithTimeout(context.WithoutCancel(deleteCtx), 5*time.Second)
		deleteErr := s.store.Delete(deleteCtx, item.AssetID)
		cancel()
		if deleteErr != nil && !errors.Is(deleteErr, storage.ErrNotFound) {
			if err := s.cleanup.recordFailure(ctx, item, deleteErr); err != nil {
				return stats, err
			}
			stats.Deferred++
			continue
		}
		if s.assets != nil {
			if err := s.assets.remove(ctx, item.AssetID); err != nil {
				if recordErr := s.cleanup.recordFailure(ctx, item, err); recordErr != nil {
					return stats, recordErr
				}
				stats.Deferred++
				continue
			}
		}
		if err := s.cleanup.remove(ctx, item.AssetID); err != nil {
			return stats, err
		}
		stats.Deleted++
	}
	return stats, nil
}

func (s *Service) DeleteMedia(parent context.Context, response Response) error {
	id := response.MediaAssetID()
	if id == "" || s == nil || s.store == nil {
		return nil
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	err := s.store.Delete(ctx, id)
	if err != nil && !errors.Is(err, storage.ErrNotFound) {
		return err
	}
	if s.assets != nil {
		if err := s.assets.remove(ctx, id); err != nil {
			return err
		}
	}
	if s.cleanup != nil {
		return s.cleanup.remove(ctx, id)
	}
	return nil
}

type Prepared struct {
	Text      string
	Caption   string
	MediaType string
	MediaPath string
	cleanup   func()
}

func (p *Prepared) Cleanup() {
	if p == nil || p.cleanup == nil {
		return
	}
	p.cleanup()
	p.cleanup = nil
}

func (s *Service) Prepare(ctx context.Context, response Response, vars TemplateVars) (*Prepared, error) {
	if response.Empty() {
		return nil, ErrEmptyResponse
	}
	compiled, err := Compile(response)
	if err != nil {
		return nil, err
	}
	return s.PrepareCompiled(ctx, response, compiled, vars)
}

func (s *Service) Materialize(ctx context.Context, response Response) (string, func(), error) {
	id := response.MediaAssetID()
	if id == "" {
		return "", func() {}, nil
	}
	if s == nil || s.store == nil || s.files == nil {
		return "", nil, ErrMediaDeliveryUnavailable
	}
	asset, err := s.store.Stat(ctx, id)
	if err != nil {
		return "", nil, err
	}
	if asset.Size > MaxPersistentMediaBytes {
		return "", nil, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, MaxPersistentMediaBytes)
	}

	reader, err := s.store.Open(ctx, id)
	if err != nil {
		return "", nil, err
	}
	defer reader.Close()

	ext := safeTempExtension(response.Media.Name)
	pattern := "saved-response-*"
	if ext != "" {
		pattern += ext
	}
	file, err := s.files.CreateTempFile(pattern)
	if err != nil {
		return "", nil, err
	}
	cleanup := func() { _ = s.files.RemoveTempFile(file.Name()) }
	if _, err := copyBounded(file, reader, MaxPersistentMediaBytes); err != nil {
		_ = file.Close()
		cleanup()
		return "", nil, err
	}
	if err := file.Close(); err != nil {
		cleanup()
		return "", nil, err
	}
	return file.Name(), cleanup, nil
}

func copyBounded(dst io.Writer, src io.Reader, maxBytes int64) (int64, error) {
	if maxBytes < 0 {
		return 0, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, maxBytes)
	}
	limited := &io.LimitedReader{R: src, N: maxBytes + 1}
	written, err := io.Copy(dst, limited)
	if err != nil {
		return written, err
	}
	if written > maxBytes {
		return written, fmt.Errorf("%w: max %d bytes", ErrMediaTooLarge, maxBytes)
	}
	return written, nil
}

func safeTempExtension(name string) string {
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(name)))
	if ext == "" || len(ext) > 16 {
		return ""
	}
	for i, r := range ext {
		if i == 0 {
			if r != '.' {
				return ""
			}
			continue
		}
		if (r < 'a' || r > 'z') && (r < '0' || r > '9') {
			return ""
		}
	}
	return ext
}

func deliveryMediaType(mediaType string) string {
	switch strings.ToLower(strings.TrimSpace(mediaType)) {
	case "photo":
		return "photo"
	case "sticker":
		return "sticker"
	case "audio", "voice":
		return "audio"
	case "video":
		return "video"
	default:
		return "file"
	}
}
