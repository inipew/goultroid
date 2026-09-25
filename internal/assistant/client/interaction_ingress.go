package client

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	assistantshell "github.com/inipew/goultroid/internal/assistant/shell"
	"github.com/inipew/goultroid/internal/core"
	rootinteraction "github.com/inipew/goultroid/internal/interaction"
	"github.com/inipew/goultroid/internal/interaction/orchestration"
	"github.com/inipew/goultroid/internal/presentation"

	presentationtelegram "github.com/inipew/goultroid/internal/presentation/telegram"
	"github.com/inipew/goultroid/internal/tasks"
	"github.com/inipew/goultroid/internal/ui"
)

var ErrInteractionUnavailable = errors.New("assistant/client: interaction engine unavailable")

const maxInteractionCallbackFlights = 4096

type callbackAcknowledger interface {
	ensureAnswered(context.Context, int64, error)
}

type immediateCallbackAcknowledger interface {
	acknowledge(context.Context, int64)
}

type interactionIngress struct {
	engine  *orchestration.Engine
	ack     callbackAcknowledger
	tasks   tasks.Client
	limiter RateLimiter
	input   func(*orchestration.Context, string) error

	flightMu sync.Mutex
	flights  map[string]struct{}
}

func isInteractionCallback(data []byte) bool {
	return strings.HasPrefix(string(data), "a2:")
}

func (v *interactionIngress) acquireCallbackFlight(key string) bool {
	if v == nil || key == "" {
		return true
	}
	v.flightMu.Lock()
	defer v.flightMu.Unlock()
	if v.flights == nil {
		v.flights = make(map[string]struct{})
	}
	if _, exists := v.flights[key]; exists {
		return false
	}
	if len(v.flights) >= maxInteractionCallbackFlights {
		return false
	}
	v.flights[key] = struct{}{}
	return true
}

func (v *interactionIngress) releaseCallbackFlight(key string) {
	if v == nil || key == "" {
		return
	}
	v.flightMu.Lock()
	delete(v.flights, key)
	v.flightMu.Unlock()
}

func (v *interactionIngress) tryMessage(ctx context.Context, data []byte, userID, queryID int64, peer tg.InputPeerClass, chatID int64, msgID int) (bool, error) {
	if !isInteractionCallback(data) {
		return false, nil
	}
	if v == nil || v.engine == nil || v.ack == nil {
		return true, ErrInteractionUnavailable
	}
	target := presentationtelegram.MessageTarget{Peer: peer, ChatID: chatID, MessageID: msgID}
	err := v.dispatchCallback(ctx, orchestration.CallbackRequest{
		Data: data, ActorID: userID, QueryID: queryID, Target: target,
	}, tasks.TaskID(fmt.Sprintf("asst:cb:a2:%d", queryID)), fmt.Sprintf("callback:msg:%d:%d", chatID, msgID))
	return true, err
}

