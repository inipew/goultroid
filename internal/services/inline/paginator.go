package inline

import (
	"strconv"
)

const (
	// DefaultPageSize specifies default count of inline results per page.
	DefaultPageSize = 10
	// MaxPageSize limits the maximum results per inline page (Telegram maximum is 50).
	MaxPageSize = 50
)

// Paginator slices inline query results across pages with numeric string offsets.
type Paginator struct {
	pageSize int
}

// NewPaginator creates a Paginator with a custom page size.
func NewPaginator(pageSize int) *Paginator {
	if pageSize <= 0 {
		pageSize = DefaultPageSize
	}
	if pageSize > MaxPageSize {
		pageSize = MaxPageSize
	}
	return &Paginator{pageSize: pageSize}
}

// Paginate slices the given results according to offset string.
// Returns the page items and next offset string (empty if no more pages).
func (p *Paginator) Paginate(results []InlineResult, offsetStr string) ([]InlineResult, string) {
	if len(results) == 0 {
		return nil, ""
	}

	offset := 0
	if offsetStr != "" {
		if parsed, err := strconv.Atoi(offsetStr); err == nil && parsed >= 0 {
			offset = parsed
		}
	}

	if offset >= len(results) {
		return nil, ""
	}

	end := offset + p.pageSize
	nextOffset := ""
	if end < len(results) {
		nextOffset = strconv.Itoa(end)
	} else {
		end = len(results)
	}

	return results[offset:end], nextOffset
}
