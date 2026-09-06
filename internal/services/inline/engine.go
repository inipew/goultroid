package inline

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

// Registry stores and resolves inline query handlers.
type Registry struct {
	handlers map[string]InlineHandler
	order    []string
	mu       sync.RWMutex
}

// NewRegistry creates a new Registry.
func NewRegistry() *Registry {
	return &Registry{
		handlers: make(map[string]InlineHandler),
	}
}

// Register registers an InlineHandler for its pattern or keyword.
func (r *Registry) Register(h InlineHandler) error {
	if h == nil {
		return fmt.Errorf("inline handler cannot be nil")
	}
	pattern := strings.ToLower(strings.TrimSpace(h.Pattern()))

	r.mu.Lock()
	defer r.mu.Unlock()

	if _, exists := r.handlers[pattern]; exists {
		return fmt.Errorf("inline handler for pattern %q is already registered", pattern)
	}

	r.handlers[pattern] = h
	r.order = append(r.order, pattern)
	return nil
}

// Resolve looks up the appropriate InlineHandler and splits query arguments.
func (r *Registry) Resolve(query string) (InlineHandler, []string, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	trimmed := strings.TrimSpace(query)
	fields := strings.Fields(trimmed)

	if len(fields) == 0 {
		// Fallback to default handler if registered
		if h, ok := r.handlers[""]; ok {
			return h, nil, true
		}
		return nil, nil, false
	}

	// Exact keyword match on first word
	kw := strings.ToLower(fields[0])
	if h, ok := r.handlers[kw]; ok {
		return h, fields[1:], true
	}

	// Fallback to catch-all handler if registered
	if h, ok := r.handlers[""]; ok {
		return h, fields, true
	}

	return nil, nil, false
}

// Engine coordinates inline query execution, caching, pagination, and MTProto serialization.
type Engine struct {
	registry  *Registry
	cache     *Cache
	paginator *Paginator
	logger    *zap.Logger
	cacheTime int
}

// NewEngine creates an initialized Engine.
func NewEngine(registry *Registry, logger *zap.Logger) *Engine {
	if registry == nil {
		registry = NewRegistry()
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Engine{
		registry:  registry,
		cache:     NewCache(30 * time.Second),
		paginator: NewPaginator(DefaultPageSize),
		logger:    logger,
		cacheTime: 5, // 5 seconds Telegram cache
	}
}

// Registry returns the handler registry.
func (e *Engine) Registry() *Registry {
	return e.registry
}

// Cache returns the internal inline result cache.
func (e *Engine) Cache() *Cache {
	return e.cache
}

// Execute processes an incoming inline query and answers Telegram via the provided service.
func (e *Engine) Execute(ctx context.Context, svc core.TelegramServicer, queryID int64, userID int64, rawQuery string, offset string) error {
	trimmed := strings.TrimSpace(rawQuery)
	correlationID := fmt.Sprintf("inline-%d-%d", queryID, time.Now().UnixNano())

	var allResults []InlineResult

	// 1. Check cache first
	if cached, ok := e.cache.Get(trimmed); ok {
		allResults = cached
	} else {
		// 2. Resolve matching handler
		handler, args, ok := e.registry.Resolve(trimmed)
		if !ok {
			e.logger.Debug("no inline handler matched query", zap.String("query", trimmed), zap.String("correlation_id", correlationID))
			if svc != nil {
				_ = svc.AnswerInlineQuery(ctx, queryID, nil, "", 1)
			}
			return ErrNoMatchingHandler
		}

		inlineCtx := &InlineContext{
			Ctx:           ctx,
			QueryID:       queryID,
			UserID:        userID,
			RawQuery:      trimmed,
			Pattern:       handler.Pattern(),
			Args:          args,
			Offset:        offset,
			CorrelationID: correlationID,
		}

		results, err := handler.HandleInline(inlineCtx)
		if err != nil {
			e.logger.Warn("inline handler execution error", zap.Error(err), zap.String("correlation_id", correlationID))
			if svc != nil {
				_ = svc.AnswerInlineQuery(ctx, queryID, nil, "", 1)
			}
			return err
		}

		allResults = results
		if len(allResults) > 0 {
			e.cache.Set(trimmed, allResults, 30*time.Second)
		}
	}

	// 3. Paginate results
	pageResults, nextOffset := e.paginator.Paginate(allResults, offset)

	// 4. Convert high-level InlineResult into MTProto tg.InputBotInlineResultClass
	tgResults := make([]tg.InputBotInlineResultClass, 0, len(pageResults))
	for _, res := range pageResults {
		msg := &tg.InputBotInlineMessageText{
			Message: res.Text,
		}
		if res.Markup != nil {
			if tgMarkup := res.Markup.ToTelegramMarkup(); tgMarkup != nil {
				msg.ReplyMarkup = tgMarkup
				msg.SetFlags()
			}
		}

		item := &tg.InputBotInlineResult{
			ID:          res.ID,
			Type:        "article",
			Title:       res.Title,
			Description: res.Description,
			SendMessage: msg,
		}

		if res.ThumbURL != "" {
			item.SetThumb(tg.InputWebDocument{
				URL:      res.ThumbURL,
				MimeType: "image/jpeg",
			})
		}
		if res.URL != "" {
			item.SetURL(res.URL)
		}
		item.SetFlags()

		tgResults = append(tgResults, item)
	}

	// 5. Send answers to Telegram
	if svc != nil {
		return svc.AnswerInlineQuery(ctx, queryID, tgResults, nextOffset, e.cacheTime)
	}
	return nil
}
