package database

// Repository aggregates legacy domain repositories for backward compatibility.
// Feature modules should prefer feature-owned repositories.
type Repository interface {
	PeerRepository
}

// Ensure DB implements Repository and segregated interfaces at compile time.
var _ Repository = (*DB)(nil)
var _ PeerRepository = (*DB)(nil)
