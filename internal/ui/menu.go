package ui

// PaginateSlice slices a list of items for the specified 1-indexed page.
// This remains a pure value helper; callback/navigation construction belongs to
// the owning interaction surface.
func PaginateSlice[T any](items []T, page, pageSize int) ([]T, int) {
	if pageSize <= 0 {
		pageSize = 6
	}
	totalItems := len(items)
	if totalItems == 0 {
		return nil, 1
	}

	totalPages := (totalItems + pageSize - 1) / pageSize
	if page < 1 {
		page = 1
	} else if page > totalPages {
		page = totalPages
	}

	start := (page - 1) * pageSize
	end := start + pageSize
	if end > totalItems {
		end = totalItems
	}

	return items[start:end], totalPages
}
