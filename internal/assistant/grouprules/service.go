package grouprules

import (
	"context"
	"fmt"
	"sync"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

// Source is implemented by the canonical blacklist/filter plugins. The
// Assistant group plane reuses those exact plugin instances, including their
// chat-scoped compiled caches and mutation invalidation, instead of maintaining
// a second rules registry.
type Source interface {
	AssistantRuleInterested(chatID int64) bool
	AssistantRuleRevision(chatID int64) uint64
	MatchAssistantRule(context.Context, *core.MessageEnvelope) (bool, error)
	ApplyAssistantRule(context.Context, core.TelegramServicer, *core.MessageEnvelope) (bool, error)
}

type Revision struct {
	Blacklist uint64
	Filters   uint64
}

func (r Revision) Equal(other Revision) bool {
	return r.Blacklist == other.Blacklist && r.Filters == other.Filters
}

type Result struct {
	Matched  bool
	Handled  bool
	Bypassed bool
	Domain   string
	Revision Revision
}

type Service struct {
	blacklist Source
	filters   Source

	mu         sync.RWMutex
	roles      core.GroupRoleResolver
	privileged func(int64) bool
	enabled    func(string) bool
	svc        core.TelegramServicer
}

func New(blacklist, filters Source) *Service {
	return &Service{blacklist: blacklist, filters: filters}
}

func (s *Service) SetRoleResolver(resolver core.GroupRoleResolver) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.roles = resolver
	s.mu.Unlock()
}

func (s *Service) SetPrivilegedChecker(check func(int64) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.privileged = check
	s.mu.Unlock()
}

func (s *Service) SetEnabledChecker(check func(string) bool) {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.enabled = check
	s.mu.Unlock()
}

func (s *Service) SetTransport(transport interaction.MessageInteraction) {
	if s == nil {
		return
	}
	s.mu.Lock()
	if transport == nil {
		s.svc = nil
	} else {
		s.svc = &interactionServicer{inter: transport}
	}
	s.mu.Unlock()
}

func (s *Service) Interested(chatID int64) bool {
	if s == nil || chatID <= 0 {
		return false
	}
	s.mu.RLock()
	enabled := s.enabled
	s.mu.RUnlock()
	blacklistEnabled := enabled == nil || enabled("blacklist")
	filtersEnabled := enabled == nil || enabled("filters")
	return (blacklistEnabled && s.blacklist != nil && s.blacklist.AssistantRuleInterested(chatID)) ||
		(filtersEnabled && s.filters != nil && s.filters.AssistantRuleInterested(chatID))
}

func (s *Service) Revision(chatID int64) Revision {
	if s == nil || chatID <= 0 {
		return Revision{}
	}
	var revision Revision
	if s.blacklist != nil {
		revision.Blacklist = s.blacklist.AssistantRuleRevision(chatID)
	}
	if s.filters != nil {
		revision.Filters = s.filters.AssistantRuleRevision(chatID)
	}
	return revision
}

func (s *Service) dependencies() (
	core.GroupRoleResolver,
	func(int64) bool,
	func(string) bool,
	core.TelegramServicer,
) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.roles, s.privileged, s.enabled, s.svc
}

func (s *Service) matched(
	ctx context.Context,
	message *core.MessageEnvelope,
	enabled func(string) bool,
) (blacklist, filters bool, err error) {
	blacklistEnabled := enabled == nil || enabled("blacklist")
	filtersEnabled := enabled == nil || enabled("filters")
	if blacklistEnabled && s.blacklist != nil && s.blacklist.AssistantRuleInterested(message.ChatID) {
		blacklist, err = s.blacklist.MatchAssistantRule(ctx, message)
		if err != nil {
			return false, false, err
		}
	}
	if filtersEnabled && s.filters != nil && s.filters.AssistantRuleInterested(message.ChatID) {
		filters, err = s.filters.MatchAssistantRule(ctx, message)
		if err != nil {
			return false, false, err
		}
	}
	return blacklist, filters, nil
}

func (s *Service) bypassed(
	ctx context.Context,
	message *core.MessageEnvelope,
	roles core.GroupRoleResolver,
	privileged func(int64) bool,
) bool {
	if privileged != nil && privileged(message.Sender.ID) {
		return true
	}
	if roles == nil {
		// Moderation automation must fail safe when contextual role verification
		// is unavailable. A missing resolver must never cause an administrator
		// message to be deleted or trigger automation.
		return true
	}
	peer, err := message.Peer.InputPeer()
	if err != nil {
		return true
	}
	snapshot, err := roles.ResolveGroupRoleFresh(ctx, core.GroupRoleRequest{
		ChatID: message.ChatID,
		Kind:   message.Chat.Kind(),
		Peer:   peer,
		UserID: message.Sender.ID,
	})
	if err != nil || !snapshot.Principal.Verified ||
		snapshot.Principal.UserID != message.Sender.ID {
		return true
	}
	switch snapshot.Principal.Role {
	case core.GroupActorRoleAdministrator, core.GroupActorRoleCreator:
		return true
	default:
		return false
	}
}

// Evaluate applies Assistant group rules to one already-admitted ordinary group
// message. No TaskEngine is owned here: ingress is responsible for admission.
//
// The expensive Telegram role lookup happens only after at least one compiled
// per-chat rule matches. Role verification is fresh and fail-open for
// moderation safety: inability to prove that a sender is not an administrator
// suppresses automatic actions.
func (s *Service) Handle(ctx context.Context, message *core.MessageEnvelope) error {
	_, err := s.Evaluate(ctx, message)
	return err
}

