package peer

import (
	"context"
	"fmt"

	"github.com/gotd/td/tg"
)

// FetcherAPI defines MTProto methods needed to fetch entity access hashes.
type FetcherAPI interface {
	UsersGetUsers(ctx context.Context, id []tg.InputUserClass) ([]tg.UserClass, error)
	ChannelsGetChannels(ctx context.Context, id []tg.InputChannelClass) (tg.MessagesChatsClass, error)
}

// TelegramEntityFetcher implements EntityFetcher by querying Telegram's MTProto API.
type TelegramEntityFetcher struct {
	api FetcherAPI
}

var _ EntityFetcher = (*TelegramEntityFetcher)(nil)

// NewTelegramEntityFetcher creates a fetcher backed by Telegram MTProto API.
func NewTelegramEntityFetcher(api FetcherAPI) *TelegramEntityFetcher {
	return &TelegramEntityFetcher{api: api}
}

// FetchUser queries Telegram for user details and access hash.
func (f *TelegramEntityFetcher) FetchUser(ctx context.Context, id int64) (*tg.User, error) {
	if f == nil || f.api == nil || id == 0 {
		return nil, ErrPeerResolution
	}
	users, err := f.api.UsersGetUsers(ctx, []tg.InputUserClass{&tg.InputUser{UserID: id, AccessHash: 0}})
	if err != nil {
		return nil, fmt.Errorf("%w: UsersGetUsers(%d): %v", ErrPeerResolution, id, err)
	}
	for _, uClass := range users {
		if u, ok := uClass.(*tg.User); ok && u.ID == id {
			return u, nil
		}
	}
	return nil, fmt.Errorf("%w: user %d not found in response", ErrPeerResolution, id)
}

// FetchChannel queries Telegram for channel details and access hash.
func (f *TelegramEntityFetcher) FetchChannel(ctx context.Context, id int64) (*tg.Channel, error) {
	if f == nil || f.api == nil || id == 0 {
		return nil, ErrPeerResolution
	}
	res, err := f.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{&tg.InputChannel{ChannelID: id, AccessHash: 0}})
	if err != nil {
		return nil, fmt.Errorf("%w: ChannelsGetChannels(%d): %v", ErrPeerResolution, id, err)
	}
	var chats []tg.ChatClass
	switch c := res.(type) {
	case *tg.MessagesChats:
		chats = c.Chats
	case *tg.MessagesChatsSlice:
		chats = c.Chats
	}
	for _, chClass := range chats {
		if ch, ok := chClass.(*tg.Channel); ok && ch.ID == id {
			return ch, nil
		}
	}
	return nil, fmt.Errorf("%w: channel %d not found in response", ErrPeerResolution, id)
}
