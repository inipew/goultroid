package core

import (
	"testing"
)

func TestGroupMutationRequirementExactRights(t *testing.T) {
	tests := []struct {
		action GroupMutationAction
		rights GroupAdminRights
	}{
		{GroupMutationBan, GroupAdminRights{BanUsers: true}},
		{GroupMutationUnban, GroupAdminRights{BanUsers: true}},
		{GroupMutationKick, GroupAdminRights{BanUsers: true}},
		{GroupMutationMute, GroupAdminRights{BanUsers: true}},
		{GroupMutationUnmute, GroupAdminRights{BanUsers: true}},
		{GroupMutationDefaultPermissions, GroupAdminRights{BanUsers: true}},
		{GroupMutationPin, GroupAdminRights{PinMessages: true}},
		{GroupMutationUnpin, GroupAdminRights{PinMessages: true}},
		{GroupMutationPurge, GroupAdminRights{DeleteMessages: true}},
		{GroupMutationPromote, GroupAdminRights{AddAdmins: true}},
		{GroupMutationDemote, GroupAdminRights{AddAdmins: true}},
	}

	for _, tc := range tests {
		t.Run(string(tc.action), func(t *testing.T) {
			got, err := GroupMutationRequirement(tc.action)
			if err != nil {
				t.Fatal(err)
			}
			if got.Level != GroupAuthorizationAdministrator {
				t.Fatalf("level=%s, want administrator", got.Level)
			}
			if got.Rights != tc.rights {
				t.Fatalf("rights=%+v, want %+v", got.Rights, tc.rights)
			}
		})
	}
}

func TestGroupMutationTargetClassification(t *testing.T) {
	for _, action := range []GroupMutationAction{
		GroupMutationBan,
		GroupMutationUnban,
		GroupMutationKick,
		GroupMutationMute,
		GroupMutationUnmute,
		GroupMutationPromote,
		GroupMutationDemote,
	} {
		if !GroupMutationTargetsParticipant(action) {
			t.Fatalf("%s should require participant hierarchy validation", action)
		}
	}
	for _, action := range []GroupMutationAction{
		GroupMutationPin,
		GroupMutationUnpin,
		GroupMutationPurge,
		GroupMutationDefaultPermissions,
	} {
		if GroupMutationTargetsParticipant(action) {
			t.Fatalf("%s unexpectedly requires participant hierarchy validation", action)
		}
	}
}

func TestAssistantPermissionOverrideDoesNotChangeUserbotPermission(t *testing.T) {
	command := Command{
		Permission:          PermissionSudo,
		AssistantPermission: PermissionRef(PermissionEveryone),
	}

	if got := command.EffectivePermission(ExecutionAssistant); got != PermissionEveryone {
		t.Fatalf("Assistant permission=%v, want Everyone", got)
	}
	if got := command.EffectivePermission(ExecutionInteractive); got != PermissionSudo {
		t.Fatalf("Userbot permission=%v, want Sudo", got)
	}
	if got := command.EffectivePermission(ExecutionScheduled); got != PermissionSudo {
		t.Fatalf("non-Assistant permission=%v, want Sudo", got)
	}
}

func TestAssistantPermissionNilPreservesGlobalPermission(t *testing.T) {
	command := Command{Permission: PermissionOwner}
	if got := command.EffectivePermission(ExecutionAssistant); got != PermissionOwner {
		t.Fatalf("Assistant permission=%v, want Owner", got)
	}
}
