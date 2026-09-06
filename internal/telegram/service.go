package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
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
	if errors.Is(err, core.ErrNotFound) || errors.Is(err, core.ErrPermissionDenied) || errors.Is(err, core.ErrRateLimit) || errors.Is(err, core.ErrTelegram) {
		return err
	}
	if wait, ok := tgerr.AsFloodWait(err); ok {
		return core.NewRateLimitError(wait, err)
	}
	if tgerr.Is(err, "CHAT_ID_INVALID", "PEER_ID_INVALID", "USER_ID_INVALID", "MESSAGE_ID_INVALID") {
		return fmt.Errorf("%w: %v", core.ErrNotFound, err)
	}
	if tgerr.Is(err, "CHAT_ADMIN_REQUIRED", "RIGHTS_NOT_MODIFIED", "CHAT_WRITE_FORBIDDEN") {
		return fmt.Errorf("%w: %v", core.ErrPermissionDenied, err)
	}
	return fmt.Errorf("%w: %v", core.ErrTelegram, err)
}

// FullBanRights returns the complete set of chat restrictions representing a full ban.
func FullBanRights(untilDate int) tg.ChatBannedRights {
	return tg.ChatBannedRights{
		ViewMessages:    true,
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
	}
}

// FullMuteRights returns the complete set of chat restrictions representing a mute (can view, but cannot send anything).
func FullMuteRights(untilDate int) tg.ChatBannedRights {
	return tg.ChatBannedRights{
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
	}
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
	storage     peers.Storage
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

// SetStorage sets the persistent peer storage for access hash lookups.
func (s *Service) SetStorage(st peers.Storage) {
	s.storage = st
}

func (s *Service) ensureChannelAccessHash(ctx context.Context, peer tg.InputPeerClass) tg.InputPeerClass {
	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok || ch.AccessHash != 0 {
		return peer
	}
	if s.peerManager != nil {
		if resolved, err := s.peerManager.ResolveChannelID(ctx, ch.ChannelID); err == nil {
			return resolved.InputPeer()
		}
	}
	if s.storage != nil {
		if val, found, err := s.storage.Find(ctx, peers.Key{Prefix: "channel", ID: ch.ChannelID}); err == nil && found && val.AccessHash != 0 {
			return &tg.InputPeerChannel{ChannelID: ch.ChannelID, AccessHash: val.AccessHash}
		}
	}
	return peer
}

func (s *Service) ensureUserAccessHash(ctx context.Context, user tg.InputPeerClass) tg.InputPeerClass {
	u, ok := user.(*tg.InputPeerUser)
	if !ok || u.AccessHash != 0 {
		return user
	}
	if s.peerManager != nil {
		if resolved, err := s.peerManager.ResolveUserID(ctx, u.UserID); err == nil {
			return resolved.InputPeer()
		}
	}
	if s.storage != nil {
		if val, found, err := s.storage.Find(ctx, peers.Key{Prefix: "user", ID: u.UserID}); err == nil && found && val.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: u.UserID, AccessHash: val.AccessHash}
		}
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

// SendMessageWithMarkup sends a text message with reply markup attached.
func (s *Service) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}

	return retryOnFloodWait(ctx, func() (*tg.Message, error) {
		req := s.sender.To(peer)
		var updates tg.UpdatesClass
		var err error

		if markup != nil {
			b := req.Markup(markup)
			updates, err = b.StyledText(ctx, html.String(nil, text))
			if err != nil {
				if _, isFlood := tgerr.AsFloodWait(err); isFlood {
					return nil, err
				}
				updates, err = b.Text(ctx, text)
			}
		} else {
			updates, err = req.StyledText(ctx, html.String(nil, text))
			if err != nil {
				if _, isFlood := tgerr.AsFloodWait(err); isFlood {
					return nil, err
				}
				updates, err = req.Text(ctx, text)
			}
		}

		if err != nil {
			return nil, err
		}
		return extractMessageFromUpdates(updates), nil
	})
}

// EditMessageMarkup edits an existing message text and updates or sets its reply markup.
func (s *Service) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}

	peer = s.ensureChannelAccessHash(ctx, peer)
	req := &tg.MessagesEditMessageRequest{
		Peer: peer,
		ID:   msgID,
	}
	req.SetMessage(text)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}

	_, err := retryOnFloodWait(ctx, func() (tg.UpdatesClass, error) {
		return s.api.MessagesEditMessage(ctx, req)
	})
	return err
}

