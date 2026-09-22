package core

import "fmt"

// GroupMutationAction identifies one privileged Telegram group mutation.
type GroupMutationAction string

const (
	GroupMutationBan                GroupMutationAction = "ban"
	GroupMutationUnban              GroupMutationAction = "unban"
	GroupMutationKick               GroupMutationAction = "kick"
	GroupMutationMute               GroupMutationAction = "mute"
	GroupMutationUnmute             GroupMutationAction = "unmute"
	GroupMutationPin                GroupMutationAction = "pin"
	GroupMutationUnpin              GroupMutationAction = "unpin"
	GroupMutationPurge              GroupMutationAction = "purge"
	GroupMutationPromote            GroupMutationAction = "promote"
	GroupMutationDemote             GroupMutationAction = "demote"
	GroupMutationDefaultPermissions GroupMutationAction = "default_permissions"
)

var (
	// ErrGroupMutationDenied means the operation-specific actor/bot rights gate
	// failed authoritatively.
	ErrGroupMutationDenied = fmt.Errorf("%w: group mutation authorization denied", ErrForbidden)
	// ErrGroupMutationTargetProtected means the target hierarchy makes the
	// requested action unsafe even though the actor has the operation right.
	ErrGroupMutationTargetProtected = fmt.Errorf("%w: group mutation target is protected", ErrForbidden)
)

// GroupMutationRequirement is the canonical operation-to-rights mapping shared
// by command metadata and the Assistant managed mutation transport.
func GroupMutationRequirement(action GroupMutationAction) (GroupAuthorizationRequirement, error) {
	requirement := GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator}
	switch action {
	case GroupMutationBan,
		GroupMutationUnban,
		GroupMutationKick,
		GroupMutationMute,
		GroupMutationUnmute,
		GroupMutationDefaultPermissions:
		requirement.Rights.BanUsers = true
	case GroupMutationPin, GroupMutationUnpin:
		requirement.Rights.PinMessages = true
	case GroupMutationPurge:
		requirement.Rights.DeleteMessages = true
	case GroupMutationPromote, GroupMutationDemote:
		requirement.Rights.AddAdmins = true
	default:
		return GroupAuthorizationRequirement{}, fmt.Errorf("%w: unknown group mutation %q", ErrInvalidArgs, action)
	}
	return requirement, nil
}

// MustGroupMutationRequirement is intended for static command metadata.
func MustGroupMutationRequirement(action GroupMutationAction) GroupAuthorizationRequirement {
	requirement, err := GroupMutationRequirement(action)
	if err != nil {
		panic(err)
	}
	return requirement
}

// GroupMutationTargetsParticipant reports whether the operation requires a
// target participant hierarchy check.
func GroupMutationTargetsParticipant(action GroupMutationAction) bool {
	switch action {
	case GroupMutationBan,
		GroupMutationUnban,
		GroupMutationKick,
		GroupMutationMute,
		GroupMutationUnmute,
		GroupMutationPromote,
		GroupMutationDemote:
		return true
	default:
		return false
	}
}
