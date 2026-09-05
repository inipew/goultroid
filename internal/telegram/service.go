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
	"github.com/gotd/td/telegram/peers"
	"github.com/gotd/td/telegram/uploader"
	"github.com/gotd/td/tg"
	"github.com/gotd/td/tgerr"
	"github.com/inipew/goultroid/internal/core"
)

const defaultFloodWaitRetryLimit = 5 * time.Second

// mapTelegramError maps raw MTProto/RPC errors into core domain errors.
func mapTelegramError(err error) error {
	if err == nil {
		return nil
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return core.NewRateLimitError(wait, err)
	}
	if tgerr.Is(err, "CHAT_ID_INVALID", "PEER_ID_INVALID", "USER_ID_INVALID") {
		return fmt.Errorf("%w: %v", core.ErrNotFound, err)
	}
	if tgerr.Is(err, "CHAT_ADMIN_REQUIRED", "RIGHTS_NOT_MODIFIED") {
		return fmt.Errorf("%w: %v", core.ErrPermissionDenied, err)
	}
	return fmt.Errorf("%w: %v", core.ErrTelegram, err)
}

func retryOnFloodWait[T any](ctx context.Context, op func() (T, error)) (T, error) {
	val, err := op()
	if err == nil {
		return val, nil
	}

	if wait, ok := tgerr.AsFloodWait(err); ok && wait <= defaultFloodWaitRetryLimit {
		select {
		case <-ctx.Done():
			return val, ctx.Err()
		case <-time.After(wait):
			val, err = op()
			if err == nil {
				return val, nil
			}
		}
	}

	return val, mapTelegramError(err)
}

// Service provides high-level Telegram operations implementing core.TelegramServicer.
type Service struct {
	api         *tg.Client
	sender      *message.Sender
	downloader  *downloader.Downloader
	uploader    *uploader.Uploader
	peerManager *peers.Manager
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

// SetPeerManager configures the peers.Manager used for caching and resolving peer access hashes.
func (s *Service) SetPeerManager(pm *peers.Manager) {
	s.peerManager = pm
}

func (s *Service) ensureChannelAccessHash(ctx context.Context, peer tg.InputPeerClass) tg.InputPeerClass {
	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok || ch.AccessHash != 0 || s.peerManager == nil {
		return peer
	}
	if resolved, err := s.peerManager.ResolveChannelID(ctx, ch.ChannelID); err == nil {
		return resolved.InputPeer()
	}
	return peer
}

func (s *Service) ensureUserAccessHash(ctx context.Context, user tg.InputPeerClass) tg.InputPeerClass {
	u, ok := user.(*tg.InputPeerUser)
	if !ok || u.AccessHash != 0 || s.peerManager == nil {
		return user
	}
	if resolved, err := s.peerManager.ResolveUserID(ctx, u.UserID); err == nil {
		return resolved.InputPeer()
	}
	return user
}

// SendMessage sends a text message to the specified peer and returns the created tg.Message if available.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
// If a short FloodWait is encountered (<= 5s), it automatically waits and retries once.
func (s *Service) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}

	return retryOnFloodWait(ctx, func() (*tg.Message, error) {
		updates, err := s.sender.To(peer).StyledText(ctx, html.String(nil, text))
		if err != nil {
			if _, isFlood := tgerr.AsFloodWait(err); isFlood {
				return nil, err
			}
			// Fallback to plain text if HTML parsing or formatting fails
			updates, err = s.sender.To(peer).Text(ctx, text)
			if err != nil {
				return nil, err
			}
		}
		return extractMessageFromUpdates(updates), nil
	})
}

// EditMessage edits the text of an existing message.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
// If a short FloodWait is encountered (<= 5s), it automatically waits and retries once.
func (s *Service) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if s.sender == nil {
		return fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.sender.To(peer).Edit(msgID).StyledText(ctx, html.String(nil, text))
		if err != nil {
			if _, isFlood := tgerr.AsFloodWait(err); isFlood {
				return struct{}{}, err
			}
			_, err = s.sender.To(peer).Edit(msgID).Text(ctx, text)
		}
		return struct{}{}, err
	})
	return err
}

