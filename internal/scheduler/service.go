package scheduler

import (
	"context"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

// ActionType represents the typed action to be executed by a scheduled job.
type ActionType string

const (
	// ActionMessage indicates the scheduled job sends a text message to the chat.
	ActionMessage = "message"
	// ActionCommand indicates the scheduled job executes a userbot command.
	ActionCommand = "command"
	// ActionJob indicates the scheduled job triggers a declarative job in jobs.Manager.
	ActionJob = "job"
)

// ParseActionType validates and parses an action type string at the boundary.
func ParseActionType(s string) (ActionType, error) {
	switch s {
	case ActionMessage:
		return ActionType(ActionMessage), nil
	case ActionCommand:
		return ActionType(ActionCommand), nil
	case ActionJob:
		return ActionType(ActionJob), nil
	default:
		return "", fmt.Errorf("%w: invalid action type %q (must be %q, %q, or %q)", core.ErrInvalidArgs, s, ActionMessage, ActionCommand, ActionJob)
	}
}

// TaskFunc defines the programmatic callback signature for in-memory periodic tasks.
type TaskFunc func(ctx context.Context) error

// Service defines the central task scheduler interface for GoUltroid.
type Service interface {
	RegisterPeriodicTask(name string, interval time.Duration, task TaskFunc) error
	UnregisterPeriodicTask(name string) error

	ScheduleOnce(ctx context.Context, chatID int64, peerType string, accessHash int64, when time.Time, actionType string, payload string, creatorID ...int64) (*ScheduledJob, error)
	ScheduleRecurring(ctx context.Context, chatID int64, peerType string, accessHash int64, interval time.Duration, actionType string, payload string, creatorID ...int64) (*ScheduledJob, error)

	// Persistent mutations require both the requester identity and chat scope.
	// This prevents a valid sudo user in one chat from operating on another
	// chat's numeric job ID.
	CancelScoped(ctx context.Context, requesterID, chatID, jobID int64) error
	List(ctx context.Context, chatID int64) ([]ScheduledJob, error)
	JobHistoryScoped(ctx context.Context, requesterID, chatID, jobID int64, limit int) ([]JobHistoryEntry, error)

	Start(ctx context.Context) error
	Stop(ctx context.Context) error
}
