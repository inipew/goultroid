package inline

// RuntimeStats is a read-only snapshot of bounded Inline vNext retention.
// It does not add a second metrics store; values are read directly from the
// canonical inline cache owned by the engine.
type RuntimeStats struct {
	CacheEntries int
	CacheBytes   int64
}

// RuntimeStats returns bounded cache retention diagnostics for acceptance and
// operational observability.
func (e *Engine) RuntimeStats() RuntimeStats {
	if e == nil || e.cache == nil {
		return RuntimeStats{}
	}
	return RuntimeStats{
		CacheEntries: e.cache.Len(),
		CacheBytes:   e.cache.RetainedBytes(),
	}
}
