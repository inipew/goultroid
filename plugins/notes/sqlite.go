package notes

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// SQLiteRepository implements Repository using *database.DB.
type SQLiteRepository struct {
	db *database.DB
}

// NewSQLiteRepository constructs a SQLiteRepository.
func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// SaveNote saves or updates a note for a chat.
func (r *SQLiteRepository) SaveNote(ctx context.Context, chatID int64, name, content string) error {
	now := time.Now().UTC()
	query := `
	INSERT INTO notes (chat_id, name, content, created_at, updated_at)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(chat_id, name) DO UPDATE SET content = excluded.content, updated_at = excluded.updated_at
	`
	_, err := r.db.ExecContext(ctx, query, chatID, name, content, now, now)
	if err != nil {
		return fmt.Errorf("failed to save note: %w", err)
	}
	return nil
}

// GetNote retrieves a note by name in a chat.
func (r *SQLiteRepository) GetNote(ctx context.Context, chatID int64, name string) (*Note, error) {
	query := "SELECT chat_id, name, content, created_at, updated_at FROM notes WHERE chat_id = ? AND name = ?"
	row := r.db.QueryRowContext(ctx, query, chatID, name)

	var n Note
	err := row.Scan(&n.ChatID, &n.Name, &n.Content, &n.CreatedAt, &n.UpdatedAt)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get note: %w", err)
	}
	return &n, nil
}

// ListNotes lists all note names for a chat.
func (r *SQLiteRepository) ListNotes(ctx context.Context, chatID int64) ([]string, error) {
	rows, err := r.db.QueryContext(ctx, "SELECT name FROM notes WHERE chat_id = ? ORDER BY name ASC", chatID)
	if err != nil {
		return nil, fmt.Errorf("failed to list notes: %w", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("failed to scan note name: %w", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return names, nil
}

// DeleteNote deletes a note by name in a chat.
func (r *SQLiteRepository) DeleteNote(ctx context.Context, chatID int64, name string) error {
	res, err := r.db.ExecContext(ctx, "DELETE FROM notes WHERE chat_id = ? AND name = ?", chatID, name)
	if err != nil {
		return fmt.Errorf("failed to delete note: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if affected == 0 {
		return errors.New("note not found")
	}
	return nil
}

var _ Repository = (*SQLiteRepository)(nil)
