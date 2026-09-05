package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

// ActionType represents the typed action to be executed by a scheduled job.
type ActionType string

const (
	// ActionMessage indicates the scheduled job sends a text message to the chat.
	ActionMessage = "message"
	// ActionCommand indicates the scheduled job executes a userbot command.
	ActionCommand = "command"
)

// ParseActionType validates and parses an action type string at the boundary.
func ParseActionType(s string) (ActionType, error) {
	switch s {
	case ActionMessage:
		return ActionType(ActionMessage), nil
	case ActionCommand:
		return ActionType(ActionCommand), nil
	default:
		return "", fmt.Errorf("%w: invalid action type %q (must be %q or %q)", core.ErrInvalidArgs, s, ActionMessage, ActionCommand)
	}
}

// TaskFunc defines the programmatic callback signature for in-memory periodic tasks.
type TaskFunc func(ctx context.Context) error

// Service defines the central task scheduler interface for GoUltroid.
type Service interface {
	// Programmatic in-memory tasks for plugins (e.g. database cleanup, background sync)
	RegisterPeriodicTask(name string, interval time.Duration, task TaskFunc) error
	UnregisterPeriodicTask(name string) error

	// Persistent scheduled jobs for users & plugins (persisted in SQLite)
	ScheduleOnce(ctx context.Context, chatID int64, peerType string, accessHash int64, when time.Time, actionType string, payload string, creatorID ...int64) (*database.ScheduledJob, error)
	ScheduleRecurring(ctx context.Context, chatID int64, peerType string, accessHash int64, interval time.Duration, actionType string, payload string, creatorID ...int64) (*database.ScheduledJob, error)

	// Cancellation & Listing
	Cancel(ctx context.Context, jobID int64) error
	List(ctx context.Context, chatID int64) ([]database.ScheduledJob, error)

	// Lifecycle
	Start(ctx context.Context) error
	Stop() error
}