func (v *interactionIngress) dispatchCallback(
	ctx context.Context,
	request orchestration.CallbackRequest,
	taskID tasks.TaskID,
	orderingKey string,
) error {
	if v == nil || v.engine == nil || v.ack == nil {
		return ErrInteractionUnavailable
	}
	flightKey := fmt.Sprintf("%s:actor:%d", orderingKey, request.ActorID)
	if !v.acquireCallbackFlight(flightKey) {
		v.ack.ensureAnswered(ctx, request.QueryID, nil)
		return nil
	}
	defer v.releaseCallbackFlight(flightKey)

	if v.limiter != nil && !v.limiter.Allow(request.ActorID, "interaction") {
		v.ack.ensureAnswered(ctx, request.QueryID, nil)
		return nil
	}

	prepared, err := v.engine.PrepareCallback(ctx, request)
	if err != nil {
		v.ack.ensureAnswered(ctx, request.QueryID, err)
		return err
	}
	if v.tasks == nil {
		err = ErrInteractionUnavailable
		v.ack.ensureAnswered(ctx, request.QueryID, err)
		return err
	}

	profile := tasks.ExecutionProfile{
		Pool:             tasks.PoolID("interactive"),
		Class:            tasks.PriorityInteractive,
		ExecutionTimeout: 15 * time.Second,
	}
	if aware, ok := prepared.(orchestration.ExecutionProfilePreparedCallback); ok {
		profile = aware.ExecutionProfile().WithDefaults(profile)
	} else if aware, ok := prepared.(orchestration.ResourcePreparedCallback); ok {
		profile.Resources = aware.Resources()
	}
	var queueDeadline time.Time
	if profile.QueueTimeout > 0 {
		queueDeadline = time.Now().Add(profile.QueueTimeout)
	}

	if aware, ok := prepared.(orchestration.AckPreparedCallback); ok &&
		aware.AckPolicy() == rootinteraction.AckImmediate {
		if immediate, ok := v.ack.(immediateCallbackAcknowledger); ok {
			immediate.acknowledge(ctx, request.QueryID)
		}
	}

	doneCh := make(chan error, 1)
	ticket, submitErr := v.tasks.Submit(ctx, tasks.WorkSpec{
		ID:               taskID,
		Scope:            prepared.Scope(),
		QuotaOwner:       tasks.OwnerID(fmt.Sprintf("telegram:user:%d", request.ActorID)),
		Pool:             profile.Pool,
		Class:            profile.Class,
		OrderingKey:      orderingKey,
		QueueDeadline:    queueDeadline,
		ExecutionTimeout: profile.ExecutionTimeout,
		Resources:        append([]tasks.ResourceRequirement(nil), profile.Resources...),
		Handler: func(taskCtx context.Context) error {
			dispatchErr := prepared.Dispatch(taskCtx)
			doneCh <- dispatchErr
			return dispatchErr
		},
	})
	if submitErr != nil {
		err = fmt.Errorf("interaction task submission failed: %w", submitErr)
		// If the optimistic immediate ACK failed, ensureAnswered retries it.
		// If it succeeded, ensureAnswered is a no-op apart from bookkeeping cleanup.
		v.ack.ensureAnswered(ctx, request.QueryID, err)
		return err
	}

	var ticketDone <-chan struct{}
	if ticket != nil {
		ticketDone = ticket.Done()
	}
	select {
	case err = <-doneCh:
	case <-ticketDone:
		select {
		case err = <-doneCh:
		default:
			if ticket != nil {
				if result, ok := ticket.Result(); ok && !result.IsSuccess() {
					if result.Failure.Message != "" {
						err = errors.New(result.Failure.Message)
					} else {
						err = fmt.Errorf("interaction task finished with outcome %s (%s)", result.Outcome, result.Cause)
					}
				}
			}
		}
	case <-ctx.Done():
		if ticket != nil {
			_, _ = v.tasks.Cancel(ticket.TaskID(), tasks.CauseTimeout)
		}
		err = ctx.Err()
	}
	v.ack.ensureAnswered(ctx, request.QueryID, err)
	return err
}

func (v *interactionIngress) tryText(ctx context.Context, text string, userID, chatID int64, peer tg.InputPeerClass) (bool, error) {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "/") && !strings.EqualFold(trimmed, "/cancel") {
		return false, nil
	}
	if v == nil || v.engine == nil {
		return false, nil
	}
	inputCtx, handled, err := v.engine.TakeInput(ctx, userID, chatID)
	if err != nil || !handled {
		return handled, err
	}
	if v.input == nil || peer == nil {
		return true, ErrInteractionUnavailable
	}
	session := inputCtx.Session()
	if session.Binding.MessageID <= 0 || session.Binding.InlineMessageID != "" {
		return true, orchestration.ErrInvalidTarget
	}
	if err := inputCtx.SetTarget(presentationtelegram.MessageTarget{
		Peer:      peer,
		ChatID:    chatID,
		MessageID: session.Binding.MessageID,
	}); err != nil {
		return true, err
	}
	return true, v.input(inputCtx, text)
}

func interactionTextInputErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	var mutationErr *assistantshell.MutationError
	if errors.As(err, &mutationErr) {
		if mutationErr.Committed {
			return "✅ Setting was saved, but the Settings view could not refresh. Reopen Settings."
		}
		if mutationErr.Stage == assistantshell.MutationStageBinding {
			return "⚠️ The setting changed while input was pending. Reopen Settings."
		}
	}
	return ui.PresentUserError(err).Text
}

func (v *interactionIngress) tryInline(ctx context.Context, data []byte, userID, queryID int64, messageID tg.InputBotInlineMessageIDClass) (bool, error) {
	if !isInteractionCallback(data) {
		return false, nil
	}
	if v == nil || v.engine == nil || v.ack == nil {
		return true, ErrInteractionUnavailable
	}
	target := presentationtelegram.InlineTarget{
		MessageID: messageID,
		BindingID: inlineBindingID(messageID),
	}
	orderingKey := fmt.Sprintf("callback:inline:%s", target.BindingID)
	switch id := messageID.(type) {
	case *tg.InputBotInlineMessageID:
		orderingKey = fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
	case *tg.InputBotInlineMessageID64:
		orderingKey = fmt.Sprintf("callback:inline_msg:%d:%d", id.DCID, id.ID)
	}
	err := v.dispatchCallback(ctx, orchestration.CallbackRequest{
		Data: data, ActorID: userID, QueryID: queryID, Target: target,
	}, tasks.TaskID(fmt.Sprintf("asst:cb:a2:inline:%d", queryID)), orderingKey)
	return true, err
}

