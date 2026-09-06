package voice

import (
	"errors"
	"time"
)

// SourceType denotes the media stream type.
type SourceType string

const (
	SourceAudio      SourceType = "audio"
	SourceVideo      SourceType = "video"
	SourceLiveStream SourceType = "stream"
)

// State represents the lifecycle state of a voice chat session.
type State string

const (
	StateIdle     State = "IDLE"
	StateJoining  State = "JOINING"
	StatePlaying  State = "PLAYING"
	StatePaused   State = "PAUSED"
	StateStopping State = "STOPPING"
	StateFailed   State = "FAILED"
)

// RepeatMode controls queue looping behavior.
type RepeatMode string

const (
	RepeatOff   RepeatMode = "off"
	RepeatTrack RepeatMode = "track"
	RepeatQueue RepeatMode = "queue"
)

// Source represents an audio/video media item to be played.
type Source struct {
	ID          string        `json:"id"`
	Title       string        `json:"title"`
	Artist      string        `json:"artist"`
	Duration    time.Duration `json:"duration"`
	SourceURL   string        `json:"source_url"`
	FilePath    string        `json:"file_path"`
	Type        SourceType    `json:"type"`
	RequesterID int64         `json:"requester_id"`
	ChatID      int64         `json:"chat_id"`
}

var (
	ErrSessionNotFound        = errors.New("voice session not found for chat")
	ErrInvalidStateTransition = errors.New("invalid voice session state transition")
	ErrAlreadyPlaying         = errors.New("track is already playing")
	ErrNotPlaying             = errors.New("no track currently playing")
	ErrQueueEmpty             = errors.New("playback queue is empty")
	ErrInvalidVolume          = errors.New("volume must be between 0 and 200")
	ErrBackendFailed          = errors.New("voice backend operation failed")
	ErrUnsupportedMedia       = errors.New("unsupported voice media format")
	ErrVoiceChatNotFound      = errors.New("no active voice chat found in group")
)
