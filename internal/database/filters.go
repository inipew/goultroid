package database

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Filter represents a chat-specific auto-reply keyword filter.
type Filter struct {
	ChatID    int64     `json:"chat_id"`
	Keyword   string    `json:"keyword"`
	ReplyText string    `json:"reply_text"`
	CreatedAt time.Time `json:"created_at"`
}

// SaveFilter persists or updates a keyword reply filter for a chat.
func (d *DB) SaveFilter(ctx context.Context, chatID int64, keyword, replyText string) error {
	query := `
	INSERT INTO filters (chat_id, keyword, reply_text, created_at)
	VALUES (?, ?, ?, ?)
	ON CONFLICT(chat_id, keyword) DO UPDATE SET
		reply_text = excluded.reply_text,
		created_at = excluded.created_at;
	`
	_, err := d.ExecContext(ctx, query, chatID, strings.ToLower(keyword), replyText, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("failed to save filter: %w", err)
	}
	return nil
}

// GetFilter retrieves a filter by keyword for a chat.
func (d *DB) GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error) {
	query := "SELECT chat_id, keyword, reply_text, created_at FROM filters WHERE chat_id = ? AND keyword = ?"
	row := d.QueryRowContext(ctx, query, chatID, strings.ToLower(keyword))

	var f Filter
	if err := row.Scan(&f.ChatID, &f.Keyword, &f.ReplyText, &f.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get filter: %w", err)
	}
	return &f, nil
}

// ListFilters lists all active filters for a chat.
func (d *DB) ListFilters(ctx context.Context, chatID int64) ([]Filter, error) {
	query := "SELECT chat_id, keyword, reply_text, created_at FROM filters WHERE chat_id = ? ORDER BY keyword ASC"
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list filters: %w", err)
	}
	defer rows.Close()

	var filters []Filter
	for rows.Next() {
		var f Filter
		if err := rows.Scan(&f.ChatID, &f.Keyword, &f.ReplyText, &f.CreatedAt); err != nil {
			return nil, fmt.Errorf("failed to scan filter: %w", err)
		}
		filters = append(filters, f)
	}
	return filters, rows.Err()
}

// DeleteFilter removes a keyword filter for a chat.
func (d *DB) DeleteFilter(ctx context.Context, chatID int64, keyword string) error {
	query := "DELETE FROM filters WHERE chat_id = ? AND keyword = ?"
	res, err := d.ExecContext(ctx, query, chatID, strings.ToLower(keyword))
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
