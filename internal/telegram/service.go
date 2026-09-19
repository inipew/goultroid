package telegram

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/telegram/downloader"
	"github.com/gotd/td/telegram/message"
	"github.com/gotd/td/telegram/message/entity"
	"github.com/gotd/td/telegram/message/html"
	"github.com/gotd/td/telegram/message/styling"
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
	// RIGHTS_NOT_MODIFIED and CHAT_NOT_MODIFIED indicate the requested permissions/state
	// are already in place; treat as idempotent success.
	if tgerr.Is(err, "RIGHTS_NOT_MODIFIED", "CHAT_NOT_MODIFIED") {
		return nil
	}
	if tgerr.Is(err, "CHAT_ID_INVALID", "PEER_ID_INVALID", "USER_ID_INVALID", "MESSAGE_ID_INVALID") {
		return fmt.Errorf("%w: %w", core.ErrNotFound, err)
	}
	if tgerr.Is(err, "CHAT_ADMIN_REQUIRED", "CHAT_WRITE_FORBIDDEN") {
		return fmt.Errorf("%w: %w", core.ErrPermissionDenied, err)
	}
	return fmt.Errorf("%w: %w", core.ErrTelegram, err)
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

// Service provides high-level Telegram operations implementing core.TelegramServicer.
type Service struct {
	api         *tg.Client
	executor    *RPCExecutor
	sender      *message.Sender
	downloader  *downloader.Downloader
	uploader    *uploader.Uploader
	peerManager *peers.Manager
	storage     peers.Storage
	resolver    *Resolver

	botSentMu       sync.RWMutex
	botSentMessages map[string]time.Time
}

func newStandaloneServiceExecutor() *RPCExecutor {
	exec, err := NewRPCExecutor(RPCExecutorConfig{
		Limiter:       NewHierarchicalRPCLimiter(DefaultHierarchicalLimiterConfig()),
		DefaultPolicy: defaultExecutorPolicy(),
	})
	if err != nil {
		return nil
	}
	return exec
}

// NewService creates a standalone Service with one bounded executor. Production
// wiring should use NewServiceWithExecutor so every surface shares the client
// executor and its limiter/metrics state.
func NewService(api *tg.Client) *Service {
	return NewServiceWithExecutor(api, nil)
}

// NewServiceWithExecutor creates a Service using the supplied shared executor.
// A nil executor gets one standalone bounded executor for compatibility.
func NewServiceWithExecutor(api *tg.Client, exec *RPCExecutor) *Service {
	if exec == nil {
		exec = newStandaloneServiceExecutor()
	}
	s := &Service{
		api:             api,
		executor:        exec,
		sender:          message.NewSender(api),
		downloader:      downloader.NewDownloader(),
		botSentMessages: make(map[string]time.Time),
	}
	if api != nil {
		s.uploader = uploader.NewUploader(&managedUploadRPCClient{
			raw:      api,
			executor: s.getExecutor,
		})
	}
	return s
}

// SetExecutor configures the shared RPCExecutor for coordinating Telegram calls.
func (s *Service) SetExecutor(exec *RPCExecutor) {
	if s != nil && exec != nil {
		s.executor = exec
	}
}

func (s *Service) getExecutor() *RPCExecutor {
	if s == nil {
		return nil
	}
	return s.executor
}

func executeServiceRPC[T any](ctx context.Context, s *Service, meta RPCMeta, op func(opCtx context.Context) (T, error)) (T, error) {
	if meta.Family == "" {
		if family, _, ok := strings.Cut(meta.Method, "."); ok {
			meta.Family = family
		}
	}
	if meta.RefreshPeer == nil && meta.PeerKey != "" && s != nil && s.resolver != nil {
		meta.RefreshPeer = func(refreshCtx context.Context) error {
			return s.refreshPeer(refreshCtx, meta.PeerKey)
		}
	}
	exec := s.getExecutor()
	if exec == nil {
		var zero T
		return zero, fmt.Errorf("%w: telegram RPC executor is not configured", core.ErrInternal)
	}
	res, err := ExecuteRPC(ctx, exec, meta, op)
	if err != nil {
		var failure *RPCFailure
		if errors.As(err, &failure) {
			return res, mapTelegramError(failure.Err)
		}
		return res, mapTelegramError(err)
	}
	return res, nil
}

func (s *Service) execReadOnly(ctx context.Context, method string, op func(opCtx context.Context) error) error {
	_, err := executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCReadOnly,
	}, func(opCtx context.Context) (struct{}, error) {
		return struct{}{}, op(opCtx)
	})
	return err
}

func (s *Service) execReadOnlyVal[T any](ctx context.Context, method string, op func(opCtx context.Context) (T, error)) (T, error) {
	return executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCReadOnly,
	}, op)
}

func (s *Service) execIdempotent(ctx context.Context, method string, op func(opCtx context.Context) error) error {
	_, err := executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCIdempotentMutation,
	}, func(opCtx context.Context) (struct{}, error) {
		return struct{}{}, op(opCtx)
	})
	return err
}

func (s *Service) execIdempotentVal[T any](ctx context.Context, method string, op func(opCtx context.Context) (T, error)) (T, error) {
	return executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCIdempotentMutation,
	}, op)
}

func (s *Service) execNonIdempotent(ctx context.Context, method string, op func(opCtx context.Context) error) error {
	_, err := executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCNonIdempotentMutation,
		RetryPolicy: RetryPolicy{
			MaxAttempts:        1,
			InlineFloodWaitMax: defaultFloodWaitRetryLimit,
		},
	}, func(opCtx context.Context) (struct{}, error) {
		return struct{}{}, op(opCtx)
	})
	return err
}

