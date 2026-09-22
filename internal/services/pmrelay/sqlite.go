package pmrelay

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

type SQLiteRepository struct {
	db     *database.DB
	limits Limits
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return NewSQLiteRepositoryWithLimits(db, DefaultLimits())
}

func NewSQLiteRepositoryWithLimits(db *database.DB, limits Limits) *SQLiteRepository {
	return &SQLiteRepository{db: db, limits: limits.normalized()}
}

func (r *SQLiteRepository) EnsureMapping(ctx context.Context, mapping Mapping) (Mapping, error) {
	if r == nil || r.db == nil {
		return Mapping{}, ErrUnavailable
	}
	normalized, err := mapping.Normalize()
	if err != nil {
		return Mapping{}, err
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO pm_relay_mappings (
			owner_chat_id, owner_message_id, visitor_user_id, visitor_message_id,
			created_at, expires_at
		)
		SELECT ?, ?, ?, ?, ?, ?
		WHERE (SELECT count(*) FROM pm_relay_mappings) < ?
	`, normalized.OwnerChatID, normalized.OwnerMessageID, normalized.VisitorUserID,
		normalized.VisitorMessageID, normalized.CreatedAt, normalized.ExpiresAt, r.limits.Mappings)
	if err == nil {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return Mapping{}, fmt.Errorf("inspect pm relay mapping insert: %w", rowsErr)
		}
		if affected == 1 {
			return normalized, nil
		}
	}
	if existing, resolveErr := r.resolveMappingIdentity(ctx, normalized); resolveErr == nil {
		return existing, nil
	} else if !errors.Is(resolveErr, ErrMappingNotFound) {
		return Mapping{}, resolveErr
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("create pm relay mapping: %w", err)
	}
	return Mapping{}, ErrMappingCapacity
}

func (r *SQLiteRepository) resolveMappingIdentity(ctx context.Context, expected Mapping) (Mapping, error) {
	byOwner, ownerErr := r.GetMapping(ctx, expected.OwnerChatID, expected.OwnerMessageID)
	if ownerErr == nil {
		if sameMappingIdentity(byOwner, expected) {
			return byOwner, nil
		}
		return Mapping{}, ErrMappingConflict
	}
	if ownerErr != nil && !errors.Is(ownerErr, ErrMappingNotFound) {
		return Mapping{}, ownerErr
	}

	byVisitor, visitorErr := r.GetMappingByVisitorMessage(ctx, expected.VisitorUserID, expected.VisitorMessageID)
	if visitorErr == nil {
		if sameMappingIdentity(byVisitor, expected) {
			return byVisitor, nil
		}
		return Mapping{}, ErrMappingConflict
	}
	if visitorErr != nil && !errors.Is(visitorErr, ErrMappingNotFound) {
		return Mapping{}, visitorErr
	}
	return Mapping{}, ErrMappingNotFound
}

func sameMappingIdentity(left, right Mapping) bool {
	return left.OwnerChatID == right.OwnerChatID &&
		left.OwnerMessageID == right.OwnerMessageID &&
		left.VisitorUserID == right.VisitorUserID &&
		left.VisitorMessageID == right.VisitorMessageID
}

func (r *SQLiteRepository) GetMapping(ctx context.Context, ownerChatID int64, ownerMessageID int) (Mapping, error) {
	if r == nil || r.db == nil {
		return Mapping{}, ErrUnavailable
	}
	if ownerChatID <= 0 || ownerMessageID <= 0 {
		return Mapping{}, ErrInvalidMapping
	}
	mapping, err := scanMapping(r.db.QueryRowContext(ctx, `
		SELECT owner_chat_id, owner_message_id, visitor_user_id, visitor_message_id,
		       created_at, expires_at
		FROM pm_relay_mappings
		WHERE owner_chat_id = ? AND owner_message_id = ?
	`, ownerChatID, ownerMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, ErrMappingNotFound
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("get pm relay mapping: %w", err)
	}
	return mapping, nil
}

func (r *SQLiteRepository) GetMappingByVisitorMessage(ctx context.Context, visitorUserID int64, visitorMessageID int) (Mapping, error) {
	if r == nil || r.db == nil {
		return Mapping{}, ErrUnavailable
	}
	if visitorUserID <= 0 || visitorMessageID <= 0 {
		return Mapping{}, ErrInvalidMapping
	}
	mapping, err := scanMapping(r.db.QueryRowContext(ctx, `
		SELECT owner_chat_id, owner_message_id, visitor_user_id, visitor_message_id,
		       created_at, expires_at
		FROM pm_relay_mappings
		WHERE visitor_user_id = ? AND visitor_message_id = ?
	`, visitorUserID, visitorMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return Mapping{}, ErrMappingNotFound
	}
	if err != nil {
		return Mapping{}, fmt.Errorf("get pm relay mapping by visitor message: %w", err)
	}
	return mapping, nil
}

func (r *SQLiteRepository) PruneExpiredMappings(ctx context.Context, now time.Time, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrUnavailable
	}
	limit = normalizePruneLimit(limit)
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM pm_relay_mappings
		WHERE rowid IN (
			SELECT rowid FROM pm_relay_mappings
			WHERE expires_at <= ?
			ORDER BY expires_at ASC, owner_chat_id ASC, owner_message_id ASC
			LIMIT ?
		)
	`, now.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("prune pm relay mappings: %w", err)
	}
	return affectedRows(result)
}

