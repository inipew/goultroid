package filters

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/services/mediaregistry"
	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type SQLiteRepository struct {
	db            *database.DB
	registryReady bool
	registryErr   error
}

func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	ready, err := mediaregistry.SchemaReady(context.Background(), db)
	return &SQLiteRepository{db: db, registryReady: ready, registryErr: err}
}

func (r *SQLiteRepository) SaveFilter(ctx context.Context, chatID int64, keyword string, response savedresponse.Response) error {
	if r.registryErr != nil {
		return fmt.Errorf("failed to inspect media registry schema: %w", r.registryErr)
	}

	keyword = strings.ToLower(strings.TrimSpace(keyword))
	if keyword == "" || len(keyword) > MaxKeywordBytes {
		return fmt.Errorf("%w: invalid filter keyword", core.ErrInvalidArgs)
	}
	if err := savedresponse.Validate(response); err != nil {
		return err
	}
	var exists int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM filters WHERE chat_id = ? AND keyword = ?",
		chatID, keyword,
	).Scan(&exists); err != nil {
		return fmt.Errorf("failed to inspect filter: %w", err)
	}
	if exists == 0 {
		var count int
		if err := r.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM filters WHERE chat_id = ?",
			chatID,
		).Scan(&count); err != nil {
			return fmt.Errorf("failed to count filters: %w", err)
		}
		if count >= MaxRulesPerChat {
			return ErrRuleLimit
		}
		if count == 0 {
			var activeChats int
			if err := r.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM (SELECT chat_id FROM filters GROUP BY chat_id LIMIT ?)",
				MaxActiveChats+1,
			).Scan(&activeChats); err != nil {
				return fmt.Errorf("failed to count active filter chats: %w", err)
			}
			if activeChats >= MaxActiveChats {
				return fmt.Errorf("%w: active filter chats exceed %d", core.ErrResourceLimit, MaxActiveChats)
			}
		}
	}

	media := savedresponse.MediaRef{}
	if response.Media != nil {
		media = *response.Media
	}
	format := response.Format
	if format == "" {
		format = savedresponse.FormatHTML
	}
	now := time.Now().UTC()
	query := `
	INSERT INTO filters (
		chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(chat_id, keyword) DO UPDATE SET
		reply_text = excluded.reply_text,
		response_format = excluded.response_format,
		media_asset_id = excluded.media_asset_id,
		media_type = excluded.media_type,
		media_name = excluded.media_name,
		media_mime = excluded.media_mime,
		created_at = excluded.created_at;
	`
	if !r.registryReady {
		if _, err := r.db.ExecContext(ctx, query,
			chatID, keyword, response.Text, string(format),
			media.AssetID, media.MediaType, media.Name, media.MIMEType, now,
		); err != nil {
			return fmt.Errorf("failed to save filter: %w", err)
		}
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin filter save: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var previousAssetID string
	err = tx.QueryRowContext(ctx,
		"SELECT media_asset_id FROM filters WHERE chat_id = ? AND keyword = ?",
		chatID,
		keyword,
	).Scan(&previousAssetID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("failed to inspect previous filter media: %w", err)
	}

	if _, err := tx.ExecContext(ctx, query,
		chatID, keyword, response.Text, string(format),
		media.AssetID, media.MediaType, media.Name, media.MIMEType, now,
	); err != nil {
		return fmt.Errorf("failed to save filter: %w", err)
	}
	if media.AssetID != "" {
		if err := savedresponse.EnsureLegacyMediaAssetWithExecutor(ctx, tx, media.AssetID); err != nil {
			return fmt.Errorf("failed to mirror legacy media ledger: %w", err)
		}
		if err := mediaregistry.RegisterAssetWithExecutor(
			ctx,
			tx,
			savedresponse.MediaRegistryAssetRegistration(media.AssetID),
			savedresponse.FilterMediaRegistryReference(media.AssetID, chatID, keyword),
		); err != nil {
			return fmt.Errorf("failed to mirror filter media registry: %w", err)
		}
	}
	if previousAssetID != "" && previousAssetID != media.AssetID {
		if err := mediaregistry.RemoveReferenceWithExecutor(
			ctx,
			tx,
			savedresponse.FilterMediaRegistryReference(previousAssetID, chatID, keyword),
		); err != nil {
			return fmt.Errorf("failed to remove previous filter media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit filter save: %w", err)
	}
	return nil
}

func (r *SQLiteRepository) GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error) {
	query := `SELECT chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
		FROM filters WHERE chat_id = ? AND keyword = ?`
	row := r.db.QueryRowContext(ctx, query, chatID, strings.ToLower(keyword))
	var f Filter
	var format, assetID, mediaType, mediaName, mediaMIME string
	if err := row.Scan(
		&f.ChatID, &f.Keyword, &f.Response.Text, &format,
		&assetID, &mediaType, &mediaName, &mediaMIME, &f.CreatedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get filter: %w", err)
	}
	f.Response.Format = savedresponse.Format(format)
	if f.Response.Format == "" {
		f.Response.Format = savedresponse.FormatHTML
	}
	if assetID != "" {
		f.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
	}
	return &f, nil
}

