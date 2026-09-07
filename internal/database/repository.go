package database

import (
	"context"
)

// SudoRepository defines access methods for sudo user management.
type SudoRepository interface {
	GetSudoUsers(ctx context.Context) ([]SudoUser, error)
	AddSudoUser(ctx context.Context, userID, addedBy int64) error
	RemoveSudoUser(ctx context.Context, userID int64) error
	IsSudoUser(ctx context.Context, userID int64) (bool, error)
}

// NotesRepository defines access methods for chat notes.
type NotesRepository interface {
	SaveNote(ctx context.Context, chatID int64, name, content string) error
	GetNote(ctx context.Context, chatID int64, name string) (*Note, error)
	ListNotes(ctx context.Context, chatID int64) ([]string, error)
	DeleteNote(ctx context.Context, chatID int64, name string) error
}

// AFKRepository defines access methods for AFK state tracking.
type AFKRepository interface {
	SetAFK(ctx context.Context, userID int64, isAFK bool, reason string) error
	GetAFK(ctx context.Context, userID int64) (*AFK, error)
}

// FiltersRepository defines access methods for chat auto-reply filters.
type FiltersRepository interface {
	SaveFilter(ctx context.Context, chatID int64, keyword, replyText string) error
	GetFilter(ctx context.Context, chatID int64, keyword string) (*Filter, error)
	ListFilters(ctx context.Context, chatID int64) ([]Filter, error)
	DeleteFilter(ctx context.Context, chatID int64, keyword string) error
}

// BlacklistRepository defines access methods for chat keyword blacklists.
type BlacklistRepository interface {
	AddBlacklist(ctx context.Context, chatID int64, word string) error
	RemoveBlacklist(ctx context.Context, chatID int64, word string) error
	ListBlacklists(ctx context.Context, chatID int64) ([]string, error)
}

// ModerationRepository groups moderation/warning persistence.
type ModerationRepository interface {
	// AddWarning and related methods are defined in moderation.go
}

// PMPermitRepository groups PM permit persistence.
type PMPermitRepository interface {
	// PM permit methods are defined in pmpermit.go
}

// Repository aggregates all domain repositories for backward compatibility.
// New services should prefer depending directly on segregated domain interfaces
// (e.g. SettingsRepository, SchedulerRepository, SudoRepository, etc.).
type Repository interface {
	SettingsRepository
	SchedulerRepository
	PeerRepository
	SudoRepository
	NotesRepository
	AFKRepository
	FiltersRepository
	BlacklistRepository
}

// Ensure DB implements Repository and segregated interfaces at compile time.
var _ Repository = (*DB)(nil)
var _ SettingsRepository = (*DB)(nil)
var _ SchedulerRepository = (*DB)(nil)
var _ PeerRepository = (*DB)(nil)
var _ SudoRepository = (*DB)(nil)
var _ NotesRepository = (*DB)(nil)
var _ AFKRepository = (*DB)(nil)
var _ FiltersRepository = (*DB)(nil)
var _ BlacklistRepository = (*DB)(nil)
