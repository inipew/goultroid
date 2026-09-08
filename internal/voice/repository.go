package voice

import (
	"context"
	"time"
)

// VoiceSessionRecord represents the persisted state of a voice chat session.
type VoiceSessionRecord struct {
	ChatID     int64     `json:"chat_id"`
	State      string    `json:"state"`
	Volume     int       `json:"volume"`
	RepeatMode string    `json:"repeat_mode"`
	UpdatedAt  time.Time `json:"updated_at"`
}

// VoiceQueueRecord represents a queued audio/video track for voice chat playback.
type VoiceQueueRecord struct {
	ID              int64     `json:"id"`
	ChatID          int64     `json:"chat_id"`
	Position        int       `json:"position"`
	Title           string    `json:"title"`
	Artist          string    `json:"artist"`
	SourceURL       string    `json:"source_url"`
	FilePath        string    `json:"file_path"`
	DurationSeconds int       `json:"duration_seconds"`
	SourceType      string    `json:"source_type"`
	RequesterID     int64     `json:"requester_id"`
	CreatedAt       time.Time `json:"created_at"`
}

// Repository defines persistence operations for voice sessions and queues.
type Repository interface {
	GetVoiceSession(ctx context.Context, chatID int64) (*VoiceSessionRecord, error)
	UpsertVoiceSession(ctx context.Context, rec *VoiceSessionRecord) error
	GetVoiceQueue(ctx context.Context, chatID int64) ([]*VoiceQueueRecord, error)
	AddVoiceQueueTrack(ctx context.Context, track *VoiceQueueRecord) error
	PopVoiceQueueTrack(ctx context.Context, chatID int64) (*VoiceQueueRecord, error)
	ClearVoiceQueue(ctx context.Context, chatID int64) error
	DeleteVoiceQueueTrack(ctx context.Context, id int64) error
}
