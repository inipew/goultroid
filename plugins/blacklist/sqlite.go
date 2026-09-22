package blacklist

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// SQLiteRepository implements Repository using *database.DB.
type SQLiteRepository struct {
	db         *database.DB
	mutationMu sync.Mutex
}

// NewSQLiteRepository constructs a SQLiteRepository.
func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// AddBlacklist adds a word to the blacklist for a chat.
func (r *SQLiteRepository) AddBlacklist(ctx context.Context, chatID int64, word string) error {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	word = strings.ToLower(strings.TrimSpace(word))
	if word == "" {
		return errors.New("word cannot be empty")
	}
	if len(word) > MaxRuleBytes {
		return fmt.Errorf("%w: blacklist rule exceeds %d bytes", core.ErrInvalidArgs, MaxRuleBytes)
	}

	var exists int
	if err := r.db.QueryRowContext(ctx,
		"SELECT COUNT(*) FROM blacklists WHERE chat_id = ? AND word = ?",
		chatID, word,
	).Scan(&exists); err != nil {
		return fmt.Errorf("failed to inspect blacklist rule: %w", err)
	}
	if exists == 0 {
		var count int
		if err := r.db.QueryRowContext(ctx,
			"SELECT COUNT(*) FROM blacklists WHERE chat_id = ?",
			chatID,
		).Scan(&count); err != nil {
			return fmt.Errorf("failed to count blacklist rules: %w", err)
		}
		if count >= MaxRulesPerChat {
			return ErrRuleLimit
		}
		if count == 0 {
			var activeChats int
			if err := r.db.QueryRowContext(ctx,
				"SELECT COUNT(*) FROM (SELECT chat_id FROM blacklists GROUP BY chat_id LIMIT ?)",
				MaxActiveChats+1,
			).Scan(&activeChats); err != nil {
				return fmt.Errorf("failed to count active blacklist chats: %w", err)
			}
			if activeChats >= MaxActiveChats {
				return fmt.Errorf("%w: active blacklist chats exceed %d", core.ErrResourceLimit, MaxActiveChats)
			}
		}
	}

	query := `INSERT INTO blacklists (chat_id, word, created_at)
	          VALUES (?, ?, ?)
	          ON CONFLICT(chat_id, word) DO UPDATE SET created_at = excluded.created_at`
	_, err := r.db.ExecContext(ctx, query, chatID, word, time.Now())
	if err != nil {
		return fmt.Errorf("failed to add blacklist word: %w", err)
	}
	return nil
}

// RemoveBlacklist removes a word from the blacklist for a chat.
func (r *SQLiteRepository) RemoveBlacklist(ctx context.Context, chatID int64, word string) error {
	r.mutationMu.Lock()
	defer r.mutationMu.Unlock()
	word = strings.ToLower(strings.TrimSpace(word))
	query := "DELETE FROM blacklists WHERE chat_id = ? AND word = ?"
	res, err := r.db.ExecContext(ctx, query, chatID, word)
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

// ListActiveChatIDs returns chats with at least one persisted blacklist entry.
func (r *SQLiteRepository) ListActiveChatIDs(ctx context.Context) ([]int64, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT DISTINCT chat_id FROM blacklists ORDER BY chat_id ASC LIMIT ?", MaxActiveChats+1)
	if err != nil {
		return nil, fmt.Errorf("failed to list active blacklist chats: %w", err)
	}
	defer rows.Close()

	var chatIDs []int64
	for rows.Next() {
		var chatID int64
		if err := rows.Scan(&chatID); err != nil {
			return nil, fmt.Errorf("failed to scan active blacklist chat: %w", err)
		}
		chatIDs = append(chatIDs, chatID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(chatIDs) > MaxActiveChats {
		return nil, fmt.Errorf("%w: active blacklist chats exceed %d", core.ErrResourceLimit, MaxActiveChats)
	}
	return chatIDs, nil
}

// ListBlacklists returns all blacklisted words for a chat.
func (r *SQLiteRepository) ListBlacklists(ctx context.Context, chatID int64) ([]string, error) {
	query := "SELECT word FROM blacklists WHERE chat_id = ? ORDER BY word ASC LIMIT ?"
	rows, err := r.db.QueryContext(ctx, query, chatID, MaxRulesPerChat+1)
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
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(words) > MaxRulesPerChat {
		return nil, ErrRuleLimit
	}
	return words, nil
}

var _ Repository = (*SQLiteRepository)(nil)
var _ ActiveChatRepository = (*SQLiteRepository)(nil)
