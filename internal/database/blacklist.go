package database

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

// AddBlacklist adds a word to the blacklist for a chat.
func (d *DB) AddBlacklist(ctx context.Context, chatID int64, word string) error {
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return errors.New("word cannot be empty")
	}

	query := `INSERT INTO blacklists (chat_id, word, created_at)
	          VALUES (?, ?, ?)
	          ON CONFLICT(chat_id, word) DO UPDATE SET created_at = excluded.created_at`
	_, err := d.ExecContext(ctx, query, chatID, word, time.Now())
	if err != nil {
		return fmt.Errorf("failed to add blacklist word: %w", err)
	}
	return nil
}

// RemoveBlacklist removes a word from the blacklist for a chat.
func (d *DB) RemoveBlacklist(ctx context.Context, chatID int64, word string) error {
	word = strings.ToLower(strings.TrimSpace(word))
	query := "DELETE FROM blacklists WHERE chat_id = ? AND word = ?"
	res, err := d.ExecContext(ctx, query, chatID, word)
	if err != nil {
		return fmt.Errorf("failed to remove blacklist word: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if rows == 0 {
		return errors.New("word not found in blacklist")
	}
	return nil
}

// ListBlacklists returns all blacklisted words for a chat.
func (d *DB) ListBlacklists(ctx context.Context, chatID int64) ([]string, error) {
	query := "SELECT word FROM blacklists WHERE chat_id = ? ORDER BY word ASC"
	rows, err := d.QueryContext(ctx, query, chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list blacklists: %w", err)
	}
	defer rows.Close()

	var words []string
	for rows.Next() {
		var w string
		if err := rows.Scan(&w); err != nil {
			return nil, fmt.Errorf("failed to scan blacklist word: %w", err)
		}
		words = append(words, w)
	}
	return words, rows.Err()
}