func (s *Service) execNonIdempotentVal[T any](ctx context.Context, method string, op func(opCtx context.Context) (T, error)) (T, error) {
	return executeServiceRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCNonIdempotentMutation,
		RetryPolicy: RetryPolicy{
			MaxAttempts:        1,
			InlineFloodWaitMax: defaultFloodWaitRetryLimit,
		},
	}, op)
}

func peerRPCLimitKey(peer tg.InputPeerClass) LimitKey {
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return LimitKey{Scope: "peer", Key: "user", ID: p.UserID}
	case *tg.InputPeerChannel:
		return LimitKey{Scope: "peer", Key: "channel", ID: p.ChannelID}
	case *tg.InputPeerChat:
		return LimitKey{Scope: "peer", Key: "chat", ID: p.ChatID}
	default:
		return LimitKey{}
	}
}

func executeServicePeersRPC[T any](ctx context.Context, s *Service, meta RPCMeta, original []tg.InputPeerClass, op func(context.Context, []tg.InputPeerClass) (T, error)) (T, error) {
	if len(original) > 0 && meta.PeerKey == "" && meta.PeerLimitKey.Scope == "" {
		meta.PeerLimitKey = peerRPCLimitKey(original[0])
	}
	if meta.RefreshPeer == nil && s != nil && s.resolver != nil {
		meta.RefreshPeer = func(refreshCtx context.Context) error {
			var refreshErrs []error
			refreshed := false
			refreshable := 0
			for _, peer := range original {
				switch peer.(type) {
				case *tg.InputPeerUser, *tg.InputPeerChannel:
					refreshable++
				default:
					continue
				}
				if err := s.resolver.RefreshPeer(refreshCtx, peer); err != nil {
					refreshErrs = append(refreshErrs, err)
					continue
				}
				refreshed = true
			}
			if refreshed || refreshable == 0 {
				return nil
			}
			return errors.Join(refreshErrs...)
		}
	}
	return executeServiceRPC(ctx, s, meta, func(opCtx context.Context) (T, error) {
		current := make([]tg.InputPeerClass, len(original))
		for i, peer := range original {
			current[i] = s.RefreshPeerAccessHash(opCtx, peer)
		}
		return op(opCtx, current)
	})
}

func executeServicePeerRPC[T any](ctx context.Context, s *Service, meta RPCMeta, original tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) (T, error)) (T, error) {
	if meta.PeerKey == "" && meta.PeerLimitKey.Scope == "" {
		meta.PeerLimitKey = peerRPCLimitKey(original)
	}
	if meta.RefreshPeer == nil && s != nil && s.resolver != nil {
		switch original.(type) {
		case *tg.InputPeerUser, *tg.InputPeerChannel:
			meta.RefreshPeer = func(refreshCtx context.Context) error {
				return s.resolver.RefreshPeer(refreshCtx, original)
			}
		}
	}
	return executeServiceRPC(ctx, s, meta, func(opCtx context.Context) (T, error) {
		return op(opCtx, s.RefreshPeerAccessHash(opCtx, original))
	})
}

func (s *Service) execReadOnlyPeerVal[T any](ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) (T, error)) (T, error) {
	return executeServicePeerRPC(ctx, s, RPCMeta{Method: method, Kind: RPCReadOnly}, peer, op)
}

