package callback

import (
	"context"
	"fmt"
	"sync"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// Router routes incoming callback query events to registered namespace handlers.
type Router struct {
	handlers   map[string]Handler
	stateStore *StateStore
	logger     *zap.Logger
	mu         sync.RWMutex
}

// NewRouter creates a new callback Router.
func NewRouter(logger *zap.Logger, stateStore *StateStore) *Router {
	if logger == nil {
		logger = zap.NewNop()
	}
	if stateStore == nil {
		stateStore = NewStateStore()
	}
	return &Router{
		handlers:   make(map[string]Handler),
		stateStore: stateStore,
		logger:     logger,
	}
}

// StateStore returns the underlying temporary state store.
func (r *Router) StateStore() *StateStore {
	return r.stateStore
}

// Register registers a new callback handler for its designated namespace.
func (r *Router) Register(h Handler) error {
	if h == nil {
		return fmt.Errorf("handler cannot be nil")
	}
	ns := h.Namespace()
	if ns == "" {
		return fmt.Errorf("handler namespace cannot be empty")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[ns]; exists {
		return fmt.Errorf("callback handler for namespace %q is already registered", ns)
	}

	r.handlers[ns] = h
	return nil
}

// GetHandler retrieves the registered handler for a namespace.
func (r *Router) GetHandler(namespace string) (Handler, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	h, ok := r.handlers[namespace]
	return h, ok
}

// Dispatch processes an incoming CallbackQueryEvent from the domain event bus.
func (r *Router) Dispatch(ctx context.Context, evt *core.CallbackQueryEvent, svc core.TelegramServicer) error {
	if evt == nil {
		return nil
	}

	ns, action, opaqueID, err := ParseCallbackData(evt.Data)
	if err != nil {
		r.logger.Debug("unrecognized callback data format", zap.ByteString("data", evt.Data))
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Invalid button action", false)
		}
		return ErrInvalidCallbackData
	}

	// No-op buttons (e.g. page counter indicators)
	if action == "noop" || opaqueID == "noop" {
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		}
		return nil
	}

	var storedState any
	if opaqueID != "" && r.stateStore != nil {
		data, allowedUser, ok := r.stateStore.Get(opaqueID)
		if ok {
			storedState = data
			if allowedUser > 0 && evt.UserID != allowedUser {
				if svc != nil {
					_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "⚠️ You are not authorized to use this button.", true)
				}
				return ErrUnauthorized
			}
		}
	}

	handler, exists := r.GetHandler(ns)
	if !exists {
		r.logger.Warn("no callback handler for namespace", zap.String("namespace", ns))
		if svc != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Feature not available", false)
		}
		return ErrHandlerNotFound
	}

	cbCtx := &CallbackContext{
		Ctx:       ctx,
		QueryID:   evt.QueryID,
		UserID:    evt.UserID,
		ChatID:    evt.ChatID,
		MsgID:     evt.MsgID,
		RawData:   evt.Data,
		Namespace: ns,
		Action:    action,
		OpaqueID:  opaqueID,
		State:     storedState,
		Service:   svc,
	}

	handleErr := handler.HandleCallback(cbCtx)

	// Ensure callback query is answered so loading spinner disappears
	if !cbCtx.IsAnswered() && svc != nil {
		if handleErr != nil {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "Action failed", false)
		} else {
			_ = svc.AnswerCallbackQuery(ctx, evt.QueryID, "", false)
		}
	}

	return handleErr
}
