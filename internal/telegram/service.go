package telegram

import (
	"context"
	"fmt"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/tg"
)

// Service provides high-level Telegram operations implementing core.TelegramServicer.
type Service struct {
	api        *tg.Client
	sender     *message.Sender
	downloader *downloader.Downloader
}

// NewService creates a new Service instance.
func NewService(api *tg.Client) *Service {
	return &Service{
		api:        api,
		sender:     message.NewSender(api),
		downloader: downloader.NewDownloader(),
	}
}

// SendMessage sends a text message to the specified peer and returns the created tg.Message if available.
func (s *Service) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("sender is not initialized")
	}

	updates, err := s.sender.To(peer).Text(ctx, text)
	if err != nil {
		return nil, err
	}

	return extractMessageFromUpdates(updates), nil
}

// EditMessage edits the text of an existing message.
func (s *Service) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if s.sender == nil {
		return fmt.Errorf("sender is not initialized")
	}

	_, err := s.sender.To(peer).Edit(msgID).Text(ctx, text)
	return err
}

// DeleteMessage deletes messages for everyone (revokes).
func (s *Service) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
			Channel: &tg.InputChannel{
				ChannelID:  ch.ChannelID,
				AccessHash: ch.AccessHash,
			},
			ID: msgIDs,
		})
		return err
	}

	_, err := s.sender.To(peer).Revoke().Messages(ctx, msgIDs...)
	return err
}

// React places an emoji reaction on the given message.
func (s *Service) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	if s.sender == nil {
		return fmt.Errorf("sender is not initialized")
	}

	_, err := s.sender.To(peer).Reaction(ctx, msgID, &tg.ReactionEmoji{Emoticon: emoji})
	return err
}

// GetMessage fetches a message by its ID.
func (s *Service) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	var msgs []tg.MessageClass

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		res, err := s.api.ChannelsGetMessages(ctx, &tg.ChannelsGetMessagesRequest{
			Channel: &tg.InputChannel{
				ChannelID:  p.ChannelID,
				AccessHash: p.AccessHash,
			},
			ID: []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
		})
		if err != nil {
			return nil, err
		}
		switch m := res.(type) {
		case *tg.MessagesChannelMessages:
			msgs = m.Messages
		case *tg.MessagesMessages:
			msgs = m.Messages
		case *tg.MessagesMessagesSlice:
			msgs = m.Messages
		}
	default:
		res, err := s.api.MessagesGetMessages(ctx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
		if err != nil {
			return nil, err
		}
		switch m := res.(type) {
		case *tg.MessagesMessages:
			msgs = m.Messages
		case *tg.MessagesMessagesSlice:
			msgs = m.Messages
		case *tg.MessagesChannelMessages:
			msgs = m.Messages
		}
	}

	for _, m := range msgs {
		if msg, ok := m.(*tg.Message); ok && msg.ID == msgID {
			return msg, nil
		}
	}

	return nil, nil
}

// PinMessage pins a message in the chat.
func (s *Service) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	req := &tg.MessagesUpdatePinnedMessageRequest{
		Silent: silent,
		Unpin:  false,
		Peer:   peer,
		ID:     msgID,
	}
	if silent {
		req.SetSilent(true)
	}
	_, err := s.api.MessagesUpdatePinnedMessage(ctx, req)
	return err
}

// UnpinMessage unpins a message in the chat.
func (s *Service) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	req := &tg.MessagesUpdatePinnedMessageRequest{
		Unpin: true,
		Peer:  peer,
		ID:    msgID,
	}
	req.SetUnpin(true)
	_, err := s.api.MessagesUpdatePinnedMessage(ctx, req)
	return err
}

// ForwardMessages forwards messages from fromPeer to toPeer.
func (s *Service) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	if s.sender == nil {
		return fmt.Errorf("sender is not initialized")
	}
	if len(msgIDs) == 0 {
		return nil
	}
	_, err := s.sender.To(toPeer).ForwardIDs(fromPeer, msgIDs[0], msgIDs[1:]...).Send(ctx)
	return err
}

// DownloadFile streams and downloads a Telegram media file to local destination path.
func (s *Service) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	if s.downloader == nil {
		s.downloader = downloader.NewDownloader()
	}
	_, err := s.downloader.Download(s.api, location).ToPath(ctx, dstPath)
	return err
}

// extractMessageFromUpdates attempts to locate a tg.Message from tg.UpdatesClass.
func extractMessageFromUpdates(u tg.UpdatesClass) *tg.Message {
	switch upd := u.(type) {
	case *tg.Updates:
		for _, item := range upd.Updates {
			if newMsg, ok := item.(*tg.UpdateNewMessage); ok {
				if msg, ok := newMsg.Message.(*tg.Message); ok {
					return msg
				}
			}
			if newChannelMsg, ok := item.(*tg.UpdateNewChannelMessage); ok {
				if msg, ok := newChannelMsg.Message.(*tg.Message); ok {
					return msg
				}
			}
		}
	case *tg.UpdateShortSentMessage:
		return &tg.Message{
			ID:   upd.ID,
			Date: upd.Date,
		}
	case *tg.UpdateShortMessage:
		return &tg.Message{
			ID:      upd.ID,
			Message: upd.Message,
			Date:    upd.Date,
		}
	}
	return nil
}
