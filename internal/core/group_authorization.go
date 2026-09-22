package core

import "fmt"

// GroupAuthorizationLevel is the minimum authoritative Telegram group role
// required by one command on the Assistant surface.
type GroupAuthorizationLevel uint8

const (
	GroupAuthorizationNone GroupAuthorizationLevel = iota
	GroupAuthorizationMember
	GroupAuthorizationAdministrator
	GroupAuthorizationCreator
)

func (l GroupAuthorizationLevel) String() string {
	switch l {
	case GroupAuthorizationMember:
		return "member"
	case GroupAuthorizationAdministrator:
		return "administrator"
	case GroupAuthorizationCreator:
		return "creator"
	default:
		return "none"
	}
}

// GroupAuthorizationRequirement is contextual Telegram authorization metadata.
// It is deliberately independent from PermissionOwner/PermissionSudo: global
// Goultroid identity never satisfies a missing Telegram group role.
type GroupAuthorizationRequirement struct {
	Level  GroupAuthorizationLevel
	Rights GroupAdminRights
}

// ErrGroupAuthorizationDenied means Telegram returned an authoritative role,
// but that role/rights set does not satisfy the command requirement.
var ErrGroupAuthorizationDenied = fmt.Errorf("%w: contextual group authorization denied", ErrForbidden)

func (r GroupAdminRights) Any() bool {
	return r.ChangeInfo || r.DeleteMessages || r.BanUsers || r.InviteUsers ||
		r.PinMessages || r.AddAdmins || r.ManageTopics
}

// Contains reports whether all required rights are present in the verified
// Telegram rights snapshot.
func (r GroupAdminRights) Contains(required GroupAdminRights) bool {
	return (!required.ChangeInfo || r.ChangeInfo) &&
		(!required.DeleteMessages || r.DeleteMessages) &&
		(!required.BanUsers || r.BanUsers) &&
		(!required.InviteUsers || r.InviteUsers) &&
		(!required.PinMessages || r.PinMessages) &&
		(!required.AddAdmins || r.AddAdmins) &&
		(!required.ManageTopics || r.ManageTopics)
}

// Required reports whether the command requests contextual group authorization.
func (r GroupAuthorizationRequirement) Required() bool {
	return r.Level != GroupAuthorizationNone || r.Rights.Any()
}

// Validate rejects ambiguous metadata. Rights are an administrator capability
// requirement; creator-only requirements do not also accept an arbitrary rights
// predicate.
func (r GroupAuthorizationRequirement) Validate() error {
	switch r.Level {
	case GroupAuthorizationNone, GroupAuthorizationMember, GroupAuthorizationAdministrator, GroupAuthorizationCreator:
	default:
		return fmt.Errorf("unknown group authorization level %d", r.Level)
	}
	if r.Rights.Any() && r.Level != GroupAuthorizationAdministrator {
		return fmt.Errorf("group admin rights require administrator authorization level")
	}
	return nil
}

func (r GroupAuthorizationRequirement) roleAllowed(role GroupActorRole) bool {
	switch r.Level {
	case GroupAuthorizationNone:
		return true
	case GroupAuthorizationMember:
		switch role {
		case GroupActorRoleMember, GroupActorRoleRestricted, GroupActorRoleAdministrator, GroupActorRoleCreator:
			return true
		default:
			return false
		}
	case GroupAuthorizationAdministrator:
		return role == GroupActorRoleAdministrator || role == GroupActorRoleCreator
	case GroupAuthorizationCreator:
		return role == GroupActorRoleCreator
	default:
		return false
	}
}

// Authorize evaluates only verified Telegram role/rights state. IsOwner and
// IsSudo are intentionally ignored so global identity cannot become group role.
func (r GroupAuthorizationRequirement) Authorize(principal GroupActorPrincipal) error {
	if !r.Required() {
		return nil
	}
	if !principal.Verified {
		return fmt.Errorf("%w: group role is not verified", ErrUnavailable)
	}
	if !r.roleAllowed(principal.Role) || !principal.Rights.Contains(r.Rights) {
		return fmt.Errorf("%w: requires %s role and declared rights", ErrGroupAuthorizationDenied, r.Level)
	}
	return nil
}