func (s *Service) execReadOnlyPeer(ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) error) error {
	_, err := s.execReadOnlyPeerVal(ctx, method, peer, func(opCtx context.Context, current tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func (s *Service) execIdempotentPeerVal[T any](ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) (T, error)) (T, error) {
	return executeServicePeerRPC(ctx, s, RPCMeta{Method: method, Kind: RPCIdempotentMutation}, peer, op)
}

func (s *Service) execIdempotentPeer(ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) error) error {
	_, err := s.execIdempotentPeerVal(ctx, method, peer, func(opCtx context.Context, current tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func (s *Service) execReadOnlyPeers(ctx context.Context, method string, peers []tg.InputPeerClass, op func(context.Context, []tg.InputPeerClass) error) error {
	_, err := executeServicePeersRPC(ctx, s, RPCMeta{Method: method, Kind: RPCReadOnly}, peers, func(opCtx context.Context, current []tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func (s *Service) execIdempotentPeers(ctx context.Context, method string, peers []tg.InputPeerClass, op func(context.Context, []tg.InputPeerClass) error) error {
	_, err := executeServicePeersRPC(ctx, s, RPCMeta{Method: method, Kind: RPCIdempotentMutation}, peers, func(opCtx context.Context, current []tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func (s *Service) execNonIdempotentPeers(ctx context.Context, method string, peers []tg.InputPeerClass, op func(context.Context, []tg.InputPeerClass) error) error {
	_, err := executeServicePeersRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCNonIdempotentMutation,
		RetryPolicy: RetryPolicy{MaxAttempts: 1, InlineFloodWaitMax: defaultFloodWaitRetryLimit},
	}, peers, func(opCtx context.Context, current []tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func (s *Service) execNonIdempotentPeerVal[T any](ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) (T, error)) (T, error) {
	return executeServicePeerRPC(ctx, s, RPCMeta{
		Method: method,
		Kind:   RPCNonIdempotentMutation,
		RetryPolicy: RetryPolicy{MaxAttempts: 1, InlineFloodWaitMax: defaultFloodWaitRetryLimit},
	}, peer, op)
}

func (s *Service) execNonIdempotentPeer(ctx context.Context, method string, peer tg.InputPeerClass, op func(context.Context, tg.InputPeerClass) error) error {
	_, err := s.execNonIdempotentPeerVal(ctx, method, peer, func(opCtx context.Context, current tg.InputPeerClass) (struct{}, error) {
		return struct{}{}, op(opCtx, current)
	})
	return err
}

func botSentInputKey(peer tg.InputPeerClass, msgID int) string {
	if msgID == 0 || peer == nil {
		return ""
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		return fmt.Sprintf("user:%d:%d", p.UserID, msgID)
	case *tg.InputPeerChannel:
		return fmt.Sprintf("channel:%d:%d", p.ChannelID, msgID)
	case *tg.InputPeerChat:
		return fmt.Sprintf("chat:%d:%d", p.ChatID, msgID)
	case *tg.InputPeerSelf:
		return fmt.Sprintf("self:%d", msgID)
	default:
		return ""
	}
}

func botSentPeerKey(peer tg.PeerClass, msgID int) string {
	if msgID == 0 || peer == nil {
		return ""
	}
	switch p := peer.(type) {
	case *tg.PeerUser:
		return fmt.Sprintf("user:%d:%d", p.UserID, msgID)
	case *tg.PeerChannel:
		return fmt.Sprintf("channel:%d:%d", p.ChannelID, msgID)
	case *tg.PeerChat:
		return fmt.Sprintf("chat:%d:%d", p.ChatID, msgID)
	default:
		return ""
	}
}

func (s *Service) recordBotSent(peer tg.InputPeerClass, msgID int) {
	if s == nil || msgID == 0 {
		return
	}
	key := botSentInputKey(peer, msgID)
	if key == "" {
		return
	}
	s.botSentMu.Lock()
	defer s.botSentMu.Unlock()
	if s.botSentMessages == nil {
		s.botSentMessages = make(map[string]time.Time)
	}
	now := time.Now()
	s.botSentMessages[key] = now
	if len(s.botSentMessages) > 200 {
		cutoff := now.Add(-5 * time.Minute)
		for id, t := range s.botSentMessages {
			if t.Before(cutoff) {
				delete(s.botSentMessages, id)
			}
		}
	}
}

// IsBotSentForPeer reports whether this bot instance sent msgID to the exact
// peer. InputPeerSelf records are accepted only for the current self user.
func (s *Service) IsBotSentForPeer(peer tg.PeerClass, msgID int, selfID int64) bool {
	if s == nil || msgID == 0 {
		return false
	}
	keys := []string{botSentPeerKey(peer, msgID)}
	if p, ok := peer.(*tg.PeerUser); ok && selfID != 0 && p.UserID == selfID {
		keys = append(keys, fmt.Sprintf("self:%d", msgID))
	}
	s.botSentMu.RLock()
	defer s.botSentMu.RUnlock()
	for _, key := range keys {
		if key == "" {
			continue
		}
		if t, ok := s.botSentMessages[key]; ok && time.Since(t) < 5*time.Minute {
			return true
		}
	}
	return false
}

// IsBotSent is retained for compatibility with older callers that do not have
// peer identity. New ingress classification must use IsBotSentForPeer.
func (s *Service) IsBotSent(msgID int) bool {
	if s == nil || msgID == 0 {
		return false
	}
	suffix := fmt.Sprintf(":%d", msgID)
	selfKey := fmt.Sprintf("self:%d", msgID)
	s.botSentMu.RLock()
	defer s.botSentMu.RUnlock()
	for key, t := range s.botSentMessages {
		if (key == selfKey || strings.HasSuffix(key, suffix)) && time.Since(t) < 5*time.Minute {
			return true
		}
	}
	return false
}

// SetPeerManager configures the peers.Manager used for caching and resolving peer access hashes.
func (s *Service) SetPeerManager(pm *peers.Manager) {
	s.peerManager = pm
}

// SetStorage sets the persistent peer storage for access hash lookups.
func (s *Service) SetStorage(st peers.Storage) {
	s.storage = st
}

// SetResolver sets the resolver used for address/username resolution and peer refresh.
func (s *Service) SetResolver(r *Resolver) {
	s.resolver = r
}

func (s *Service) refreshPeer(ctx context.Context, peerKey string) error {
	if s == nil || s.resolver == nil || peerKey == "" {
		return nil
	}
	if err := s.resolver.InvalidateRefContext(ctx, peerKey); err != nil {
		return err
	}
	if strings.HasPrefix(peerKey, "@") || !strings.ContainsAny(peerKey, "0123456789") {
		_, _, err := s.resolver.ResolveUser(ctx, peerKey)
		if err != nil {
			_, err = s.resolver.ResolveChat(ctx, peerKey)
		}
		return err
	}
	return nil
}

func (s *Service) ensureChannelAccessHash(ctx context.Context, peer tg.InputPeerClass) tg.InputPeerClass {
	ch, ok := peer.(*tg.InputPeerChannel)
	if !ok || ch.AccessHash != 0 {
		return peer
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
	if s.storage != nil {
		if val, found, err := s.storage.Find(ctx, peers.Key{Prefix: "user", ID: u.UserID}); err == nil && found && val.AccessHash != 0 {
			return &tg.InputPeerUser{UserID: u.UserID, AccessHash: val.AccessHash}
		}
	}
	return user
}

// RefreshPeerAccessHash retrieves the latest access hash from persistent storage only.
func (s *Service) RefreshPeerAccessHash(ctx context.Context, peer tg.InputPeerClass) tg.InputPeerClass {
	if s == nil || peer == nil {
		return peer
	}
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		if s.storage != nil {
			if val, found, err := s.storage.Find(ctx, peers.Key{Prefix: "user", ID: p.UserID}); err == nil && found && val.AccessHash != 0 {
				if val.AccessHash == p.AccessHash {
					return p
				}
				return &tg.InputPeerUser{UserID: p.UserID, AccessHash: val.AccessHash}
			}
		}
		return p
	case *tg.InputPeerChannel:
		if s.storage != nil {
			if val, found, err := s.storage.Find(ctx, peers.Key{Prefix: "channel", ID: p.ChannelID}); err == nil && found && val.AccessHash != 0 {
				if val.AccessHash == p.AccessHash {
					return p
				}
				return &tg.InputPeerChannel{ChannelID: p.ChannelID, AccessHash: val.AccessHash}
			}
		}
		return p
	default:
		return peer
	}
}

func (s *Service) invalidatePeer(ctx context.Context, peer tg.InputPeerClass) {
	if s == nil || s.storage == nil || peer == nil || ctx == nil {
		return
	}
	type contextInvalidator interface {
		InvalidateContext(context.Context, peers.Key) error
	}
	inv, ok := s.storage.(contextInvalidator)
	if !ok {
		return
	}
	var key peers.Key
	switch p := peer.(type) {
	case *tg.InputPeerUser:
		key = peers.Key{Prefix: "user", ID: p.UserID}
	case *tg.InputPeerChannel:
		key = peers.Key{Prefix: "channel", ID: p.ChannelID}
	case *tg.InputPeerChat:
		key = peers.Key{Prefix: "chat", ID: p.ChatID}
	default:
		return
	}
	_ = inv.InvalidateContext(ctx, key)
}

func (s *Service) checkPeerError(ctx context.Context, err error, peers ...tg.InputPeerClass) {
	if err == nil {
		return
	}
	if tgerr.Is(err, "PEER_ID_INVALID", "CHANNEL_INVALID", "CHANNEL_PRIVATE") {
		for _, p := range peers {
			s.invalidatePeer(ctx, p)
		}
	}
}

// SendMessage sends a text message to the specified peer and returns the created tg.Message if available.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
// If a short FloodWait is encountered (<= 5s), it automatically waits and retries once.
// P1-09: Centralize access-hash preparation before every send.
func (s *Service) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}
	res, err := s.execNonIdempotentPeerVal(ctx, "messages.sendMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.Message, error) {
		updates, err := s.sender.To(currentPeer).StyledText(opCtx, html.String(nil, text))
		if err != nil {
			if _, isFlood := tgerr.AsFloodWait(err); isFlood {
				return nil, err
			}
			// Fallback to plain text if HTML parsing or formatting fails
			updates, err = s.sender.To(currentPeer).Text(opCtx, text)
			if err != nil {
				return nil, err
			}
		}
		return extractMessageFromUpdates(updates), nil
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	if err == nil && res != nil {
		s.recordBotSent(peer, res.ID)
	}
	return res, err
}

// EditMessage edits the text of an existing message.
// It parses HTML formatting, falling back to plain text if parsing or formatting fails.
// If a short FloodWait is encountered (<= 5s), it automatically waits and retries once.
func (s *Service) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	if s.sender == nil {
		return fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}
	err := s.execIdempotentPeer(ctx, "messages.editMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		_, err := s.sender.To(currentPeer).Edit(msgID).StyledText(opCtx, html.String(nil, text))
		if err != nil {
			if _, isFlood := tgerr.AsFloodWait(err); isFlood {
				return err
			}
			_, err = s.sender.To(currentPeer).Edit(msgID).Text(opCtx, text)
		}
		return err
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	return err
}

// SendMessageWithMarkup sends a text message with reply markup attached.
func (s *Service) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s.sender == nil {
		return nil, fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}
	res, err := s.execNonIdempotentPeerVal(ctx, "messages.sendMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.Message, error) {
		req := s.sender.To(currentPeer)
		var updates tg.UpdatesClass
		var err error

		if markup != nil {
			b := req.Markup(markup)
			updates, err = b.StyledText(opCtx, html.String(nil, text))
			if err != nil {
				if _, isFlood := tgerr.AsFloodWait(err); isFlood {
					return nil, err
				}
				updates, err = b.Text(opCtx, text)
			}
		} else {
			updates, err = req.StyledText(opCtx, html.String(nil, text))
			if err != nil {
				if _, isFlood := tgerr.AsFloodWait(err); isFlood {
					return nil, err
				}
				updates, err = req.Text(opCtx, text)
			}
		}

		if err != nil {
			return nil, err
		}
		return extractMessageFromUpdates(updates), nil
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	if err == nil && res != nil {
		s.recordBotSent(peer, res.ID)
	}
	return res, err
}

// parseHTML parses text as HTML into plain text and Telegram message entities.
// Falls back to returning original text without entities if parsing fails.
func parseHTML(text string) (string, []tg.MessageEntityClass) {
	var eb entity.Builder
	if err := styling.Perform(&eb, html.String(nil, text)); err == nil {
		plain, ents := eb.Complete()
		return plain, ents
	}
	return text, nil
}

// EditMessageMarkup edits an existing message text and updates or sets its reply markup.
func (s *Service) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}

	_, err := s.execIdempotentPeerVal(ctx, "messages.editMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.UpdatesClass, error) {
		req := &tg.MessagesEditMessageRequest{Peer: currentPeer, ID: msgID}
		msgText, ents := parseHTML(text)
		req.SetMessage(msgText)
		if len(ents) > 0 {
			req.SetEntities(ents)
		}
		req.SetNoWebpage(true)
		if markup != nil {
			req.SetReplyMarkup(markup)
		}
		return s.api.MessagesEditMessage(opCtx, req)
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	return err
}

// EditMessageMarkupOnly updates only the reply markup of an existing message, preserving its text.
func (s *Service) EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}

	_, err := s.execIdempotentPeerVal(ctx, "messages.editMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.UpdatesClass, error) {
		req := &tg.MessagesEditMessageRequest{Peer: currentPeer, ID: msgID}
		if markup != nil {
			req.SetReplyMarkup(markup)
		}
		return s.api.MessagesEditMessage(opCtx, req)
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	return err
}

// EditInlineBotMessage edits an inline-origin bot message via messages.editInlineBotMessage.
func (s *Service) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}
	if inlineID == nil {
		return fmt.Errorf("%w: inline message id is nil", core.ErrInternal)
	}
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: inlineID,
	}
	msgText, ents := parseHTML(text)
	req.SetMessage(msgText)
	if len(ents) > 0 {
		req.SetEntities(ents)
	}
	req.SetNoWebpage(true)
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := s.execIdempotentVal(ctx, "messages.editInlineBotMessage", func(opCtx context.Context) (bool, error) {
		return s.api.MessagesEditInlineBotMessage(opCtx, req)
	})
	return err
}

// EditInlineBotMessageMarkup updates only the reply markup of an inline message, preserving its text.
func (s *Service) EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}
	if inlineID == nil {
		return fmt.Errorf("%w: inline message id is nil", core.ErrInternal)
	}
	req := &tg.MessagesEditInlineBotMessageRequest{
		ID: inlineID,
	}
	if markup != nil {
		req.SetReplyMarkup(markup)
	}
	_, err := s.execIdempotentVal(ctx, "messages.editInlineBotMessage", func(opCtx context.Context) (bool, error) {
		return s.api.MessagesEditInlineBotMessage(opCtx, req)
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

	_, err := s.execIdempotentVal(ctx, "messages.setBotCallbackAnswer", func(opCtx context.Context) (bool, error) {
		return s.api.MessagesSetBotCallbackAnswer(opCtx, req)
	})
	return err
}

// AnswerInlineQuery answers an inline query with the prepared results.
func (s *Service) AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error {
	return s.AnswerInlineQueryOptions(ctx, queryID, results, core.InlineAnswerOptions{
		Results:    results,
		NextOffset: nextOffset,
		CacheTime:  cacheTime,
	})
}

// AnswerInlineQueryOptions answers an inline query with gallery/private/switch_pm support.
func (s *Service) AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts core.InlineAnswerOptions) error {
	if s.api == nil {
		return fmt.Errorf("%w: api client is not initialized", core.ErrInternal)
	}
	if results == nil {
		results = opts.Results
	}
	nextOffset := opts.NextOffset
	cacheTime := opts.CacheTime

	req := &tg.MessagesSetInlineBotResultsRequest{
		QueryID:    queryID,
		Results:    results,
		CacheTime:  cacheTime,
		NextOffset: nextOffset,
		Gallery:    opts.Gallery,
		Private:    opts.Private,
	}
	if opts.SwitchPM != nil {
		req.SwitchPm = *opts.SwitchPM
	}
	if opts.SwitchWebView != nil {
		req.SwitchWebview = *opts.SwitchWebView
	}
	// SetFlags computes flag bits for Gallery/Private/SwitchPM etc.
	req.SetFlags()

	_, err := s.execIdempotentVal(ctx, "messages.setInlineBotResults", func(opCtx context.Context) (bool, error) {
		return s.api.MessagesSetInlineBotResults(opCtx, req)
	})
	return err
}

// DeleteMessage deletes messages for everyone (revokes).
func (s *Service) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	if len(msgIDs) == 0 {
		return nil
	}

	if _, ok := peer.(*tg.InputPeerChannel); ok {
		err := s.execIdempotentPeer(ctx, "channels.deleteMessages", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
			ch := currentPeer.(*tg.InputPeerChannel)
			_, err := s.api.ChannelsDeleteMessages(opCtx, &tg.ChannelsDeleteMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				ID:      msgIDs,
			})
			return err
		})
		if err != nil {
			s.checkPeerError(ctx, err, peer)
		}
		return err
	}

	err := s.execIdempotentPeer(ctx, "messages.deleteMessages", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		_, err := s.sender.To(currentPeer).Revoke().Messages(opCtx, msgIDs...)
		return err
	})
	if err != nil {
		s.checkPeerError(ctx, err, peer)
	}
	return err
}