func (s *Service) Evaluate(
	ctx context.Context,
	message *core.MessageEnvelope,
) (Result, error) {
	if s == nil || message == nil || message.ChatID <= 0 ||
		!message.IsGroup() || message.Outgoing || message.IsCommand ||
		message.Text == "" || message.Sender.ID <= 0 || message.Sender.IsBot {
		return Result{}, nil
	}
	if !s.Interested(message.ChatID) {
		return Result{}, nil
	}

	roles, privileged, enabled, svc := s.dependencies()
	if privileged != nil && privileged(message.Sender.ID) {
		return Result{Bypassed: true}, nil
	}

	revision := s.Revision(message.ChatID)
	blacklistMatch, filterMatch, err := s.matched(ctx, message, enabled)
	if err != nil {
		return Result{}, err
	}
	if !blacklistMatch && !filterMatch {
		return Result{}, nil
	}
	result := Result{Matched: true, Revision: revision}
	if s.bypassed(ctx, message, roles, privileged) {
		result.Bypassed = true
		return result, nil
	}
	if latest := s.Revision(message.ChatID); !latest.Equal(revision) {
		// A manager changed rules while the role verification RPC was in
		// flight. Apply paths re-match current compiled state; surface the new
		// generation in the result so tests/metrics can prove invalidation.
		result.Revision = latest
	}
	if svc == nil {
		return result, fmt.Errorf("%w: Assistant group-rule transport unavailable", core.ErrUnavailable)
	}

	// Blacklist is a decision/interception rule and therefore outranks the
	// automated filter response. Re-evaluate at apply time so a rule mutation
	// that raced the initial match never executes stale compiled state.
	if (enabled == nil || enabled("blacklist")) &&
		s.blacklist != nil && s.blacklist.AssistantRuleInterested(message.ChatID) {
		handled, applyErr := s.blacklist.ApplyAssistantRule(ctx, svc, message)
		if applyErr != nil {
			return result, applyErr
		}
		if handled {
			result.Handled = true
			result.Domain = "blacklist"
			return result, nil
		}
	}
	if (enabled == nil || enabled("filters")) &&
		s.filters != nil && s.filters.AssistantRuleInterested(message.ChatID) {
		handled, applyErr := s.filters.ApplyAssistantRule(ctx, svc, message)
		if applyErr != nil {
			return result, applyErr
		}
		if handled {
			result.Handled = true
			result.Domain = "filter"
		}
	}
	return result, nil
}

type interactionServicer struct {
	core.MockTelegramServicer
	inter interaction.MessageInteraction
}

func (s *interactionServicer) SendMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
) (*tg.Message, error) {
	if s == nil || s.inter == nil {
		return nil, fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	return s.inter.SendMessage(ctx, peer, text, nil)
}

func (s *interactionServicer) SendMessageWithMarkup(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
	markup tg.ReplyMarkupClass,
) (*tg.Message, error) {
	if s == nil || s.inter == nil {
		return nil, fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	return s.inter.SendMessage(ctx, peer, text, markup)
}

func (s *interactionServicer) SendMedia(
	ctx context.Context,
	peer tg.InputPeerClass,
	mediaType string,
	filePath string,
	caption string,
) (*tg.Message, error) {
	if s == nil || s.inter == nil {
		return nil, fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	return s.inter.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (s *interactionServicer) SendMessageContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
	markup tg.ReplyMarkupClass,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.inter == nil {
		return nil, fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	if contextual, ok := s.inter.(interface {
		SendMessageContext(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass, core.MessageSendContext) (*tg.Message, error)
	}); ok {
		return contextual.SendMessageContext(ctx, peer, text, markup, send)
	}
	return s.inter.SendMessage(ctx, peer, text, markup)
}

func (s *interactionServicer) SendMediaContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	mediaType string,
	filePath string,
	caption string,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.inter == nil {
		return nil, fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	if contextual, ok := s.inter.(interface {
		SendMediaContext(context.Context, tg.InputPeerClass, string, string, string, core.MessageSendContext) (*tg.Message, error)
	}); ok {
		return contextual.SendMediaContext(ctx, peer, mediaType, filePath, caption, send)
	}
	return s.inter.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (s *interactionServicer) DeleteMessage(
	ctx context.Context,
	peer tg.InputPeerClass,
	msgIDs []int,
) error {
	if s == nil || s.inter == nil {
		return fmt.Errorf("%w: Assistant interaction transport unavailable", core.ErrUnavailable)
	}
	chatID := inputPeerChatID(peer)
	if chatID <= 0 {
		return core.ErrInvalidArgs
	}
	for _, msgID := range msgIDs {
		if msgID <= 0 {
			continue
		}
		if err := s.inter.Delete(ctx, interaction.NewMessageTarget(peer, msgID, chatID, 0)); err != nil {
			return err
		}
	}
	return nil
}

func inputPeerChatID(peer tg.InputPeerClass) int64 {
	switch value := peer.(type) {
	case *tg.InputPeerChat:
		return value.ChatID
	case *tg.InputPeerChannel:
		return value.ChannelID
	case *tg.InputPeerUser:
		return value.UserID
	default:
		return 0
	}
}
