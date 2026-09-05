package telegram

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
)

// Service provides high-level Telegram operations implementing core.TelegramServicer.
type Service struct {
	api        *tg.Client
	sender     *message.Sender
	downloader *downloader.Downloader
	uploader   *uploader.Uploader
}

// NewService creates a new Service instance.
func NewService(api *tg.Client) *Service {
	return &Service{
		api:        api,
		sender:     message.NewSender(api),
		downloader: downloader.NewDownloader(),
		uploader:   uploader.NewUploader(api),
	}
}

// SendMessage sends a text message to the specified peer and returns the created tg.Message if available.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
func (s *Service) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("sender is not initialized")
	}

	updates, err := s.sender.To(peer).StyledText(ctx, html.String(nil, text))
	if err != nil {
		// Fallback to plain text if HTML parsing or formatting fails
		updates, err = s.sender.To(peer).Text(ctx, text)
		if err != nil {
			return nil, err
		}
	}

	return extractMessageFromUpdates(updates), nil
}

// EditMessage edits the text of an existing message.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
func (s *Service) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if s.sender == nil {
		return fmt.Errorf("sender is not initialized")
	}

	_, err := s.sender.To(peer).Edit(msgID).StyledText(ctx, html.String(nil, text))
	if err != nil {
		// Fallback to plain text if HTML parsing or formatting fails
		_, err = s.sender.To(peer).Edit(msgID).Text(ctx, text)
	}
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

// BanUser restricts a user from viewing and sending messages in a group/supergroup.
func (s *Service) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			Participant: user,
			BannedRights: tg.ChatBannedRights{
				ViewMessages: true,
				SendMessages: true,
				SendMedia:    true,
				SendStickers: true,
				SendGifs:     true,
				SendGames:    true,
				SendInline:   true,
				EmbedLinks:   true,
				UntilDate:    untilDate,
			},
		})
		return err
	}

	if chat, ok := peer.(*tg.InputPeerChat); ok {
		if u, ok := user.(*tg.InputPeerUser); ok {
			_, err := s.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
				ChatID: chat.ChatID,
				UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
			})
			return err
		}
	}

	return fmt.Errorf("unsupported peer type for ban: %T", peer)
}

// UnbanUser removes all ban restrictions on a user.
func (s *Service) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			Participant:  user,
			BannedRights: tg.ChatBannedRights{}, // reset all rights
		})
		return err
	}

	return nil
}

// KickUser removes a user from the group while allowing them to rejoin.
func (s *Service) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		channel := &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}
		_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      channel,
			Participant:  user,
			BannedRights: tg.ChatBannedRights{ViewMessages: true, UntilDate: int(time.Now().Unix() + 60)},
		})
		if err != nil {
			return err
		}
		_, err = s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      channel,
			Participant:  user,
			BannedRights: tg.ChatBannedRights{}, // allow rejoin
		})
		return err
	}

	if chat, ok := peer.(*tg.InputPeerChat); ok {
		if u, ok := user.(*tg.InputPeerUser); ok {
			_, err := s.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
				ChatID: chat.ChatID,
				UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
			})
			return err
		}
	}

	return fmt.Errorf("unsupported peer type for kick: %T", peer)
}

// MuteUser restricts a user from sending messages and media until untilDate.
func (s *Service) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			Participant: user,
			BannedRights: tg.ChatBannedRights{
				SendMessages:    true,
				SendMedia:       true,
				SendStickers:    true,
				SendGifs:        true,
				SendGames:       true,
				SendInline:      true,
				EmbedLinks:      true,
				SendPolls:       true,
				SendPhotos:      true,
				SendVideos:      true,
				SendRoundvideos: true,
				SendAudios:      true,
				SendVoices:      true,
				SendDocs:        true,
				SendPlain:       true,
				UntilDate:       untilDate,
			},
		})
		return err
	}

	return fmt.Errorf("mute is only supported in supergroups and channels")
}

// UnmuteUser removes send message restrictions on a user.
func (s *Service) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
			Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			Participant:  user,
			BannedRights: tg.ChatBannedRights{},
		})
		return err
	}

	return nil
}

