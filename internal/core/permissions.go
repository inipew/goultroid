package core

import (
	"context"
	"sync"
)

// Principal represents the resolved authorization identity for an execution context.
type Principal struct {
	UserID  int64      `json:"user_id"`
	IsOwner bool       `json:"is_owner"`
	IsSudo  bool       `json:"is_sudo"`
	Level   Permission `json:"level"`
}

// PrincipalResolver dynamically evaluates and resolves identity permissions at execution time.
type PrincipalResolver interface {
	Resolve(ctx context.Context, userID int64) (*Principal, error)
}

// Permissions manages user access tiers (Owner, Sudo, Everyone) in a thread-safe manner.
type Permissions struct {
	OwnerID   int64
	mu        sync.RWMutex
	SudoUsers map[int64]struct{}
}

// NewPermissions creates a new Permissions instance.
func NewPermissions(ownerID int64, sudoUsers []int64) *Permissions {
	sudos := make(map[int64]struct{}, len(sudoUsers))
	for _, id := range sudoUsers {
		if id != 0 {
			sudos[id] = struct{}{}
		}
	}
	return &Permissions{
		OwnerID:   ownerID,
		SudoUsers: sudos,
	}
}

// IsOwner returns true if the userID matches the configured OwnerID.
func (p *Permissions) IsOwner(userID int64) bool {
	return p != nil && p.OwnerID != 0 && userID == p.OwnerID
}

// IsSudo returns true if the userID is either the Owner or in SudoUsers.
func (p *Permissions) IsSudo(userID int64) bool {
	if p == nil {
		return false
	}
	if p.IsOwner(userID) {
		return true
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.SudoUsers[userID]
	return ok
}

// AddSudo dynamically adds a user to the sudo users set.
func (p *Permissions) AddSudo(userID int64) {
	if p == nil || userID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.SudoUsers == nil {
		p.SudoUsers = make(map[int64]struct{})
	}
	p.SudoUsers[userID] = struct{}{}
}

// RemoveSudo dynamically removes a user from the sudo users set.
func (p *Permissions) RemoveSudo(userID int64) {
	if p == nil || userID == 0 {
		return
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.SudoUsers, userID)
}

// ListSudo returns a copy of all registered sudo user IDs.
func (p *Permissions) ListSudo() []int64 {
	if p == nil {
		return nil
	}
	p.mu.RLock()
	defer p.mu.RUnlock()
	list := make([]int64, 0, len(p.SudoUsers))
	for id := range p.SudoUsers {
		list = append(list, id)
	}
	return list
}

// Level returns the highest Permission tier of the given userID.
func (p *Permissions) Level(userID int64) Permission {
	if p == nil {
		return PermissionEveryone
	}
	if p.IsOwner(userID) {
		return PermissionOwner
	}
	if p.IsSudo(userID) {
		return PermissionSudo
	}
	return PermissionEveryone
}

// CanRun checks if the given userID has sufficient permission to run cmd.
func (p *Permissions) CanRun(userID int64, cmd Command) bool {
	return p.Level(userID) >= cmd.Permission
}

var _ PrincipalResolver = (*Permissions)(nil)

// Resolve dynamically resolves user permissions into a Principal at execution time.
func (p *Permissions) Resolve(ctx context.Context, userID int64) (*Principal, error) {
	if p == nil || userID == 0 {
		return &Principal{
			UserID:  userID,
			IsOwner: false,
			IsSudo:  false,
			Level:   PermissionEveryone,
		}, nil
	}

	return &Principal{
		UserID:  userID,
		IsOwner: p.IsOwner(userID),
		IsSudo:  p.IsSudo(userID),
		Level:   p.Level(userID),
	}, nil
}
