package execution

// Actor captures the identity, location, and privilege level of the entity triggering an action.
type Actor struct {
	UserID    int64
	ChatID    int64
	IsOwner   bool
	IsSudo    bool
	Username  string
	FirstName string
}

// NewActor creates an Actor with the given basic parameters.
func NewActor(userID int64, chatID int64, isOwner bool, isSudo bool) Actor {
	return Actor{
		UserID:  userID,
		ChatID:  chatID,
		IsOwner: isOwner,
		IsSudo:  isSudo,
	}
}

// CanExecute reports whether the actor meets the required privileges.
func (a Actor) CanExecute(requireOwner bool, requireSudo bool) bool {
	if a.IsOwner {
		return true
	}
	if requireOwner {
		return false
	}
	if requireSudo {
		return a.IsSudo
	}
	return true
}
