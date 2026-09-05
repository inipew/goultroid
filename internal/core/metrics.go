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
		StartTime:         m.startTime,
		Uptime:            time.Since(m.startTime),
		TotalCommands:     m.totalCommands,
		TotalErrors:       m.totalErrors,
		Commands:          cmdCopy,
		SchedulerJobsRun:  m.schedulerJobsRun,
		SchedulerJobsFail: m.schedulerJobsFail,
		TelegramRequests:  m.telegramRequests,
		TelegramErrors:    m.telegramErrors,
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
}