// React places an emoji reaction on the given message.
func (s *Service) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	if s.sender == nil {
		return fmt.Errorf("%w: sender is not initialized", core.ErrInternal)
	}

	err := s.execIdempotentPeer(ctx, "messages.sendReaction", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		_, err := s.sender.To(currentPeer).Reaction(opCtx, msgID, &tg.ReactionEmoji{Emoticon: emoji})
		return err
	})
	return err
}

// GetMessage fetches a message by its ID. Returns (nil, core.ErrNotFound) if message does not exist.
func (s *Service) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	var msgs []tg.MessageClass

	switch peer.(type) {
	case *tg.InputPeerChannel:
		res, err := s.execReadOnlyPeerVal(ctx, "channels.getMessages", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.MessagesMessagesClass, error) {
			p := currentPeer.(*tg.InputPeerChannel)
			return s.api.ChannelsGetMessages(opCtx, &tg.ChannelsGetMessagesRequest{
				Channel: &tg.InputChannel{ChannelID: p.ChannelID, AccessHash: p.AccessHash},
				ID:      []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}},
			})
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
		res, err := s.execReadOnlyVal(ctx, "messages.getMessages", func(opCtx context.Context) (tg.MessagesMessagesClass, error) {
			return s.api.MessagesGetMessages(opCtx, []tg.InputMessageClass{&tg.InputMessageID{ID: msgID}})
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
	err := s.execIdempotentPeer(ctx, "messages.updatePinnedMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		req := &tg.MessagesUpdatePinnedMessageRequest{Silent: silent, Unpin: false, Peer: currentPeer, ID: msgID}
		if silent {
			req.SetSilent(true)
		}
		_, err := s.api.MessagesUpdatePinnedMessage(opCtx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return nil
		}
		return err
	})
	return err
}