// DeleteMessage deletes messages for everyone (revokes).
func (s *Service) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsDeleteMessages(ctx, &tg.ChannelsDeleteMessagesRequest{
				Channel: &tg.InputChannel{
					ChannelID:  ch.ChannelID,
					AccessHash: ch.AccessHash,
				},
				ID: msgIDs,
			})
			return struct{}{}, err
		})
		return err
	}

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.sender.To(peer).Revoke().Messages(ctx, msgIDs...)
		return struct{}{}, err
	})
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

	peer = s.ensureChannelAccessHash(ctx, peer)

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
		if s.peerManager != nil {
			switch m := res.(type) {
			case *tg.MessagesChannelMessages:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			case *tg.MessagesMessages:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			case *tg.MessagesMessagesSlice:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			}
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
		if s.peerManager != nil {
			switch m := res.(type) {
			case *tg.MessagesChannelMessages:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			case *tg.MessagesMessages:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			case *tg.MessagesMessagesSlice:
				_ = s.peerManager.Apply(ctx, m.Users, m.Chats)
			}
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
	peer = s.ensureChannelAccessHash(ctx, peer)
	req := &tg.MessagesUpdatePinnedMessageRequest{
		Silent: silent,
		Unpin:  false,
		Peer:   peer,
		ID:     msgID,
	}
	if silent {
		req.SetSilent(true)
	}
	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.api.MessagesUpdatePinnedMessage(ctx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
	return err
}

// UnpinMessage unpins a message in the chat.
func (s *Service) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	peer = s.ensureChannelAccessHash(ctx, peer)
	req := &tg.MessagesUpdatePinnedMessageRequest{
		Unpin: true,
		Peer:  peer,
		ID:    msgID,
	}
	req.SetUnpin(true)
	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.api.MessagesUpdatePinnedMessage(ctx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
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

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		participant := s.ensureUserAccessHash(ctx, user)
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant: participant,
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
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
				return struct{}{}, nil
			}
			return struct{}{}, err
		})
		return err
	}

	if chat, ok := peer.(*tg.InputPeerChat); ok {
		if u, ok := user.(*tg.InputPeerUser); ok {
			_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
				_, err := s.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
					ChatID: chat.ChatID,
					UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
				})
				return struct{}{}, err
			})
			return err
		}
	}

	return fmt.Errorf("%w: unsupported peer type for ban: %T", core.ErrUnsupported, peer)
}

// UnbanUser removes all ban restrictions on a user.
func (s *Service) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		participant := s.ensureUserAccessHash(ctx, user)
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  participant,
				BannedRights: tg.ChatBannedRights{}, // reset all rights
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED", "USER_NOT_PARTICIPANT") {
				return struct{}{}, nil
			}
			return struct{}{}, err
		})
		return err
	}

	return fmt.Errorf("%w: unban is only supported in supergroups and channels (got %T)", core.ErrUnsupported, peer)
}

// KickUser removes a user from the group while allowing them to rejoin.
func (s *Service) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		channel := &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash}
		participant := s.ensureUserAccessHash(ctx, user)
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:      channel,
				Participant:  participant,
				BannedRights: tg.ChatBannedRights{ViewMessages: true, UntilDate: int(time.Now().Unix() + 60)},
			})
			return struct{}{}, err
		})
		if err != nil {
			return err
		}
		_, err = retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:      channel,
				Participant:  participant,
				BannedRights: tg.ChatBannedRights{}, // allow rejoin
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED") {
				return struct{}{}, nil
			}
			return struct{}{}, err
		})
		return err
	}

	if chat, ok := peer.(*tg.InputPeerChat); ok {
		if u, ok := user.(*tg.InputPeerUser); ok {
			_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
				_, err := s.api.MessagesDeleteChatUser(ctx, &tg.MessagesDeleteChatUserRequest{
					ChatID: chat.ChatID,
					UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
				})
				return struct{}{}, err
			})
			return err
		}
	}

	return fmt.Errorf("%w: unsupported peer type for kick: %T", core.ErrUnsupported, peer)
}

