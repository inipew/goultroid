package database

// Repository aggregates all domain repositories for backward compatibility.
// New services should prefer depending directly on segregated domain interfaces
// (e.g. SettingsRepository, SchedulerRepository, etc.).
type Repository interface {
	SettingsRepository
	SchedulerRepository
	PeerRepository
}

// Ensure DB implements Repository and segregated interfaces at compile time.
var _ Repository = (*DB)(nil)
var _ SettingsRepository = (*DB)(nil)
var _ SchedulerRepository = (*DB)(nil)
var _ PeerRepository = (*DB)(nil)
