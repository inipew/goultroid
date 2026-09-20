package savedresponse

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
)

const (
	savedResponseRegistryProducer = "savedresponse.capture"
	savedResponseRegistryOwner    = "savedresponse"
)

type registryReferenceSource struct {
	table     string
	keyColumn string
	subsystem string
	kind      string
}

var (
	noteRegistryReferenceSource   = registryReferenceSource{table: "notes", keyColumn: "name", subsystem: "notes", kind: "note"}
	filterRegistryReferenceSource = registryReferenceSource{table: "filters", keyColumn: "keyword", subsystem: "filters", kind: "filter"}
	registryReferenceSources      = []registryReferenceSource{noteRegistryReferenceSource, filterRegistryReferenceSource}
)

// RegistryCompatibilityStats summarizes one bounded compatibility pass. All
// mutations are registry metadata only; SavedResponse cleanup remains the
// authority for physical deletion during P3-B migration.
type RegistryCompatibilityStats struct {
	AssetsBackfilled       int
	ReferencesBackfilled   int
	StaleReferencesRemoved int
	StaleAssetsRemoved     int
	Consistency            RegistryConsistencyStats
}

// RegistryConsistencyStats describes divergence between the P3-A SavedResponse
// ledger/reference sources and the global media registry.
type RegistryConsistencyStats struct {
	MissingGlobalAssets       int
	AssetMetadataMismatches   int
	SourceAssetsMissingLedger int
	MissingGlobalReferences   int
	GlobalAssetsWithoutLedger int
	StaleGlobalReferences     int
	FindingsTruncated         bool
}

func (s RegistryConsistencyStats) Consistent() bool {
	return s.MissingGlobalAssets == 0 &&
		s.AssetMetadataMismatches == 0 &&
		s.SourceAssetsMissingLedger == 0 &&
		s.MissingGlobalReferences == 0 &&
		s.GlobalAssetsWithoutLedger == 0 &&
		s.StaleGlobalReferences == 0
}

// MediaRegistryAssetRegistration returns the canonical global ownership
// metadata for a SavedResponse-owned persistent asset.
func MediaRegistryAssetRegistration(assetID string) mediaregistry.AssetRegistration {
	return mediaregistry.AssetRegistration{
		AssetID:   strings.TrimSpace(assetID),
		Producer:  savedResponseRegistryProducer,
		Owner:     savedResponseRegistryOwner,
		Lifecycle: mediaregistry.LifecyclePersistent,
	}
}

func savedResponseAssetRegistration(assetID string) mediaregistry.AssetRegistration {
	return MediaRegistryAssetRegistration(assetID)
}

