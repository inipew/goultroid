package telegram

import (
	"context"
	"fmt"
	"strings"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// QueryInlineBot queries one inline bot through the production userbot MTProto
// transport. The bot username and destination peer are resolved/refreshed by
// the same resolver and RPC executor used by the rest of Telegram service.
func (s *Service) QueryInlineBot(ctx context.Context, botUsername string, peer tg.InputPeerClass, query, offset string) (*tg.MessagesBotResults, error) {
	if s == nil || s.api == nil || s.resolver == nil {
		return nil, fmt.Errorf("%w: self-inline Telegram transport is unavailable", core.ErrUnavailable)
	}
	if peer == nil {
		return nil, fmt.Errorf("%w: inline destination peer is required", core.ErrInvalidArgs)
	}
	botUsername = strings.TrimPrefix(strings.TrimSpace(botUsername), "@")
	if botUsername == "" {
		return nil, fmt.Errorf("%w: inline bot username is required", core.ErrInvalidArgs)
	}
	botPeer, _, err := s.resolver.ResolveUser(ctx, botUsername)
	if err != nil {
		return nil, err
	}
	botPeer = s.RefreshPeerAccessHash(ctx, botPeer)
	userPeer, ok := botPeer.(*tg.InputPeerUser)
	if !ok || userPeer.AccessHash == 0 {
		return nil, fmt.Errorf("%w: inline bot resolved without user access hash", core.ErrAccessHashMissing)
	}
	bot := &tg.InputUser{UserID: userPeer.UserID, AccessHash: userPeer.AccessHash}

	return s.execReadOnlyPeerVal(ctx, "messages.getInlineBotResults", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.MessagesBotResults, error) {
		return s.api.MessagesGetInlineBotResults(opCtx, &tg.MessagesGetInlineBotResultsRequest{
			Bot:    bot,
			Peer:   currentPeer,
			Query:  query,
			Offset: offset,
		})
	})
}

// SendInlineBotResult inserts one result returned by QueryInlineBot. It uses a
// caller-supplied random_id and the shared non-idempotent RPC policy, avoiding a
// second retry/FloodWait authority in the self-inline bridge.
func (s *Service) SendInlineBotResult(
	ctx context.Context,
	peer tg.InputPeerClass,
	queryID int64,
	resultID string,
	randomID int64,
	replyToID int,
	topicID int,
	silent bool,
	hideVia bool,
) error {
	if s == nil || s.api == nil {
		return fmt.Errorf("%w: self-inline Telegram transport is unavailable", core.ErrUnavailable)
	}
	if peer == nil || queryID == 0 || strings.TrimSpace(resultID) == "" || randomID == 0 {
		return fmt.Errorf("%w: invalid inline result send request", core.ErrInvalidArgs)
	}
	_, err := s.execNonIdempotentPeerVal(ctx, "messages.sendInlineBotResult", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.UpdatesClass, error) {
		req := &tg.MessagesSendInlineBotResultRequest{
			Silent:   silent,
			HideVia:  hideVia,
			Peer:     currentPeer,
			RandomID: randomID,
			QueryID:  queryID,
			ID:       strings.TrimSpace(resultID),
		}
		if replyToID > 0 {
			reply := &tg.InputReplyToMessage{ReplyToMsgID: replyToID}
			if topicID > 0 && topicID != replyToID {
				reply.TopMsgID = topicID
			}
			req.ReplyTo = reply
		}
		return s.api.MessagesSendInlineBotResult(opCtx, req)
	})
	return err
}