func (r *SQLiteRepository) CountMappings(ctx context.Context) (int, error) {
	return r.count(ctx, "pm_relay_mappings")
}

func (r *SQLiteRepository) EnsureDelivery(ctx context.Context, intent DeliveryIntent) (DeliveryIntent, error) {
	if r == nil || r.db == nil {
		return DeliveryIntent{}, ErrUnavailable
	}
	normalized, err := intent.Normalize()
	if err != nil {
		return DeliveryIntent{}, err
	}
	if normalized.ClaimID != "" || normalized.ClaimExpiresAt != nil || normalized.Attempts != 0 ||
		normalized.LastError != "" || normalized.TargetMessageID != 0 || normalized.DeliveredAt != nil {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO pm_relay_deliveries (
			direction, source_chat_id, source_message_id, target_chat_id, random_id,
			target_message_id, claim_id, claim_expires_at, attempts, last_error,
			created_at, updated_at, delivered_at, expires_at
		)
		SELECT ?, ?, ?, ?, ?, 0, '', NULL, 0, '', ?, ?, NULL, ?
		WHERE (SELECT count(*) FROM pm_relay_deliveries) < ?
	`, string(normalized.Direction), normalized.SourceChatID, normalized.SourceMessageID,
		normalized.TargetChatID, normalized.RandomID, normalized.CreatedAt, normalized.UpdatedAt,
		normalized.ExpiresAt, r.limits.Deliveries)
	if err == nil {
		affected, rowsErr := result.RowsAffected()
		if rowsErr != nil {
			return DeliveryIntent{}, fmt.Errorf("inspect pm relay delivery insert: %w", rowsErr)
		}
		if affected == 1 {
			return normalized, nil
		}
	}
	existing, getErr := r.GetDelivery(ctx, normalized.DeliveryKey)
	if getErr == nil {
		if existing.TargetChatID != normalized.TargetChatID {
			return DeliveryIntent{}, ErrDeliveryConflict
		}
		// Existing source identity is authoritative. In particular, RandomID must
		// be reused across crash/retry instead of accepting a newly generated ID.
		return existing, nil
	}
	if getErr != nil && !errors.Is(getErr, ErrDeliveryNotFound) {
		return DeliveryIntent{}, getErr
	}
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("create pm relay delivery: %w", err)
	}
	return DeliveryIntent{}, ErrDeliveryCapacity
}

func (r *SQLiteRepository) GetDelivery(ctx context.Context, key DeliveryKey) (DeliveryIntent, error) {
	if r == nil || r.db == nil {
		return DeliveryIntent{}, ErrUnavailable
	}
	normalized, err := key.Normalize()
	if err != nil {
		return DeliveryIntent{}, err
	}
	intent, err := scanDelivery(r.db.QueryRowContext(ctx, `
		SELECT direction, source_chat_id, source_message_id, target_chat_id, random_id,
		       target_message_id, claim_id, claim_expires_at, attempts, last_error,
		       created_at, updated_at, delivered_at, expires_at
		FROM pm_relay_deliveries
		WHERE direction = ? AND source_chat_id = ? AND source_message_id = ?
	`, string(normalized.Direction), normalized.SourceChatID, normalized.SourceMessageID))
	if errors.Is(err, sql.ErrNoRows) {
		return DeliveryIntent{}, ErrDeliveryNotFound
	}
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("get pm relay delivery: %w", err)
	}
	return intent, nil
}

func (r *SQLiteRepository) ClaimDelivery(
	ctx context.Context,
	key DeliveryKey,
	now time.Time,
	claimID string,
	claimExpiresAt time.Time,
) (DeliveryIntent, error) {
	if r == nil || r.db == nil {
		return DeliveryIntent{}, ErrUnavailable
	}
	normalized, err := key.Normalize()
	if err != nil {
		return DeliveryIntent{}, err
	}
	now = now.UTC()
	claimExpiresAt = claimExpiresAt.UTC()
	claimID = strings.TrimSpace(claimID)
	if now.IsZero() || claimID == "" || len(claimID) > MaxClaimIDBytes || !claimExpiresAt.After(now) || claimExpiresAt.Sub(now) > DeliveryClaimTTL {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE pm_relay_deliveries
		SET claim_id = ?, claim_expires_at = ?, attempts = attempts + 1,
		    last_error = '', updated_at = ?
		WHERE direction = ? AND source_chat_id = ? AND source_message_id = ?
		  AND delivered_at IS NULL
		  AND expires_at > ?
		  AND (claim_id = '' OR claim_expires_at IS NULL OR claim_expires_at <= ?)
	`, claimID, claimExpiresAt, now, string(normalized.Direction), normalized.SourceChatID,
		normalized.SourceMessageID, now, now)
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("claim pm relay delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DeliveryIntent{}, err
	}
	if affected == 1 {
		return r.GetDelivery(ctx, normalized)
	}
	existing, err := r.GetDelivery(ctx, normalized)
	if err != nil {
		return DeliveryIntent{}, err
	}
	if existing.Completed() {
		return DeliveryIntent{}, ErrDeliveryCompleted
	}
	if existing.Expired(now) {
		return DeliveryIntent{}, ErrDeliveryExpired
	}
	if existing.ClaimID != "" && existing.ClaimExpiresAt != nil && existing.ClaimExpiresAt.After(now) {
		return DeliveryIntent{}, ErrDeliveryClaimed
	}
	return DeliveryIntent{}, ErrDeliveryConflict
}