// EnsureLegacyMediaAssetWithExecutor mirrors the P3-A ledger write inside a
// caller-owned transaction. Removing ledger rows remains the cleanup journal's
// responsibility after physical reclamation succeeds.
func EnsureLegacyMediaAssetWithExecutor(
	ctx context.Context,
	exec database.SQLExecutor,
	assetID string,
) error {
	if exec == nil {
		return mediaregistry.ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	assetID = strings.TrimSpace(assetID)
	if assetID == "" {
		return mediaregistry.ErrInvalidAsset
	}
	now := time.Now().UTC()
	if _, err := exec.ExecContext(ctx, `
		INSERT INTO saved_response_media_assets (asset_id, registered_at, last_seen_at)
		VALUES (?, ?, ?)
		ON CONFLICT(asset_id) DO UPDATE SET last_seen_at = excluded.last_seen_at
	`, assetID, now, now); err != nil {
		return fmt.Errorf("saved response: ensure legacy media asset %q: %w", assetID, err)
	}
	return nil
}

func registryReferenceKey(chatID int64, key string) string {
	return strconv.FormatInt(chatID, 10) + ":" + key
}

func registryReference(source registryReferenceSource, assetID string, chatID int64, key string) mediaregistry.Reference {
	return mediaregistry.Reference{
		AssetID:   strings.TrimSpace(assetID),
		Subsystem: source.subsystem,
		Kind:      source.kind,
		Key:       registryReferenceKey(chatID, key),
	}
}

// NoteMediaRegistryReference returns the canonical global reference identity
// for one Notes row.
func NoteMediaRegistryReference(assetID string, chatID int64, name string) mediaregistry.Reference {
	return registryReference(noteRegistryReferenceSource, assetID, chatID, name)
}

// FilterMediaRegistryReference returns the canonical global reference identity
// for one Filters row.
func FilterMediaRegistryReference(assetID string, chatID int64, keyword string) mediaregistry.Reference {
	return registryReference(filterRegistryReferenceSource, assetID, chatID, keyword)
}

// ReconcileRegistryCompatibility mirrors the current P3-A ledger and
// Notes/Filters references into the global registry. It is bounded, idempotent,
// and never performs physical storage reclamation.
func ReconcileRegistryCompatibility(
	ctx context.Context,
	db *database.DB,
	limit int,
) (RegistryCompatibilityStats, error) {
	var stats RegistryCompatibilityStats
	if db == nil {
		return stats, mediaregistry.ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)

	ready, err := mediaregistry.SchemaReady(ctx, db)
	if err != nil {
		return stats, err
	}
	if !ready {
		return stats, fmt.Errorf("saved response: global media registry schema is unavailable")
	}

	stats.AssetsBackfilled, err = backfillRegistryAssets(ctx, db, limit)
	if err != nil {
		return stats, err
	}
	stats.ReferencesBackfilled, err = backfillRegistryReferences(ctx, db, limit)
	if err != nil {
		return stats, err
	}
	stats.StaleReferencesRemoved, err = pruneStaleRegistryReferences(ctx, db, limit)
	if err != nil {
		return stats, err
	}
	stats.StaleAssetsRemoved, err = pruneStaleRegistryAssets(ctx, db, limit)
	if err != nil {
		return stats, err
	}
	stats.Consistency, err = VerifyRegistryCompatibility(ctx, db, limit)
	return stats, err
}

func backfillRegistryAssets(ctx context.Context, db *database.DB, limit int) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT a.asset_id
		FROM saved_response_media_assets a
		WHERE NOT EXISTS (
			SELECT 1 FROM media_assets m WHERE m.asset_id = a.asset_id
		)
		ORDER BY a.registered_at ASC, a.asset_id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("saved response: list global registry asset backfill: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			return 0, err
		}
		ids = append(ids, assetID)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, assetID := range ids {
		if err := mediaregistry.RegisterAssetWithExecutor(ctx, db, savedResponseAssetRegistration(assetID)); err != nil {
			return 0, fmt.Errorf("saved response: backfill global asset %q: %w", assetID, err)
		}
	}
	return len(ids), nil
}

type registryReferenceCandidate struct {
	assetID string
	chatID  int64
	key     string
}

func backfillRegistryReferences(ctx context.Context, db *database.DB, limit int) (int, error) {
	inserted := 0
	quota := max(1, limit/len(registryReferenceSources))
	for _, source := range registryReferenceSources {
		remaining := limit - inserted
		if remaining <= 0 {
			break
		}
		n, err := backfillRegistryReferenceSource(ctx, db, source, min(quota, remaining))
		if err != nil {
			return inserted, err
		}
		inserted += n
	}
	for inserted < limit {
		progress := 0
		for _, source := range registryReferenceSources {
			remaining := limit - inserted
			if remaining <= 0 {
				break
			}
			n, err := backfillRegistryReferenceSource(ctx, db, source, remaining)
			if err != nil {
				return inserted, err
			}
			inserted += n
			progress += n
		}
		if progress == 0 {
			break
		}
	}
	return inserted, nil
}

func backfillRegistryReferenceSource(
	ctx context.Context,
	db *database.DB,
	source registryReferenceSource,
	limit int,
) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	query := fmt.Sprintf(`
		SELECT TRIM(r.media_asset_id), r.chat_id, r.%s
		FROM %s r
		WHERE TRIM(r.media_asset_id) <> ''
		  AND NOT EXISTS (
			SELECT 1 FROM media_asset_references mr
			WHERE mr.asset_id = TRIM(r.media_asset_id)
			  AND mr.subsystem = ?
			  AND mr.reference_kind = ?
			  AND mr.reference_key = CAST(r.chat_id AS TEXT) || ':' || r.%s
		  )
		ORDER BY r.chat_id ASC, r.%s ASC
		LIMIT ?
	`, source.keyColumn, source.table, source.keyColumn, source.keyColumn)
	rows, err := db.QueryContext(ctx, query, source.subsystem, source.kind, limit)
	if err != nil {
		return 0, fmt.Errorf("saved response: list %s global reference backfill: %w", source.table, err)
	}
	defer rows.Close()

	candidates := make([]registryReferenceCandidate, 0, limit)
	for rows.Next() {
		var candidate registryReferenceCandidate
		if err := rows.Scan(&candidate.assetID, &candidate.chatID, &candidate.key); err != nil {
			return 0, err
		}
		candidates = append(candidates, candidate)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, candidate := range candidates {
		ref := registryReference(source, candidate.assetID, candidate.chatID, candidate.key)
		if err := mediaregistry.RegisterAssetWithExecutor(
			ctx,
			db,
			savedResponseAssetRegistration(candidate.assetID),
			ref,
		); err != nil {
			return 0, fmt.Errorf("saved response: backfill %s reference %q: %w", source.table, ref.Key, err)
		}
	}
	return len(candidates), nil
}