// AnswerCallbackQuery sends an answer to a bot callback query.
func (s *Service) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}

	req := &tg.MessagesSetBotCallbackAnswerRequest{
		QueryID: queryID,
		Message: text,
		Alert:   alert,
	}
	if alert {
		req.SetFlags()
	}

	_, err := retryOnFloodWait(ctx, func() (bool, error) {
		return s.api.MessagesSetBotCallbackAnswer(ctx, req)
	})
	return err
}

// AnswerInlineQuery answers an inline query with the prepared results.
func (s *Service) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}

	req := &tg.MessagesSetInlineBotResultsRequest{
		QueryID:    queryID,
		Results:    results,
		CacheTime:  cacheTime,
		NextOffset: nextOffset,
	}
	if nextOffset != "" {
		req.SetFlags()
	}

	_, err := retryOnFloodWait(ctx, func() (bool, error) {
		return s.api.MessagesSetInlineBotResults(ctx, req)
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
		return fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}

	peer = s.ensureChannelAccessHash(ctx, peer)
	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.sender.To(peer).Reaction(ctx, msgID, &tg.ReactionEmoji{Emoticon: emoji})
		return struct{}{}, err
	})
	return err
}

// GetMessage fetches a message by its ID. Returns (nil, core.ErrNotFound) if message does not exist.
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
			return nil, mapTelegramError(err)
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
			return nil, mapTelegramError(err)
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

	return nil, fmt.Errorf("%w: message %d not found", core.ErrNotFound, msgID)
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

// ForwardMessages forwards messages from fromPeer to toPeer with access hash normalization.
func (s *Service) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	if s.sender == nil {
		return fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}
	if len(msgIDs) == 0 {
		return nil
	}

	fromPeer = s.ensureChannelAccessHash(ctx, fromPeer)
	fromPeer = s.ensureUserAccessHash(ctx, fromPeer)
	toPeer = s.ensureChannelAccessHash(ctx, toPeer)
	toPeer = s.ensureUserAccessHash(ctx, toPeer)

	_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
		_, err := s.sender.To(toPeer).ForwardIDs(fromPeer, msgIDs[0], msgIDs[1:]...).Send(ctx)
		return struct{}{}, err
	})
	return err
}

type boundedWriter struct {
	writer  io.Writer
	limit   int64
	written int64
}

func (w *boundedWriter) Write(p []byte) (int, error) {
	if w.written+int64(len(p)) > w.limit {
		return 0, fmt.Errorf("%w: streamed download exceeded safety limit (%d bytes)", core.ErrMediaTooLarge, w.limit)
	}
	n, err := w.writer.Write(p)
	w.written += int64(n)
	return n, err
}

// DownloadFile streams and downloads a Telegram media file to local destination path.
// It wraps the destination in a boundedWriter to enforce real-time streaming byte limits (500MB),
// preventing transient disk and bandwidth exhaustion even when media metadata size is 0 or unknown.
func (s *Service) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	if s.downloader == nil {
		s.downloader = downloader.NewDownloader()
	}

	file, err := os.OpenFile(dstPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0600)
	if err != nil {
		return fmt.Errorf("failed to create destination file: %w", err)
	}
	defer file.Close()

	bw := &boundedWriter{
		writer: file,
		limit:  core.DefaultMaxDownloadSize,
	}

	_, err = s.downloader.Download(s.api, location).Stream(ctx, bw)
	if err != nil {
		_ = os.Remove(dstPath)
		return mapTelegramError(err)
	}
	return nil
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
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  participant,
				BannedRights: FullBanRights(untilDate),
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
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  participant,
				BannedRights: FullMuteRights(untilDate),
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
// MaxPurgeBatchLimit defines the maximum number of messages that can be purged in a single call.
const MaxPurgeBatchLimit = 1000