func (r *SQLiteRepository) CommitDelivery(
	ctx context.Context,
	key DeliveryKey,
	claimID string,
	targetMessageID int,
	deliveredAt time.Time,
) (DeliveryIntent, error) {
	if r == nil || r.db == nil {
		return DeliveryIntent{}, ErrUnavailable
	}
	normalized, err := key.Normalize()
	if err != nil {
		return DeliveryIntent{}, err
	}
	claimID = strings.TrimSpace(claimID)
	deliveredAt = deliveredAt.UTC()
	if claimID == "" || len(claimID) > MaxClaimIDBytes || targetMessageID <= 0 || deliveredAt.IsZero() {
		return DeliveryIntent{}, ErrInvalidDelivery
	}
	result, err := r.db.ExecContext(ctx, `
		UPDATE pm_relay_deliveries
		SET target_message_id = ?, delivered_at = ?, claim_id = '', claim_expires_at = NULL,
		    last_error = '', updated_at = ?
		WHERE direction = ? AND source_chat_id = ? AND source_message_id = ?
		  AND delivered_at IS NULL AND claim_id = ?
	`, targetMessageID, deliveredAt, deliveredAt, string(normalized.Direction), normalized.SourceChatID,
		normalized.SourceMessageID, claimID)
	if err != nil {
		return DeliveryIntent{}, fmt.Errorf("commit pm relay delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return DeliveryIntent{}, err
	}
	if affected == 1 {
		return r.GetDelivery(ctx, normalized)
	}
	existing, err := r.GetDelivery(ctx, normalized)
	if err != nil {
		return DeliveryIntent{}, err
	}
	if existing.Completed() {
		if existing.TargetMessageID != targetMessageID {
			return DeliveryIntent{}, ErrDeliveryConflict
		}
		return existing, nil
	}
	return DeliveryIntent{}, ErrDeliveryClaimed
}

func (r *SQLiteRepository) ReleaseDelivery(
	ctx context.Context,
	key DeliveryKey,
	claimID string,
	now time.Time,
	lastError string,
) error {
	if r == nil || r.db == nil {
		return ErrUnavailable
	}
	normalized, err := key.Normalize()
	if err != nil {
		return err
	}
	claimID = strings.TrimSpace(claimID)
	now = now.UTC()
	if claimID == "" || len(claimID) > MaxClaimIDBytes || now.IsZero() {
		return ErrInvalidDelivery
	}
	lastError = boundedDeliveryError(lastError)
	result, err := r.db.ExecContext(ctx, `
		UPDATE pm_relay_deliveries
		SET claim_id = '', claim_expires_at = NULL, last_error = ?, updated_at = ?
		WHERE direction = ? AND source_chat_id = ? AND source_message_id = ?
		  AND delivered_at IS NULL AND claim_id = ?
	`, lastError, now, string(normalized.Direction), normalized.SourceChatID, normalized.SourceMessageID, claimID)
	if err != nil {
		return fmt.Errorf("release pm relay delivery: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 1 {
		return nil
	}
	existing, err := r.GetDelivery(ctx, normalized)
	if err != nil {
		return err
	}
	if existing.Completed() {
		return nil
	}
	return ErrDeliveryClaimed
}

func (r *SQLiteRepository) PruneExpiredDeliveries(ctx context.Context, now time.Time, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrUnavailable
	}
	now = now.UTC()
	limit = normalizePruneLimit(limit)
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM pm_relay_deliveries
		WHERE rowid IN (
			SELECT rowid FROM pm_relay_deliveries
			WHERE expires_at <= ?
			  AND (claim_id = '' OR claim_expires_at IS NULL OR claim_expires_at <= ?)
			ORDER BY expires_at ASC, direction ASC, source_chat_id ASC, source_message_id ASC
			LIMIT ?
		)
	`, now, now, limit)
	if err != nil {
		return 0, fmt.Errorf("prune pm relay deliveries: %w", err)
	}
	return affectedRows(result)
}

func (r *SQLiteRepository) CountDeliveries(ctx context.Context) (int, error) {
	return r.count(ctx, "pm_relay_deliveries")
}

func (r *SQLiteRepository) TouchAudience(ctx context.Context, touch AudienceTouch) (AudienceMember, error) {
	if r == nil || r.db == nil {
		return AudienceMember{}, ErrUnavailable
	}
	normalized, err := touch.Normalize()
	if err != nil {
		return AudienceMember{}, err
	}
	result, err := r.db.ExecContext(ctx, `
		INSERT INTO assistant_audience_members (user_id, sources, first_seen_at, last_seen_at)
		SELECT ?, ?, ?, ?
		WHERE EXISTS (
			SELECT 1 FROM assistant_audience_members WHERE user_id = ?
		) OR (
			SELECT count(*) FROM assistant_audience_members
		) < ?
		ON CONFLICT(user_id) DO UPDATE SET
			sources = assistant_audience_members.sources | excluded.sources,
			first_seen_at = CASE
				WHEN excluded.first_seen_at < assistant_audience_members.first_seen_at THEN excluded.first_seen_at
				ELSE assistant_audience_members.first_seen_at
			END,
			last_seen_at = CASE
				WHEN excluded.last_seen_at > assistant_audience_members.last_seen_at THEN excluded.last_seen_at
				ELSE assistant_audience_members.last_seen_at
			END
	`, normalized.UserID, int64(normalized.Source), normalized.SeenAt, normalized.SeenAt,
		normalized.UserID, r.limits.Audience)
	if err != nil {
		return AudienceMember{}, fmt.Errorf("touch assistant audience: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return AudienceMember{}, err
	}
	if affected != 1 {
		return AudienceMember{}, ErrAudienceCapacity
	}
	return r.GetAudience(ctx, normalized.UserID)
}

func (r *SQLiteRepository) GetAudience(ctx context.Context, userID int64) (AudienceMember, error) {
	if r == nil || r.db == nil {
		return AudienceMember{}, ErrUnavailable
	}
	if userID <= 0 {
		return AudienceMember{}, ErrInvalidAudience
	}
	member, err := scanAudience(r.db.QueryRowContext(ctx, `
		SELECT user_id, sources, first_seen_at, last_seen_at
		FROM assistant_audience_members
		WHERE user_id = ?
	`, userID))
	if errors.Is(err, sql.ErrNoRows) {
		return AudienceMember{}, ErrAudienceNotFound
	}
	if err != nil {
		return AudienceMember{}, fmt.Errorf("get assistant audience member: %w", err)
	}
	return member, nil
}

func (r *SQLiteRepository) ListAudience(ctx context.Context, afterUserID int64, limit int) ([]AudienceMember, error) {
	if r == nil || r.db == nil {
		return nil, ErrUnavailable
	}
	if afterUserID < 0 {
		return nil, ErrInvalidAudience
	}
	if limit <= 0 || limit > MaxPruneBatch {
		limit = 100
	}
	rows, err := r.db.QueryContext(ctx, `
		SELECT user_id, sources, first_seen_at, last_seen_at
		FROM assistant_audience_members
		WHERE user_id > ?
		ORDER BY user_id ASC
		LIMIT ?
	`, afterUserID, limit)
	if err != nil {
		return nil, fmt.Errorf("list assistant audience: %w", err)
	}
	defer rows.Close()
	result := make([]AudienceMember, 0, limit)
	for rows.Next() {
		member, err := scanAudience(rows)
		if err != nil {
			return nil, fmt.Errorf("scan assistant audience member: %w", err)
		}
		result = append(result, member)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate assistant audience: %w", err)
	}
	return result, nil
}

func (r *SQLiteRepository) PruneAudienceBefore(ctx context.Context, cutoff time.Time, limit int) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrUnavailable
	}
	cutoff = cutoff.UTC()
	limit = normalizePruneLimit(limit)
	result, err := r.db.ExecContext(ctx, `
		DELETE FROM assistant_audience_members
		WHERE user_id IN (
			SELECT user_id FROM assistant_audience_members
			WHERE last_seen_at < ?
			ORDER BY last_seen_at ASC, user_id ASC
			LIMIT ?
		)
	`, cutoff, limit)
	if err != nil {
		return 0, fmt.Errorf("prune assistant audience: %w", err)
	}
	return affectedRows(result)
}

func (r *SQLiteRepository) CountAudience(ctx context.Context) (int, error) {
	return r.count(ctx, "assistant_audience_members")
}

type rowScanner interface {
	Scan(...any) error
}

func scanMapping(scanner rowScanner) (Mapping, error) {
	var mapping Mapping
	if err := scanner.Scan(
		&mapping.OwnerChatID,
		&mapping.OwnerMessageID,
		&mapping.VisitorUserID,
		&mapping.VisitorMessageID,
		&mapping.CreatedAt,
		&mapping.ExpiresAt,
	); err != nil {
		return Mapping{}, err
	}
	return mapping.Normalize()
}

func scanDelivery(scanner rowScanner) (DeliveryIntent, error) {
	var intent DeliveryIntent
	var direction string
	var claimExpires sql.NullTime
	var delivered sql.NullTime
	if err := scanner.Scan(
		&direction,
		&intent.SourceChatID,
		&intent.SourceMessageID,
		&intent.TargetChatID,
		&intent.RandomID,
		&intent.TargetMessageID,
		&intent.ClaimID,
		&claimExpires,
		&intent.Attempts,
		&intent.LastError,
		&intent.CreatedAt,
		&intent.UpdatedAt,
		&delivered,
		&intent.ExpiresAt,
	); err != nil {
		return DeliveryIntent{}, err
	}
	intent.Direction = DeliveryDirection(direction)
	if claimExpires.Valid {
		at := claimExpires.Time.UTC()
		intent.ClaimExpiresAt = &at
	}
	if delivered.Valid {
		at := delivered.Time.UTC()
		intent.DeliveredAt = &at
	}
	return intent.Normalize()
}

func scanAudience(scanner rowScanner) (AudienceMember, error) {
	var member AudienceMember
	var sources int64
	if err := scanner.Scan(&member.UserID, &sources, &member.FirstSeenAt, &member.LastSeenAt); err != nil {
		return AudienceMember{}, err
	}
	member.Sources = AudienceSource(sources)
	return member.Normalize()
}

func normalizePruneLimit(limit int) int {
	if limit <= 0 || limit > MaxPruneBatch {
		return 64
	}
	return limit
}

func affectedRows(result sql.Result) (int, error) {
	affected, err := result.RowsAffected()
	if err != nil {
		return 0, err
	}
	return int(affected), nil
}

func boundedDeliveryError(value string) string {
	value = strings.TrimSpace(value)
	if len(value) <= MaxDeliveryErrorBytes {
		return value
	}
	return strings.ToValidUTF8(string([]byte(value)[:MaxDeliveryErrorBytes]), "")
}

func (r *SQLiteRepository) count(ctx context.Context, table string) (int, error) {
	if r == nil || r.db == nil {
		return 0, ErrUnavailable
	}
	var count int
	query := "SELECT count(*) FROM " + table
	if err := r.db.QueryRowContext(ctx, query).Scan(&count); err != nil {
		return 0, fmt.Errorf("count %s: %w", table, err)
	}
	return count, nil
}

var _ Repository = (*SQLiteRepository)(nil)