func staleRegistryReferencePredicate() string {
	clauses := make([]string, 0, len(registryReferenceSources))
	for _, source := range registryReferenceSources {
		clauses = append(clauses, fmt.Sprintf(`(
			mr.subsystem = '%s' AND mr.reference_kind = '%s'
			AND NOT EXISTS (
				SELECT 1 FROM %s r
				WHERE TRIM(r.media_asset_id) = mr.asset_id
				  AND CAST(r.chat_id AS TEXT) || ':' || r.%s = mr.reference_key
			)
		)`, source.subsystem, source.kind, source.table, source.keyColumn))
	}
	return strings.Join(clauses, " OR ")
}

func staleRegistryReferenceQuery(selectList string) string {
	return `
		SELECT ` + selectList + `
		FROM media_asset_references mr
		WHERE ` + staleRegistryReferencePredicate() + `
		ORDER BY mr.subsystem ASC, mr.reference_kind ASC, mr.reference_key ASC, mr.asset_id ASC
		LIMIT ?
	`
}

func pruneStaleRegistryReferences(ctx context.Context, db *database.DB, limit int) (int, error) {
	rows, err := db.QueryContext(ctx, staleRegistryReferenceQuery("mr.asset_id, mr.subsystem, mr.reference_kind, mr.reference_key"), limit)
	if err != nil {
		return 0, fmt.Errorf("saved response: list stale global references: %w", err)
	}
	defer rows.Close()

	refs := make([]mediaregistry.Reference, 0, limit)
	for rows.Next() {
		var ref mediaregistry.Reference
		if err := rows.Scan(&ref.AssetID, &ref.Subsystem, &ref.Kind, &ref.Key); err != nil {
			return 0, err
		}
		refs = append(refs, ref)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, ref := range refs {
		if err := mediaregistry.RemoveReferenceWithExecutor(ctx, db, ref); err != nil {
			return 0, err
		}
	}
	return len(refs), nil
}

func pruneStaleRegistryAssets(ctx context.Context, db *database.DB, limit int) (int, error) {
	rows, err := db.QueryContext(ctx, `
		SELECT m.asset_id
		FROM media_assets m
		WHERE m.owner = ? AND m.producer = ?
		  AND NOT EXISTS (
			SELECT 1 FROM saved_response_media_assets a WHERE a.asset_id = m.asset_id
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM media_asset_references mr WHERE mr.asset_id = m.asset_id
		  )
		ORDER BY m.registered_at ASC, m.asset_id ASC
		LIMIT ?
	`, savedResponseRegistryOwner, savedResponseRegistryProducer, limit)
	if err != nil {
		return 0, fmt.Errorf("saved response: list stale global assets: %w", err)
	}
	defer rows.Close()

	ids := make([]string, 0, limit)
	for rows.Next() {
		var assetID string
		if err := rows.Scan(&assetID); err != nil {
			return 0, err
		}
		ids = append(ids, assetID)
	}
	if err := rows.Err(); err != nil {
		return 0, err
	}
	for _, assetID := range ids {
		if err := mediaregistry.RemoveOwnedAssetWithExecutor(ctx, db, assetID, savedResponseRegistryOwner); err != nil {
			return 0, err
		}
	}
	return len(ids), nil
}

// VerifyRegistryCompatibility performs bounded, read-only consistency checks
// across the P3-A ledger, Notes/Filters rows, and the global registry.
func VerifyRegistryCompatibility(
	ctx context.Context,
	db *database.DB,
	limit int,
) (RegistryConsistencyStats, error) {
	var stats RegistryConsistencyStats
	if db == nil {
		return stats, mediaregistry.ErrNilDatabase
	}
	if ctx == nil {
		ctx = context.Background()
	}
	limit = normalizePersistentReconcileLimit(limit)

	ready, err := mediaregistry.SchemaReady(ctx, db)
	if err != nil {
		return stats, err
	}
	if !ready {
		return stats, fmt.Errorf("saved response: global media registry schema is unavailable")
	}

	stats.MissingGlobalAssets, stats.FindingsTruncated, err = boundedFindingCount(ctx, db, `
		SELECT 1
		FROM saved_response_media_assets a
		WHERE NOT EXISTS (SELECT 1 FROM media_assets m WHERE m.asset_id = a.asset_id)
		ORDER BY a.registered_at ASC, a.asset_id ASC
		LIMIT ?
	`, limit)
	if err != nil {
		return stats, fmt.Errorf("saved response: verify missing global assets: %w", err)
	}

	var truncated bool
	stats.AssetMetadataMismatches, truncated, err = boundedFindingCount(ctx, db, `
		SELECT 1
		FROM saved_response_media_assets a
		JOIN media_assets m ON m.asset_id = a.asset_id
		WHERE m.producer <> ? OR m.owner <> ? OR m.lifecycle <> ?
		ORDER BY a.registered_at ASC, a.asset_id ASC
		LIMIT ?
	`, limit, savedResponseRegistryProducer, savedResponseRegistryOwner, mediaregistry.LifecyclePersistent)
	stats.FindingsTruncated = stats.FindingsTruncated || truncated
	if err != nil {
		return stats, fmt.Errorf("saved response: verify global asset metadata: %w", err)
	}

	for _, source := range registryReferenceSources {
		missingLedgerQuery := fmt.Sprintf(`
			SELECT 1
			FROM %s r
			WHERE TRIM(r.media_asset_id) <> ''
			  AND NOT EXISTS (
				SELECT 1 FROM saved_response_media_assets a
				WHERE a.asset_id = TRIM(r.media_asset_id)
			  )
			ORDER BY r.chat_id ASC, r.%s ASC
			LIMIT ?
		`, source.table, source.keyColumn)
		count, sourceTruncated, countErr := boundedFindingCount(ctx, db, missingLedgerQuery, limit)
		if countErr != nil {
			return stats, fmt.Errorf("saved response: verify %s ledger references: %w", source.table, countErr)
		}
		stats.SourceAssetsMissingLedger += count
		stats.FindingsTruncated = stats.FindingsTruncated || sourceTruncated

		missingReferenceQuery := fmt.Sprintf(`
			SELECT 1
			FROM %s r
			WHERE TRIM(r.media_asset_id) <> ''
			  AND NOT EXISTS (
				SELECT 1 FROM media_asset_references mr
				WHERE mr.asset_id = TRIM(r.media_asset_id)
				  AND mr.subsystem = ?
				  AND mr.reference_kind = ?
				  AND mr.reference_key = CAST(r.chat_id AS TEXT) || ':' || r.%s
			  )
			ORDER BY r.chat_id ASC, r.%s ASC
			LIMIT ?
		`, source.table, source.keyColumn, source.keyColumn)
		count, sourceTruncated, countErr = boundedFindingCount(
			ctx,
			db,
			missingReferenceQuery,
			limit,
			source.subsystem,
			source.kind,
		)
		if countErr != nil {
			return stats, fmt.Errorf("saved response: verify %s global references: %w", source.table, countErr)
		}
		stats.MissingGlobalReferences += count
		stats.FindingsTruncated = stats.FindingsTruncated || sourceTruncated
	}

	stats.GlobalAssetsWithoutLedger, truncated, err = boundedFindingCount(ctx, db, `
		SELECT 1
		FROM media_assets m
		WHERE m.owner = ? AND m.producer = ?
		  AND NOT EXISTS (
			SELECT 1 FROM saved_response_media_assets a WHERE a.asset_id = m.asset_id
		  )
		ORDER BY m.registered_at ASC, m.asset_id ASC
		LIMIT ?
	`, limit, savedResponseRegistryOwner, savedResponseRegistryProducer)
	stats.FindingsTruncated = stats.FindingsTruncated || truncated
	if err != nil {
		return stats, fmt.Errorf("saved response: verify global assets without ledger: %w", err)
	}

	stats.StaleGlobalReferences, truncated, err = boundedFindingCount(
		ctx,
		db,
		staleRegistryReferenceQuery("1"),
		limit,
	)
	stats.FindingsTruncated = stats.FindingsTruncated || truncated
	if err != nil {
		return stats, fmt.Errorf("saved response: verify stale global references: %w", err)
	}
	return stats, nil
}

func boundedFindingCount(
	ctx context.Context,
	db *database.DB,
	query string,
	limit int,
	args ...any,
) (count int, truncated bool, err error) {
	queryArgs := append([]any(nil), args...)
	queryArgs = append(queryArgs, limit+1)
	rows, err := db.QueryContext(ctx, query, queryArgs...)
	if err != nil {
		return 0, false, err
	}
	defer rows.Close()
	for rows.Next() {
		count++
		if count > limit {
			truncated = true
			count = limit
			break
		}
	}
	if err := rows.Err(); err != nil {
		return 0, false, err
	}
	return count, truncated, nil
}