// UnpinMessage unpins a message in the chat.
func (s *Service) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	err := s.execIdempotentPeer(ctx, "messages.updatePinnedMessage", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		req := &tg.MessagesUpdatePinnedMessageRequest{Unpin: true, Peer: currentPeer, ID: msgID}
		req.SetUnpin(true)
		_, err := s.api.MessagesUpdatePinnedMessage(opCtx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return nil
		}
		return err
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

	err := s.execNonIdempotentPeers(ctx, "messages.forwardMessages", []tg.InputPeerClass{toPeer, fromPeer}, func(opCtx context.Context, current []tg.InputPeerClass) error {
		_, err := s.sender.To(current[0]).ForwardIDs(current[1], msgIDs[0], msgIDs[1:]...).Send(opCtx)
		return err
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
	if s.api == nil {
		return fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
	}
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
	transferCtx, cancel := core.WithDefaultTimeout(ctx, mediaTransferTimeout)
	defer cancel()

	managed := &managedDownloadRPCClient{
		raw:      s.api,
		executor: s.getExecutor,
	}
	_, err = s.downloader.Download(managed, location).Stream(transferCtx, bw)
	if err != nil {
		_ = os.Remove(dstPath)
		return mapTelegramError(unwrapMediaRPCBoundary(err))
	}
	return nil
}

// BanUser restricts a user from viewing and sending messages in a group/supergroup.
func (s *Service) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); ok {
		return s.execIdempotentPeers(ctx, "channels.editBanned", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: FullBanRights(untilDate),
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
				return nil
			}
			return err
		})
	}
	if _, ok := peer.(*tg.InputPeerChat); ok {
		if _, ok := user.(*tg.InputPeerUser); ok {
			return s.execIdempotentPeers(ctx, "messages.deleteChatUser", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
				chat := current[0].(*tg.InputPeerChat)
				u := current[1].(*tg.InputPeerUser)
				_, err := s.api.MessagesDeleteChatUser(opCtx, &tg.MessagesDeleteChatUserRequest{
					ChatID: chat.ChatID,
					UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
				})
				return err
			})
		}
	}
	return fmt.Errorf("%w: unsupported peer type for ban: %T", core.ErrUnsupported, peer)
}

