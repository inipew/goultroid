package core

// Permissions manages user access tiers (Owner, Sudo, Everyone).
type Permissions struct {
	OwnerID   int64
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
	_, ok := p.SudoUsers[userID]
	return ok
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