func (r *SQLiteRepository) ListActiveChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT chat_id FROM filters ORDER BY chat_id ASC LIMIT ?", MaxActiveChats+1)
	if err != nil {
		return nil, fmt.Errorf("failed to list active filter chats: %w", err)
	}
	defer rows.Close()
	var chatIDs []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("failed to scan active filter chat: %w", err)
		}
		chatIDs = append(chatIDs, chatID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(chatIDs) > MaxActiveChats {
		return nil, fmt.Errorf("%w: active filter chats exceed %d", core.ErrResourceLimit, MaxActiveChats)
	}
	return chatIDs, nil
}

func (r *SQLiteRepository) ListFilters(ctx context.Context, chatID int64) ([]Filter, error) {
	query := `SELECT chat_id, keyword, reply_text, response_format,
		media_asset_id, media_type, media_name, media_mime, created_at
		FROM filters WHERE chat_id = ? ORDER BY keyword ASC`
	rows, err := r.db.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list filters: %w", err)
	}
	defer rows.Close()

	var result []Filter
	for rows.Next() {
		var f Filter
		var format, assetID, mediaType, mediaName, mediaMIME string
		if err := rows.Scan(
			&f.ChatID, &f.Keyword, &f.Response.Text, &format,
			&assetID, &mediaType, &mediaName, &mediaMIME, &f.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("failed to scan filter: %w", err)
		}
		f.Response.Format = savedresponse.Format(format)
		if f.Response.Format == "" {
			f.Response.Format = savedresponse.FormatHTML
		}
		if assetID != "" {
			f.Response.Media = &savedresponse.MediaRef{AssetID: assetID, MediaType: mediaType, Name: mediaName, MIMEType: mediaMIME}
		}
		result = append(result, f)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(result) > MaxRulesPerChat {
		return nil, ErrRuleLimit
	}
	return result, nil
}

func (r *SQLiteRepository) DeleteFilter(ctx context.Context, chatID int64, keyword string) error {
	if r.registryErr != nil {
		return fmt.Errorf("failed to inspect media registry schema: %w", r.registryErr)
	}

	keyword = strings.ToLower(keyword)
	if !r.registryReady {
		res, err := r.db.ExecContext(ctx, "DELETE FROM filters WHERE chat_id = ? AND keyword = ?", chatID, keyword)
		if err != nil {
			return fmt.Errorf("failed to delete filter: %w", err)
		}
		rows, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if rows == 0 {
			return errors.New("filter not found")
		}
		return nil
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to begin filter delete: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var assetID string
	if err := tx.QueryRowContext(ctx,
		"SELECT media_asset_id FROM filters WHERE chat_id = ? AND keyword = ?",
		chatID,
		keyword,
	).Scan(&assetID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("filter not found")
		}
		return fmt.Errorf("failed to inspect filter media before delete: %w", err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM filters WHERE chat_id = ? AND keyword = ?", chatID, keyword); err != nil {
		return fmt.Errorf("failed to delete filter: %w", err)
	}
	if assetID != "" {
		if err := mediaregistry.RemoveReferenceWithExecutor(
			ctx,
			tx,
			savedresponse.FilterMediaRegistryReference(assetID, chatID, keyword),
		); err != nil {
			return fmt.Errorf("failed to remove filter media reference: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("failed to commit filter delete: %w", err)
	}
	return nil
}

var _ Repository = (*SQLiteRepository)(nil)
var _ ActiveChatRepository = (*SQLiteRepository)(nil)
