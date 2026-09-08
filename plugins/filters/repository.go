package filters

import (
	"context"
	"time"
)

// Filter represents a chat-specific auto-reply keyword filter.
type Filter struct {
	ChatID    int64     `json:"chat_id"`
	Keyword   string    `json:"keyword"`
	ReplyText string    `json:"reply_text"`
	CreatedAt time.Time `json:"created_at"`
}

// Repository defines access methods for chat auto-reply filters.
type Repository interface {
	SaveFilter(ctx context.Context, chatID int64, keyword, replyText string) error
	GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error)
	ListFilters(ctx context.Context, chatID int64) ([]Filter, error)
	DeleteFilter(ctx context.Context, chatID int64, keyword string) error
}
