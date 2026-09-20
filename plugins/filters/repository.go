package filters

import (
	"context"
	"time"

	"github.com/inipew/goultroid/internal/services/savedresponse"
)

type Filter struct {
	ChatID    int64                  `json:"chat_id"`
	Keyword   string                 `json:"keyword"`
	Response  savedresponse.Response `json:"response"`
	CreatedAt time.Time              `json:"created_at"`
}

type Repository interface {
	SaveFilter(ctx context.Context, chatID int64, keyword string, response savedresponse.Response) error
	GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error)
	ListFilters(ctx context.Context, chatID int64) ([]Filter, error)
	DeleteFilter(ctx context.Context, chatID int64, keyword string) error
}

type ActiveChatRepository interface {
	ListActiveChatIDs(ctx context.Context) ([]int64, error)
}
