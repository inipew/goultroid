package notes

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type Note struct {
	ChatID    int64                  `json:"chat_id"`
	Name      string                 `json:"name"`
	Response  savedresponse.Response `json:"response"`
	CreatedAt time.Time              `json:"created_at"`
	UpdatedAt time.Time              `json:"updated_at"`
}

type Repository interface {
	SaveNote(ctx context.Context, chatID int64, name string, response savedresponse.Response) error
	GetNote(ctx context.Context, chatID int64, name string) (*Note, error)
	ListNotes(ctx context.Context, chatID int64) ([]string, error)
	DeleteNote(ctx context.Context, chatID int64, name string) error
}