// MuteUser restricts a user from sending messages and media until untilDate.
func (s *Service) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		participant := s.ensureUserAccessHash(ctx, user)
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant: participant,
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
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
				return struct{}{}, nil
			}
			return struct{}{}, err
		})
		return err
	}

	return fmt.Errorf("%w: mute is only supported in supergroups and channels (got %T)", core.ErrUnsupported, peer)
}

// UnmuteUser removes send message restrictions on a user.
func (s *Service) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	if ch, ok := peer.(*tg.InputPeerChannel); ok {
		participant := s.ensureUserAccessHash(ctx, user)
		_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
			_, err := s.api.ChannelsEditBanned(ctx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  participant,
				BannedRights: tg.ChatBannedRights{},
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED", "USER_NOT_PARTICIPANT") {
				return struct{}{}, nil
			}
			return struct{}{}, err
		})
		return err
	}

	return fmt.Errorf("%w: unmute is only supported in supergroups and channels (got %T)", core.ErrUnsupported, peer)
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
		if err != nil {
			return 0, fmt.Errorf("failed to fetch topic replies for purge: %w", err)
		}
		if resp != nil {
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
		if err != nil {
			return 0, fmt.Errorf("failed to fetch message history for purge: %w", err)
		}
		if resp != nil {
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

	peer = s.ensureChannelAccessHash(ctx, peer)

	var res *tg.MessagesChatFull
	var err error

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		res, err = s.api.ChannelsGetFullChannel(ctx, &tg.InputChannel{
			ChannelID:  p.ChannelID,
			AccessHash: p.AccessHash,
		})
	case *tg.InputPeerChat:
		res, err = s.api.MessagesGetFullChat(ctx, p.ChatID)
	default:
		return nil, errors.New("chat info is only available for groups, supergroups, and channels")
	}

	if err == nil && res != nil && s.peerManager != nil {
		_ = s.peerManager.Apply(ctx, res.Users, res.Chats)
	}

	return res, err
}

// PromoteAdmin promotes a user to administrator in the supergroup/channel with custom title.
func (s *Service) PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("%w: promote is only supported in supergroups/channels, got: %T", core.ErrUnsupported, peer)
	}

	user = s.ensureUserAccessHash(ctx, user)
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

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.api.ChannelsEditAdmin(ctx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
	return err
}

// DemoteAdmin demotes an administrator to regular member in the supergroup/channel.
func (s *Service) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	peer = s.ensureChannelAccessHash(ctx, peer)

	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("%w: demote is only supported in supergroups/channels, got: %T", core.ErrUnsupported, peer)
	}

	user = s.ensureUserAccessHash(ctx, user)
	u, ok := user.(*tg.InputPeerUser)
	if !ok {
		return fmt.Errorf("target user must be an InputPeerUser, got: %T", user)
	}

	req := &tg.ChannelsEditAdminRequest{
		Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
		UserID:      &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
		AdminRights: tg.ChatAdminRights{}, // empty rights removes admin status
	}

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.api.ChannelsEditAdmin(ctx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
	return err
}

// EditChatDefaultBannedRights updates the default chat permissions (locks) in a group/supergroup.
func (s *Service) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok {
		return fmt.Errorf("%w: permissions lock is only supported in supergroups, got %T", core.ErrUnsupported, peer)
	}

	req := &tg.MessagesEditChatDefaultBannedRightsRequest{
		Peer:         ch,
		BannedRights: rights,
	}

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.api.MessagesEditChatDefaultBannedRights(ctx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED") {
			// Rights are already in the requested state; treat as idempotent success.
			return struct{}{}, nil
		}
		return struct{}{}, err
	})
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