// UnbanUser removes all ban restrictions on a user.
func (s *Service) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); ok {
		return s.execIdempotentPeers(ctx, "channels.editBanned", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: tg.ChatBannedRights{},
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED", "USER_NOT_PARTICIPANT") {
				return nil
			}
			return err
		})
	}
	return fmt.Errorf("%w: unban is only supported in supergroups and channels (got %T)", core.ErrUnsupported, peer)
}

// KickUser removes a user from the group while allowing them to rejoin.
func (s *Service) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); ok {
		currentPeers := []tg.InputPeerClass{peer, user}
		kickUntil := int(time.Now().Unix() + 60)
		if err := s.execIdempotentPeers(ctx, "channels.editBanned", currentPeers, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: tg.ChatBannedRights{ViewMessages: true, UntilDate: kickUntil},
			})
			return err
		}); err != nil {
			return err
		}
		return s.execIdempotentPeers(ctx, "channels.editBanned", currentPeers, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: tg.ChatBannedRights{},
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED") {
				return nil
			}
			return err
		})
	}
	if _, ok := peer.(*tg.InputPeerChat); ok {
		if _, ok := user.(*tg.InputPeerUser); ok {
			return s.execIdempotentPeers(ctx, "messages.deleteChatUser", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
				chat := current[0].(*tg.InputPeerChat)
				u := current[1].(*tg.InputPeerUser)
				_, err := s.api.MessagesDeleteChatUser(opCtx, &tg.MessagesDeleteChatUserRequest{ChatID: chat.ChatID, UserID: &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash}})
				return err
			})
		}
	}
	return fmt.Errorf("%w: unsupported peer type for kick: %T", core.ErrUnsupported, peer)
}

// MuteUser restricts a user from sending messages and media until untilDate.
func (s *Service) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); ok {
		return s.execIdempotentPeers(ctx, "channels.editBanned", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: FullMuteRights(untilDate),
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
				return nil
			}
			return err
		})
	}
	return fmt.Errorf("%w: mute is only supported in supergroups and channels (got %T)", core.ErrUnsupported, peer)
}

// UnmuteUser removes send message restrictions on a user.
func (s *Service) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); ok {
		return s.execIdempotentPeers(ctx, "channels.editBanned", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
			ch := current[0].(*tg.InputPeerChannel)
			_, err := s.api.ChannelsEditBanned(opCtx, &tg.ChannelsEditBannedRequest{
				Channel:      &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
				Participant:  current[1],
				BannedRights: tg.ChatBannedRights{},
			})
			if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED", "USER_NOT_PARTICIPANT") {
				return nil
			}
			return err
		})
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
			err := s.execReadOnlyPeer(ctx, "messages.getReplies", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
				resp, err := s.api.MessagesGetReplies(opCtx, &tg.MessagesGetRepliesRequest{
					Peer:     currentPeer,
					MsgID:    topicID,
					OffsetID: currentOffsetID,
					MinID:    minID - 1,
					MaxID:    maxID + 1,
					Limit:    100,
				})
				if err != nil {
					return err
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return nil
			})
			fetchErr = err
		} else {
			// Non-topic history fetch with pagination
			err := s.execReadOnlyPeer(ctx, "messages.getHistory", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
				resp, err := s.api.MessagesGetHistory(opCtx, &tg.MessagesGetHistoryRequest{
					Peer:     currentPeer,
					OffsetID: currentOffsetID,
					MinID:    minID - 1,
					MaxID:    maxID + 1,
					Limit:    100,
				})
				if err != nil {
					return err
				}
				if resp != nil {
					if mod, ok := resp.AsModified(); ok {
						messages = mod.GetMessages()
					}
				}
				return nil
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

	transferCtx, cancel := core.WithDefaultTimeout(ctx, mediaTransferTimeout)
	inputFile, err := s.uploader.FromPath(transferCtx, filePath)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("failed to upload file %q: %w", filePath, unwrapMediaRPCBoundary(err))
	}

	var styledCaption []message.StyledTextOption
	if caption != "" {
		styledCaption = append(styledCaption, html.String(nil, caption))
	}

	updates, err := s.execNonIdempotentPeerVal(ctx, "messages.sendMedia", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (tg.UpdatesClass, error) {
		builder := s.sender.To(currentPeer)
		switch mediaType {
		case "photo":
			return builder.UploadedPhoto(opCtx, inputFile, styledCaption...)
		case "sticker":
			return builder.UploadedSticker(opCtx, inputFile, styledCaption...)
		case "audio":
			return builder.Audio(opCtx, inputFile, styledCaption...)
		case "video":
			return builder.Video(opCtx, inputFile, styledCaption...)
		case "file", "document":
			fallthrough
		default:
			return builder.File(opCtx, inputFile, styledCaption...)
		}
	})

	if err != nil {
		s.checkPeerError(ctx, err, peer)
		return nil, fmt.Errorf("failed to send media (%s): %w", mediaType, err)
	}

	msg := extractMessageFromUpdates(updates)
	if msg != nil {
		s.recordBotSent(peer, msg.ID)
	}
	return msg, nil
}

