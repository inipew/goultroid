package inline

import (
	"context"
	"errors"

	"github.com/inipew/goultroid/internal/ui"
)

var (
	// ErrNoMatchingHandler indicates no registered inline handler matched the incoming query.
	ErrNoMatchingHandler = errors.New("no matching inline handler found")
	// ErrEmptyResults indicates the handler produced zero inline results.
	ErrEmptyResults = errors.New("no inline results available")
)

// InlineContext contains query input, requester identity, and execution context.
type InlineContext struct {
	Ctx           context.Context
	QueryID       int64
	UserID        int64
	RawQuery      string
	Pattern       string
	Args          []string
	Offset        string
	CorrelationID string
}

// InlineResult represents a high-level search article result for inline queries.
type InlineResult struct {
	ID          string
	Title       string
	Description string
	Text        string
	Markup      *ui.Markup
	ThumbURL    string
	URL         string
}

// InlineHandler processes an inline search query matching a specific keyword or pattern.
type InlineHandler interface {
	Pattern() string
	Description() string
	HandleInline(ctx *InlineContext) ([]InlineResult, error)
}