func inlineBindingID(messageID tg.InputBotInlineMessageIDClass) string {
	if messageID == nil {
		return ""
	}
	return fmt.Sprintf("%T:%v", messageID, messageID)
}

type interactionPresentationServicer struct {
	unsupportedTelegramServicer
	interaction *assistantinteraction.ClientInteraction

	mu       sync.Mutex
	answered map[int64]struct{}
}

func newInteractionPresentationServicer(interaction *assistantinteraction.ClientInteraction) *interactionPresentationServicer {
	return &interactionPresentationServicer{
		interaction: interaction,
		answered:    make(map[int64]struct{}),
	}
}

func (s *interactionPresentationServicer) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, ErrInteractionUnavailable
	}
	return s.interaction.SendMessage(ctx, peer, text, markup)
}

func (s *interactionPresentationServicer) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, ErrInteractionUnavailable
	}
	return s.interaction.SendMedia(ctx, peer, mediaType, filePath, caption)
}

func (s *interactionPresentationServicer) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return ErrInteractionUnavailable
	}
	target := assistantinteraction.NewMessageTarget(peer, msgID, extractChatIDFromInputPeer(peer), 0)
	return s.interaction.Edit(ctx, target, text, markup)
}

func (s *interactionPresentationServicer) SendMessageContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	text string,
	markup tg.ReplyMarkupClass,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, ErrInteractionUnavailable
	}
	return s.interaction.SendMessageContext(ctx, peer, text, markup, send)
}

func (s *interactionPresentationServicer) SendMediaContext(
	ctx context.Context,
	peer tg.InputPeerClass,
	mediaType string,
	filePath string,
	caption string,
	send core.MessageSendContext,
) (*tg.Message, error) {
	if s == nil || s.interaction == nil {
		return nil, ErrInteractionUnavailable
	}
	return s.interaction.SendMediaContext(ctx, peer, mediaType, filePath, caption, send)
}

func (s *interactionPresentationServicer) EditInlineBotMedia(
	ctx context.Context,
	inlineID tg.InputBotInlineMessageIDClass,
	media presentation.Media,
) error {
	if s == nil || s.interaction == nil || inlineID == nil {
		return ErrInteractionUnavailable
	}
	target := assistantinteraction.NewInlineTarget(1, inlineID, 0)
	return s.interaction.AsInline().EditMedia(ctx, target, media)
}

func (s *interactionPresentationServicer) EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error {
	if s == nil || s.interaction == nil {
		return ErrInteractionUnavailable
	}
	target := assistantinteraction.NewInlineTarget(1, inlineID, 0)
	return s.interaction.AsInline().Edit(ctx, target, text, markup)
}

func (s *interactionPresentationServicer) AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error {
	if s == nil || s.interaction == nil || queryID == 0 {
		return ErrInteractionUnavailable
	}
	s.mu.Lock()
	if _, answered := s.answered[queryID]; answered {
		s.mu.Unlock()
		return nil
	}
	s.answered[queryID] = struct{}{}
	s.mu.Unlock()
	if err := s.interaction.Answer(ctx, queryID, text, alert); err != nil {
		s.mu.Lock()
		delete(s.answered, queryID)
		s.mu.Unlock()
		return err
	}
	return nil
}

func (s *interactionPresentationServicer) acknowledge(ctx context.Context, queryID int64) {
	if s == nil || queryID == 0 {
		return
	}
	_ = s.AnswerCallbackQuery(ctx, queryID, "", false)
}

func (s *interactionPresentationServicer) ensureAnswered(ctx context.Context, queryID int64, dispatchErr error) {
	if s == nil || s.interaction == nil || queryID == 0 {
		return
	}
	s.mu.Lock()
	_, answered := s.answered[queryID]
	delete(s.answered, queryID)
	s.mu.Unlock()
	if answered {
		return
	}
	presented := ui.PresentUserError(dispatchErr)
	_ = s.interaction.Answer(ctx, queryID, presented.Text, presented.Alert)
}