// PurgeMessages purges messages in the range [fromID..toID]. If topicID > 0, it uses MessagesGetReplies
// to ensure only messages inside that forum topic/thread are deleted.
func (s *Service) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	if s.api == nil {
		return 0, errors.New("api is not initialized")
	}

	minID := fromID
	maxID := toID
	if minID > maxID {
		minID, maxID = maxID, minID
	}

	msgIDsMap := make(map[int]struct{})
	msgIDsMap[fromID] = struct{}{}
	msgIDsMap[toID] = struct{}{}

	if topicID > 0 {
		// Topic-scoped purge via GetReplies
		resp, err := s.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
			Peer:  peer,
			MsgID: topicID,
			MinID: minID - 1,
			MaxID: maxID + 1,
			Limit: 100,
		})
		if err == nil && resp != nil {
			if mod, ok := resp.AsModified(); ok {
				for _, m := range mod.GetMessages() {
					if msg, ok := m.(*tg.Message); ok {
						if msg.ID >= minID && msg.ID <= maxID {
							msgIDsMap[msg.ID] = struct{}{}
						}
					}
				}
			}
		}
	} else {
		// Non-topic history fetch
		resp, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
			Peer:  peer,
			MinID: minID - 1,
			MaxID: maxID + 1,
			Limit: 100,
		})
		if err == nil && resp != nil {
			if mod, ok := resp.AsModified(); ok {
				for _, m := range mod.GetMessages() {
					if msg, ok := m.(*tg.Message); ok {
						if msg.ID >= minID && msg.ID <= maxID {
							msgIDsMap[msg.ID] = struct{}{}
						}
					}
				}
			}
		}
	}

	allIDs := make([]int, 0, len(msgIDsMap))
	for id := range msgIDsMap {
		allIDs = append(allIDs, id)
	}

	// Delete in chunks of 100
	const chunkSize = 100
	totalDeleted := 0
	for i := 0; i < len(allIDs); i += chunkSize {
		end := i + chunkSize
		if end > len(allIDs) {
			end = len(allIDs)
		}
		chunk := allIDs[i:end]
		if err := s.DeleteMessage(ctx, peer, chunk); err != nil {
			return totalDeleted, err
		}
		totalDeleted += len(chunk)
	}

	return totalDeleted, nil
}

// SendMedia uploads and sends media (photo, sticker, audio, video, file) to the specified peer.
func (s *Service) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if s.sender == nil || s.uploader == nil {
		return nil, fmt.Errorf("sender/uploader is not initialized")
	}

	inputFile, err := s.uploader.FromPath(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to upload file %q: %w", filePath, err)
	}

	builder := s.sender.To(peer)
	var styledCaption []message.StyledTextOption
	if caption != "" {
		styledCaption = append(styledCaption, html.String(nil, caption))
	}

	var updates tg.UpdatesClass
	switch mediaType {
	case "photo":
		updates, err = builder.UploadedPhoto(ctx, inputFile, styledCaption...)
	case "sticker":
		updates, err = builder.UploadedSticker(ctx, inputFile, styledCaption...)
	case "audio":
		updates, err = builder.Audio(ctx, inputFile, styledCaption...)
	case "video":
		updates, err = builder.Video(ctx, inputFile, styledCaption...)
	case "file", "document":
		fallthrough
	default:
		updates, err = builder.File(ctx, inputFile, styledCaption...)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to send media (%s): %w", mediaType, err)
	}

	return extractMessageFromUpdates(updates), nil
}

// GetFullUser retrieves extended profile information for a user.
func (s *Service) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	if s.api == nil {
		return nil, errors.New("telegram api not initialized")
	}
	return s.api.UsersGetFullUser(ctx, user)
}

// ResolveUsername resolves a public @username to peer entities.
func (s *Service) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	if s.api == nil {
		return nil, errors.New("telegram api not initialized")
	}
	cleaned := strings.TrimPrefix(username, "@")
	return s.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: cleaned})
}

// GetFullChat retrieves extended information for a group, supergroup, or channel.
func (s *Service) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	if s.api == nil {
		return nil, errors.New("telegram api not initialized")
	}

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		return s.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{
			ChannelID:  p.ChannelID,
			AccessHash: p.AccessHash,
		})
	case *tg.InputPeerChat:
		return s.api.MessagesGetFullChat(ctx, p.ChatID)
	default:
		return nil, errors.New("chat info is only available for groups, supergroups, and channels")
	}
}

// extractMessageFromUpdates attempts to locate a tg.Message from tg.UpdatesClass.
// PromoteAdmin promotes a user to administrator in the supergroup/channel with custom title.
func (s *Service) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("promote is only supported in supergroups/channels, got: %T", peer)
	}

	u, ok := user.(*tg.InputPeerUser)
	if !ok {
		return fmt.Errorf("target user must be an InputPeerUser, got: %T", user)
	}

	req := &tg.ChannelsEditAdminRequest{
		Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		UserID:  &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
		AdminRights: tg.ChatAdminRights{
			ChangeInfo:     true,
			PostMessages:   true,
			EditMessages:   true,
			DeleteMessages: true,
			BanUsers:       true,
			InviteUsers:    true,
			PinMessages:    true,
			ManageTopics:   true,
		},
	}
	if title != "" {
		req.SetRank(title)
	}

	_, err := s.api.ChannelsEditAdmin(ctx, req)
	return err
}

// DemoteAdmin demotes an administrator to regular member in the supergroup/channel.
func (s *Service) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("demote is only supported in supergroups/channels, got: %T", peer)
	}

	u, ok := user.(*tg.InputPeerUser)
	if !ok {
		return fmt.Errorf("target user must be an InputPeerUser, got: %T", user)
	}

	req := &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		UserID:      &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
		AdminRights: tg.ChatAdminRights{}, // empty rights removes admin status
	}

	_, err := s.api.ChannelsEditAdmin(ctx, req)
	return err
}

// EditChatDefaultBannedRights updates the default chat permissions (locks) in a group/supergroup.
func (s *Service) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	req := &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer:         peer,
		BannedRights: rights,
	}

	_, err := s.api.MessagesEditChatDefaultBannedRights(ctx, req)
	return err
}

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
