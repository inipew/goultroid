package blacklist

import "context"

// Repository defines access methods for chat keyword blacklists.
type Repository interface {
	AddBlacklist(ctx context.Context, chatID int64, word string) error
	RemoveBlacklist(ctx context.Context, chatID int64, word string) error
	ListBlacklists(ctx context.Context, chatID int64) ([]string, error)
}

// ActiveChatRepository is an optional optimization interface used to preload
// the set of chats that actually have blacklist state.
type ActiveChatRepository interface {
	ListActiveChatIDs(ctx context.Context) ([]int64, error)
}
