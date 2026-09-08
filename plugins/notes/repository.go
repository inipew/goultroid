package notes

import (
	"context"
	"time"
)

// Note represents a saved note for a specific chat.
type Note struct {
	ChatID    int64     `json:"chat_id"`
	Name      string    `json:"name"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// Repository defines access methods for chat notes.
type Repository interface {
	SaveNote(ctx context.Context, chatID int64, name, content string) error
	GetNote(ctx context.Context, chatID int64, name string) (*Note, error)
	ListNotes(ctx context.Context, chatID int64) ([]string, error)
	DeleteNote(ctx context.Context, chatID int64, name string) error
}