// GetFullUser retrieves extended profile information for a user.
func (s *Service) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
	}
	var res *tg.UsersUserFull
	var err error
	if u, ok := user.(*tg.InputUser); ok {
		peer := &tg.InputPeerUser{UserID: u.UserID, AccessHash: u.AccessHash}
		res, err = s.execReadOnlyPeerVal(ctx, "users.getFullUser", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.UsersUserFull, error) {
			current := currentPeer.(*tg.InputPeerUser)
			return s.api.UsersGetFullUser(opCtx, &tg.InputUser{UserID: current.UserID, AccessHash: current.AccessHash})
		})
	} else {
		res, err = s.execReadOnlyVal(ctx, "users.getFullUser", func(opCtx context.Context) (*tg.UsersUserFull, error) {
			return s.api.UsersGetFullUser(opCtx, user)
		})
	}
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
	res, err := s.execReadOnlyVal(ctx, "contacts.resolveUsername", func(opCtx context.Context) (*tg.ContactsResolvedPeer, error) {
		return s.api.ContactsResolveUsername(opCtx, &tg.ContactsResolveUsernameRequest{Username: cleaned})
	})
	if err != nil {
		return nil, mapTelegramError(err)
	}
	if s.peerManager != nil && res != nil {
		_ = s.peerManager.Apply(ctx, res.Users, res.Chats)
	}
	return res, nil
}

// GetFullChat retrieves extended information for a group, supergroup, or channel.
func (s *Service) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: telegram api not initialized", core.ErrInternal)
	}

	var res *tg.MessagesChatFull
	var err error

	switch p := peer.(type) {
	case *tg.InputPeerChannel:
		res, err = s.execReadOnlyPeerVal(ctx, "channels.getFullChannel", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
			current := currentPeer.(*tg.InputPeerChannel)
			return s.api.ChannelsGetFullChannel(opCtx, &tg.InputChannel{ChannelID: current.ChannelID, AccessHash: current.AccessHash})
		})
	case *tg.InputPeerChat:
		res, err = s.execReadOnlyVal(ctx, "messages.getFullChat", func(opCtx context.Context) (*tg.MessagesChatFull, error) {
			return s.api.MessagesGetFullChat(opCtx, p.ChatID)
		})
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
	if _, ok := peer.(*tg.InputPeerChannel); !ok {
		return fmt.Errorf("%w: promote is only supported in supergroups/channels, got: %T", core.ErrUnsupported, peer)
	}
	if _, ok := user.(*tg.InputPeerUser); !ok {
		return fmt.Errorf("target user must be an InputPeerUser, got: %T", user)
	}
	return s.execIdempotentPeers(ctx, "channels.editAdmin", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
		ch := current[0].(*tg.InputPeerChannel)
		u := current[1].(*tg.InputPeerUser)
		req := &tg.ChannelsEditAdminRequest{
			Channel: &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			UserID:  &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
			AdminRights: tg.ChatAdminRights{ChangeInfo: true, PostMessages: true, EditMessages: true, DeleteMessages: true, BanUsers: true, InviteUsers: true, PinMessages: true, ManageTopics: true},
		}
		if title != "" {
			req.SetRank(title)
		}
		_, err := s.api.ChannelsEditAdmin(opCtx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return nil
		}
		return err
	})
}

// DemoteAdmin demotes an administrator to regular member in the supergroup/channel.
func (s *Service) DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}
	if _, ok := peer.(*tg.InputPeerChannel); !ok {
		return fmt.Errorf("%w: demote is only supported in supergroups/channels, got: %T", core.ErrUnsupported, peer)
	}
	if _, ok := user.(*tg.InputPeerUser); !ok {
		return fmt.Errorf("target user must be an InputPeerUser, got: %T", user)
	}
	return s.execIdempotentPeers(ctx, "channels.editAdmin", []tg.InputPeerClass{peer, user}, func(opCtx context.Context, current []tg.InputPeerClass) error {
		ch := current[0].(*tg.InputPeerChannel)
		u := current[1].(*tg.InputPeerUser)
		_, err := s.api.ChannelsEditAdmin(opCtx, &tg.ChannelsEditAdminRequest{
			Channel:     &tg.InputChannel{ChannelID: ch.ChannelID, AccessHash: ch.AccessHash},
			UserID:      &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash},
			AdminRights: tg.ChatAdminRights{},
		})
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED") {
			return nil
		}
		return err
	})
}

