package core

import (
	"sync"
	"time"
)

// MetricsCollector defines the interface for collecting runtime operational metrics.
type MetricsCollector interface {
	RecordCommand(name string, duration time.Duration, err error)
	RecordSchedulerJob(jobID int64, actionType string, duration time.Duration, err error)
	RecordTelegramRequest(method string, duration time.Duration, err error)
	RecordCallback(status string, duration time.Duration, err error)
	RecordInline(cacheHit bool, resultCount int, duration time.Duration, err error)
	RecordInlineCacheHit()
	RecordInlineCacheMiss()
	Snapshot() MetricsSnapshot
	Reset()
}

// CommandStats stores aggregate performance metrics for a specific command.
type CommandStats struct {
	TotalCalls int64         `json:"total_calls"`
	Errors     int64         `json:"errors"`
	TotalTime  time.Duration `json:"total_time"`
	MinTime    time.Duration `json:"min_time"`
	MaxTime    time.Duration `json:"max_time"`
}

// MetricsSnapshot provides an immutable point-in-time view of system metrics.
type MetricsSnapshot struct {
	StartTime         time.Time                `json:"start_time"`
	Uptime            time.Duration            `json:"uptime"`
	TotalCommands     int64                    `json:"total_commands"`
	TotalErrors       int64                    `json:"total_errors"`
	Commands          map[string]*CommandStats `json:"commands"`
	SchedulerJobsRun  int64                    `json:"scheduler_jobs_run"`
	SchedulerJobsFail int64                    `json:"scheduler_jobs_fail"`
	TelegramRequests  int64                    `json:"telegram_requests"`
	TelegramErrors    int64                    `json:"telegram_errors"`

	CallbackReceived     int64         `json:"callback_received"`
	CallbackInvalid      int64         `json:"callback_invalid"`
	CallbackUnauthorized int64         `json:"callback_unauthorized"`
	CallbackExpired      int64         `json:"callback_expired"`
	CallbackHandlerError int64         `json:"callback_handler_error"`
	CallbackTotalTime    time.Duration `json:"callback_total_time"`
	CallbackMinTime      time.Duration `json:"callback_min_time"`
	CallbackMaxTime      time.Duration `json:"callback_max_time"`
	InlineReceived       int64         `json:"inline_received"`
	InlineCacheHit       int64         `json:"inline_cache_hit"`
	InlineCacheMiss      int64         `json:"inline_cache_miss"`
	InlineHandlerError   int64         `json:"inline_handler_error"`
	InlineAnswerError    int64         `json:"inline_answer_error"`
	InlineTotalTime      time.Duration `json:"inline_total_time"`
	InlineMinTime        time.Duration `json:"inline_min_time"`
	InlineMaxTime        time.Duration `json:"inline_max_time"`
	InlineResultCount    int64         `json:"inline_result_count"`
}

// DefaultMetricsTracker is a thread-safe in-memory implementation of MetricsCollector.
type DefaultMetricsTracker struct {
	mu                sync.RWMutex
	startTime         time.Time
	totalCommands     int64
	totalErrors       int64
	commands          map[string]*CommandStats
	schedulerJobsRun  int64
	schedulerJobsFail int64
	telegramRequests  int64
	telegramErrors    int64

	callbackReceived     int64
	callbackInvalid      int64
	callbackUnauthorized int64
	callbackExpired      int64
	callbackHandlerError int64
	callbackTotalTime    time.Duration
	callbackMinTime      time.Duration
	callbackMaxTime      time.Duration
	inlineReceived       int64
	inlineCacheHit       int64
	inlineCacheMiss      int64
	inlineHandlerError   int64
	inlineAnswerError    int64
	inlineTotalTime      time.Duration
	inlineMinTime        time.Duration
	inlineMaxTime        time.Duration
	inlineResultCount    int64
}

// NewDefaultMetricsTracker creates a new initialized metrics tracker.
func NewDefaultMetricsTracker() *DefaultMetricsTracker {
	return &DefaultMetricsTracker{
		startTime: time.Now(),
		commands:  make(map[string]*CommandStats),
	}
}

// RecordCommand records execution duration and error status of a command.
func (m *DefaultMetricsTracker) RecordCommand(name string, duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.totalCommands++
	if err != nil {
		m.totalErrors++
	}

	st, exists := m.commands[name]
	if !exists {
		st = &CommandStats{
			MinTime: duration,
			MaxTime: duration,
		}
		m.commands[name] = st
	}

	st.TotalCalls++
	if err != nil {
		st.Errors++
	}
	st.TotalTime += duration

	if duration < st.MinTime {
		st.MinTime = duration
	}
	if duration > st.MaxTime {
		st.MaxTime = duration
	}
}

// RecordSchedulerJob records execution duration and error status of a scheduled job.
func (m *DefaultMetricsTracker) RecordSchedulerJob(jobID int64, actionType string, duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.schedulerJobsRun++
	if err != nil {
		m.schedulerJobsFail++
	}
}

// RecordTelegramRequest records a raw MTProto request event.
func (m *DefaultMetricsTracker) RecordTelegramRequest(method string, duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.telegramRequests++
	if err != nil {
		m.telegramErrors++
	}
}

