package client

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type groupRuleChatAPI interface {
	ChannelsGetChannels(context.Context, []tg.InputChannelClass) (tg.MessagesChatsClass, error)
}

type managedGroupRuleChatClassifier struct {
	api groupRuleChatAPI
}

func newManagedGroupRuleChatClassifier(api groupRuleChatAPI) *managedGroupRuleChatClassifier {
	if api == nil {
		return nil
	}
	return &managedGroupRuleChatClassifier{api: api}
}

func groupRuleChannelFromResult(result tg.MessagesChatsClass, channelID int64) *tg.Channel {
	var chats []tg.ChatClass
	switch value := result.(type) {
	case *tg.MessagesChats:
		chats = value.Chats
	case *tg.MessagesChatsSlice:
		chats = value.Chats
	default:
		return nil
	}
	for _, item := range chats {
		channel, ok := item.(*tg.Channel)
		if ok && channel != nil && channel.ID == channelID {
			return channel
		}
	}
	return nil
}

func (c *managedGroupRuleChatClassifier) Classify(
	ctx context.Context,
	message *tg.Message,
	entities tg.Entities,
	inputPeer tg.InputPeerClass,
) (core.Chat, error) {
	if c == nil || c.api == nil || message == nil || inputPeer == nil {
		return core.Chat{}, fmt.Errorf("%w: group rule chat classifier unavailable", core.ErrUnavailable)
	}
	if ctx == nil {
		ctx = context.Background()
	}

	switch peer := message.PeerID.(type) {
	case *tg.PeerChat:
		chat := core.Chat{ID: peer.ChatID, Type: string(core.ChatKindGroup)}
		if entity := entities.Chats[peer.ChatID]; entity != nil {
			chat.Title = entity.Title
		}
		return chat, nil

	case *tg.PeerChannel:
		if entity := entities.Channels[peer.ChannelID]; entity != nil {
			if !entity.Megagroup {
				return core.Chat{}, core.ErrGroupOnly
			}
			return core.Chat{
				ID:         peer.ChannelID,
				Type:       string(core.ChatKindSupergroup),
				Title:      entity.Title,
				Username:   entity.Username,
				AccessHash: entity.AccessHash,
			}, nil
		}

		resolved, ok := inputPeer.(*tg.InputPeerChannel)
		if !ok || resolved.ChannelID != peer.ChannelID || resolved.AccessHash == 0 {
			return core.Chat{}, fmt.Errorf("%w: unresolved group-rule channel", core.ErrPeerUnresolved)
		}
		result, err := c.api.ChannelsGetChannels(ctx, []tg.InputChannelClass{
			&tg.InputChannel{ChannelID: resolved.ChannelID, AccessHash: resolved.AccessHash},
		})
		if err != nil {
			return core.Chat{}, fmt.Errorf("%w: classify group-rule channel: %v", core.ErrUnavailable, err)
		}
		channel := groupRuleChannelFromResult(result, peer.ChannelID)
		if channel == nil {
			return core.Chat{}, fmt.Errorf("%w: group-rule channel metadata missing", core.ErrUnavailable)
		}
		if !channel.Megagroup {
			return core.Chat{}, core.ErrGroupOnly
		}
		return core.Chat{
			ID:         channel.ID,
			Type:       string(core.ChatKindSupergroup),
			Title:      strings.TrimSpace(channel.Title),
			Username:   channel.Username,
			AccessHash: channel.AccessHash,
		}, nil

	default:
		return core.Chat{}, core.ErrGroupOnly
	}
}