// EditChatDefaultBannedRights updates the default chat permissions (locks) in a group/supergroup.
func (s *Service) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	if s.api == nil {
		return errors.New("api is not initialized")
	}

	if _, ok := peer.(*tg.InputPeerChannel); !ok {
		return fmt.Errorf("%w: permissions lock is only supported in supergroups, got %T", core.ErrUnsupported, peer)
	}

	err := s.execIdempotentPeer(ctx, "messages.editChatDefaultBannedRights", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) error {
		req := &tg.MessagesEditChatDefaultBannedRightsRequest{Peer: currentPeer, BannedRights: rights}
		_, err := s.api.MessagesEditChatDefaultBannedRights(opCtx, req)
		if err != nil && tgerr.Is(err, "CHAT_NOT_MODIFIED", "RIGHTS_NOT_MODIFIED") {
			// Rights are already in the requested state; treat as idempotent success.
			return nil
		}
		return err
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

	_, err := s.execIdempotentVal(ctx, "account.updateProfile", func(opCtx context.Context) (tg.UserClass, error) {
		return s.api.AccountUpdateProfile(opCtx, req)
	})
	return err
}

// BlockUser blocks the specified peer from sending messages or calling.
func (s *Service) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	_, err := s.execIdempotentPeerVal(ctx, "contacts.block", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (bool, error) {
		return s.api.ContactsBlock(opCtx, &tg.ContactsBlockRequest{ID: currentPeer})
	})
	return err
}

// UnblockUser removes the specified peer from the blocklist.
func (s *Service) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if s.api == nil {
		return fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	_, err := s.execIdempotentPeerVal(ctx, "contacts.unblock", peer, func(opCtx context.Context, currentPeer tg.InputPeerClass) (bool, error) {
		return s.api.ContactsUnblock(opCtx, &tg.ContactsUnblockRequest{ID: currentPeer})
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

	transferCtx, cancel := core.WithDefaultTimeout(ctx, mediaTransferTimeout)
	inputFile, err := s.uploader.FromPath(transferCtx, filePath)
	cancel()
	if err != nil {
		return fmt.Errorf("failed to upload photo file: %w", unwrapMediaRPCBoundary(err))
	}

	_, err = s.execNonIdempotentVal(ctx, "photos.uploadProfilePhoto", func(opCtx context.Context) (*tg.PhotosPhoto, error) {
		return s.api.PhotosUploadProfilePhoto(opCtx, &tg.PhotosUploadProfilePhotoRequest{
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

	photosRes, err := s.execReadOnlyVal(ctx, "photos.getUserPhotos", func(opCtx context.Context) (tg.PhotosPhotosClass, error) {
		return s.api.PhotosGetUserPhotos(opCtx, &tg.PhotosGetUserPhotosRequest{
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

	deletedIDs, err := s.execIdempotentVal(ctx, "photos.deletePhotos", func(opCtx context.Context) ([]int64, error) {
		return s.api.PhotosDeletePhotos(opCtx, inputPhotos)
	})
	if err != nil {
		return 0, err
	}
	return len(deletedIDs), nil
}

// GetDialogs returns recent active dialogs/chats up to limit (capped at 100 per
// Telegram's messages.getDialogs max; larger requests require pagination).
func (s *Service) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	if s.api == nil {
		return nil, fmt.Errorf("%w: api is not initialized", core.ErrInternal)
	}
	if limit <= 0 {
		limit = 10
	}
	if limit > 100 {
		limit = 100
	}

	res, err := s.execReadOnlyVal(ctx, "messages.getDialogs", func(opCtx context.Context) (tg.MessagesDialogsClass, error) {
		return s.api.MessagesGetDialogs(opCtx, &tg.MessagesGetDialogsRequest{
			OffsetPeer: &tg.InputPeerEmpty{},
			Limit:      limit,
		})
	})
	if err != nil {
		return nil, err
	}

	var chats []tg.ChatClass
	var users []tg.UserClass
	switch d := res.(type) {
	case *tg.MessagesDialogs:
		chats = d.Chats
		users = d.Users
	case *tg.MessagesDialogsSlice:
		chats = d.Chats
		users = d.Users
	}

	var result []*core.Chat
	for _, c := range chats {
		switch ch := c.(type) {
		case *tg.Channel:
			if s.storage != nil {
				_ = s.storage.Save(ctx, peers.Key{Prefix: "channel", ID: ch.ID}, peers.Value{AccessHash: ch.AccessHash})
			}
			chatType := "channel"
			if ch.Megagroup {
				chatType = "supergroup"
			}
			result = append(result, &core.Chat{
				ID:         ch.ID,
				Title:      ch.Title,
				Username:   ch.Username,
				Type:       chatType,
				AccessHash: ch.AccessHash,
			})
		case *tg.Chat:
			result = append(result, &core.Chat{
				ID:    ch.ID,
				Title: ch.Title,
				Type:  "group",
			})
		}
	}
	for _, u := range users {
		if usr, ok := u.(*tg.User); ok && usr != nil {
			if usr.Self {
				continue
			}
			if s.storage != nil {
				_ = s.storage.Save(ctx, peers.Key{Prefix: "user", ID: usr.ID}, peers.Value{AccessHash: usr.AccessHash})
			}
			name := strings.TrimSpace(usr.FirstName + " " + usr.LastName)
			if name == "" {
				name = usr.Username
			}
			if name == "" {
				name = fmt.Sprintf("User %d", usr.ID)
			}
			result = append(result, &core.Chat{
				ID:         usr.ID,
				Title:      name,
				Username:   usr.Username,
				Type:       "private",
				AccessHash: usr.AccessHash,
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

	res, err := s.execReadOnlyVal(ctx, "contacts.getContacts", func(opCtx context.Context) (tg.ContactsContactsClass, error) {
		return s.api.ContactsGetContacts(opCtx, 0)
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