// RecordCallback records callback interaction metrics.
func (m *DefaultMetricsTracker) RecordCallback(status string, duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.callbackReceived++
	switch status {
	case "invalid":
		m.callbackInvalid++
	case "unauthorized":
		m.callbackUnauthorized++
	case "expired":
		m.callbackExpired++
	case "handler_error":
		if err != nil {
			m.callbackHandlerError++
		}
	case "handler_panic":
		m.callbackHandlerError++
	case "rate_limited":
		m.callbackInvalid++
	}
	// histogram
	if duration > 0 {
		m.callbackTotalTime += duration
		if m.callbackMinTime == 0 || duration < m.callbackMinTime {
			m.callbackMinTime = duration
		}
		if duration > m.callbackMaxTime {
			m.callbackMaxTime = duration
		}
	}
}

// RecordInline records inline query execution.
func (m *DefaultMetricsTracker) RecordInline(cacheHit bool, resultCount int, duration time.Duration, err error) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.inlineReceived++
	if cacheHit {
		m.inlineCacheHit++
	} else {
		m.inlineCacheMiss++
	}
	if err != nil {
		m.inlineHandlerError++
	}
	m.inlineResultCount += int64(resultCount)
	if duration > 0 {
		m.inlineTotalTime += duration
		if m.inlineMinTime == 0 || duration < m.inlineMinTime {
			m.inlineMinTime = duration
		}
		if duration > m.inlineMaxTime {
			m.inlineMaxTime = duration
		}
	}
}

// RecordInlineCacheHit records explicit cache hit.
func (m *DefaultMetricsTracker) RecordInlineCacheHit() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.inlineCacheHit++
	m.mu.Unlock()
}

// RecordInlineCacheMiss records explicit cache miss.
func (m *DefaultMetricsTracker) RecordInlineCacheMiss() {
	if m == nil {
		return
	}
	m.mu.Lock()
	m.inlineCacheMiss++
	m.mu.Unlock()
}

// Snapshot returns a point-in-time copy of collected metrics.
func (m *DefaultMetricsTracker) Snapshot() MetricsSnapshot {
	if m == nil {
		return MetricsSnapshot{}
	}
	m.mu.RLock()
	defer m.mu.RUnlock()

	cmdCopy := make(map[string]*CommandStats, len(m.commands))
	for k, v := range m.commands {
		cmdCopy[k] = &CommandStats{
			TotalCalls: v.TotalCalls,
			Errors:     v.Errors,
			TotalTime:  v.TotalTime,
			MinTime:    v.MinTime,
			MaxTime:    v.MaxTime,
		}
	}

	return MetricsSnapshot{
		StartTime:            m.startTime,
		Uptime:               time.Since(m.startTime),
		TotalCommands:        m.totalCommands,
		TotalErrors:          m.totalErrors,
		Commands:             cmdCopy,
		SchedulerJobsRun:     m.schedulerJobsRun,
		SchedulerJobsFail:    m.schedulerJobsFail,
		TelegramRequests:     m.telegramRequests,
		TelegramErrors:       m.telegramErrors,
		CallbackReceived:     m.callbackReceived,
		CallbackInvalid:      m.callbackInvalid,
		CallbackUnauthorized: m.callbackUnauthorized,
		CallbackExpired:      m.callbackExpired,
		CallbackHandlerError: m.callbackHandlerError,
		CallbackTotalTime:    m.callbackTotalTime,
		CallbackMinTime:      m.callbackMinTime,
		CallbackMaxTime:      m.callbackMaxTime,
		InlineReceived:       m.inlineReceived,
		InlineCacheHit:       m.inlineCacheHit,
		InlineCacheMiss:      m.inlineCacheMiss,
		InlineHandlerError:   m.inlineHandlerError,
		InlineAnswerError:    m.inlineAnswerError,
		InlineTotalTime:      m.inlineTotalTime,
		InlineMinTime:        m.inlineMinTime,
		InlineMaxTime:        m.inlineMaxTime,
		InlineResultCount:    m.inlineResultCount,
	}
}

// Reset resets all accumulated metrics.
func (m *DefaultMetricsTracker) Reset() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	m.startTime = time.Now()
	m.totalCommands = 0
	m.totalErrors = 0
	m.commands = make(map[string]*CommandStats)
	m.schedulerJobsRun = 0
	m.schedulerJobsFail = 0
	m.telegramRequests = 0
	m.telegramErrors = 0
	m.callbackReceived = 0
	m.callbackInvalid = 0
	m.callbackUnauthorized = 0
	m.callbackExpired = 0
	m.callbackHandlerError = 0
	m.callbackTotalTime = 0
	m.callbackMinTime = 0
	m.callbackMaxTime = 0
	m.inlineReceived = 0
	m.inlineCacheHit = 0
	m.inlineCacheMiss = 0
	m.inlineHandlerError = 0
	m.inlineAnswerError = 0
	m.inlineTotalTime = 0
	m.inlineMinTime = 0
	m.inlineMaxTime = 0
	m.inlineResultCount = 0
}