// PurgeMessages purges messages in the range [fromID..toID]. If topicID > 0, it uses MessagesGetReplies
// to ensure only messages inside that forum topic/thread are deleted. It paginates backwards from maxID
// to minID until all messages in the range are collected or MaxPurgeBatchLimit is reached.
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

	currentOffsetID := maxID + 1

	for len(msgIDsMap) < MaxPurgeBatchLimit {
		var (
			messages []tg.MessageClass
			fetchErr error
		)

		if topicID > 0 {
			// Topic-scoped purge via GetReplies with pagination
			_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
				resp, err := s.api.MessagesGetReplies(ctx, &tg.MessagesGetRepliesRequest{
					Peer:     peer,
					MsgID:    topicID,
					OffsetID: currentOffsetID,
					MinID:    minID - 1,
					MaxID:    maxID + 1,
					Limit:    100,
				})
				if err != nil {
					return struct{}{}, err
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return struct{}{}, nil
			})
			fetchErr = err
		} else {
			// Non-topic history fetch with pagination
			_, err := retryOnFloodWait(ctx, func() (struct{}, error) {
				resp, err := s.api.MessagesGetHistory(ctx, &tg.MessagesGetHistoryRequest{
					Peer:     peer,
					OffsetID: currentOffsetID,
					MinID:    minID - 1,
					MaxID:    maxID + 1,
					Limit:    100,
				})
				if err != nil {
					return struct{}{}, err
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return struct{}{}, nil
			})
			fetchErr = err
		}

		if fetchErr != nil {
			if len(msgIDsMap) <= 2 {
				return 0, fmt.Errorf("failed to fetch messages for purge: %w", fetchErr)
			}
			break
		}

		if len(messages) == 0 {
			break
		}

		lowestIDInBatch := currentOffsetID
		newFound := 0

		for _, m := range messages {
			if msg, ok := m.(*tg.Message); ok {
				if msg.ID < lowestIDInBatch {
					lowestIDInBatch = msg.ID
				}
				if msg.ID >= minID && msg.ID <= maxID {
					if _, exists := msgIDsMap[msg.ID]; !exists {
						msgIDsMap[msg.ID] = struct{}{}
						newFound++
					}
				}
			}
		}

		if lowestIDInBatch >= currentOffsetID || lowestIDInBatch <= minID || newFound == 0 {
			break
		}

		currentOffsetID = lowestIDInBatch
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
	stat, err := os.Stat(filePath)
	if err != nil {
		return nil, fmt.Errorf("failed to inspect file %q: %w", filePath, err)
	}
	if stat.Size() > core.DefaultMaxUploadSize {
		return nil, fmt.Errorf("%w: file size (%d bytes) exceeds maximum upload limit (500MB)", core.ErrMediaTooLarge, stat.Size())
	}

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
		return nil, fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
	}
	res, err := s.api.UsersGetFullUser(ctx, user)
	if err != nil {
		return nil, mapTelegramError(err)
	}
	return res, nil
}

// ResolveUsername resolves a public @username to peer entities.
func (s *Service) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
	}
	cleaned := strings.TrimPrefix(username, "@")
	res, err := s.api.ContactsResolveUsername(ctx, &tg.ContactsResolveUsernameRequest{Username: cleaned})
	if err != nil {
		return nil, mapTelegramError(err)
	}
	return res, nil
}

// GetFullChat retrieves extended information for a group, supergroup, or channel.
func (s *Service) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
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
		return nil, fmt.Errorf("%w: chat info is only available for groups, supergroups, and channels", core.ErrUnsupported)
	}

	if err != nil {
		return nil, mapTelegramError(err)
	}

	if res != nil && s.peerManager != nil {
		_ = s.peerManager.Apply(ctx, res.Users, res.Chats)
	}

	return res, nil
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

// UpdateProfile updates the account's first name, last name, and/or about (bio).
func (s *Service) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	if s.api == nil {
		return fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}

	req := &tg.AccountUpdateProfileRequest{}
	if firstName != nil {
		req.SetFirstName(*firstName)
	}
	if lastName != nil {
		req.SetLastName(*lastName)
	}
	if about != nil {
		req.SetAbout(*about)
	}

	_, err := retryOnFloodWait(ctx, func() (tg.UserClass, error) {
		return s.api.AccountUpdateProfile(ctx, req)
	})
	return err
}

// BlockUser blocks the specified peer from sending messages or calling.
func (s *Service) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	peer = s.ensureUserAccessHash(ctx, peer)

	_, err := retryOnFloodWait(ctx, func() (bool, error) {
		return s.api.ContactsBlock(ctx, &tg.ContactsBlockRequest{ID: peer})
	})
	return err
}

// UnblockUser removes the specified peer from the blocklist.
func (s *Service) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	peer = s.ensureUserAccessHash(ctx, peer)

	_, err := retryOnFloodWait(ctx, func() (bool, error) {
		return s.api.ContactsUnblock(ctx, &tg.ContactsUnblockRequest{ID: peer})
	})
	return err
}

// UploadProfilePhoto uploads an image file and sets it as the account's profile photo.
func (s *Service) UploadProfilePhoto(ctx context.Context, filePath string) error {
	stat, err := os.Stat(filePath)
	if err != nil {
		return fmt.Errorf("failed to inspect photo file %q: %w", filePath, err)
	}
	if stat.Size() > core.DefaultMaxUploadSize {
		return fmt.Errorf("%w: photo file size (%d bytes) exceeds upload limit (500MB)", core.ErrMediaTooLarge, stat.Size())
	}

	if s.api == nil || s.uploader == nil {
		return fmt.Errorf("%w: api/uploader is not initialized", core.ErrInternal)
	}

	inputFile, err := s.uploader.FromPath(ctx, filePath)
	if err != nil {
		return fmt.Errorf("failed to upload photo file: %w", err)
	}

	_, err = retryOnFloodWait(ctx, func() (*tg.PhotosPhoto, error) {
		return s.api.PhotosUploadProfilePhoto(ctx, &tg.PhotosUploadProfilePhotoRequest{
			File: inputFile,
		})
	})
	return err
}

// DeleteProfilePhotos deletes up to limit recent profile photos. Returns number of deleted photos.
func (s *Service) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	if s.api == nil {
		return 0, fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	if limit <= 0 {
		limit = 1
	}

	photosRes, err := retryOnFloodWait(ctx, func() (tg.PhotosPhotosClass, error) {
		return s.api.PhotosGetUserPhotos(ctx, &tg.PhotosGetUserPhotosRequest{
			UserID: &tg.InputUserSelf{},
			Offset: 0,
			MaxID:  0,
			Limit:  limit,
		})
	})
	if err != nil {
		return 0, err
	}

	var photos []tg.PhotoClass
	switch p := photosRes.(type) {
	case *tg.PhotosPhotos:
		photos = p.Photos
	case *tg.PhotosPhotosSlice:
		photos = p.Photos
	}

	if len(photos) == 0 {
		return 0, nil
	}

	var inputPhotos []tg.InputPhotoClass
	for _, p := range photos {
		if photo, ok := p.(*tg.Photo); ok {
			inputPhotos = append(inputPhotos, &tg.InputPhoto{
				ID:            photo.ID,
				AccessHash:    photo.AccessHash,
				FileReference: photo.FileReference,
			})
		}
	}

	if len(inputPhotos) == 0 {
		return 0, nil
	}

	deletedIDs, err := retryOnFloodWait(ctx, func() ([]int64, error) {
		return s.api.PhotosDeletePhotos(ctx, inputPhotos)
	})
	if err != nil {
		return 0, err
	}
	return len(deletedIDs), nil
}

// GetDialogs returns recent active dialogs/chats up to limit (capped at 30).
func (s *Service) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 30 {
		limit = 30
	}

	res, err := retryOnFloodWait(ctx, func() (tg.MessagesDialogsClass, error) {
		return s.api.MessagesGetDialogs(ctx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      limit,
		})
	})
	if err != nil {
		return nil, err
	}

	var chats []tg.ChatClass
	switch d := res.(type) {
	case *tg.MessagesDialogs:
		chats = d.Chats
	case *tg.MessagesDialogsSlice:
		chats = d.Chats
	}

	var result []*core.Chat
	for _, c := range chats {
		switch ch := c.(type) {
		case *tg.Channel:
			chatType := "channel"
			if ch.Megagroup {
				chatType = "supergroup"
			}
			result = append(result, &core.Chat{
				ID:       ch.ID,
				Title:    ch.Title,
				Username: ch.Username,
				Type:     chatType,
			})
		case *tg.Chat:
			result = append(result, &core.Chat{
				ID:    ch.ID,
				Title: ch.Title,
				Type:  "group",
			})
		}
	}
	return result, nil
}

// GetContacts returns all saved contacts for the logged-in user.
func (s *Service) GetContacts(ctx context.Context) ([]*core.User, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}

	res, err := retryOnFloodWait(ctx, func() (tg.ContactsContactsClass, error) {
		return s.api.ContactsGetContacts(ctx, 0)
	})
	if err != nil {
		return nil, err
	}

	contacts, ok := res.(*tg.ContactsContacts)
	if !ok {
		return nil, nil
	}

	var users []*core.User
	for _, u := range contacts.Users {
		if usr, ok := u.(*tg.User); ok {
			users = append(users, &core.User{
				ID:        usr.ID,
				FirstName: usr.FirstName,
				LastName:  usr.LastName,
				Username:  usr.Username,
				IsBot:     usr.Bot,
			})
		}
	}
	return users, nil
}
