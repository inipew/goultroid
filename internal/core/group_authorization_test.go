package core

import (
	"errors"
	"testing"
)

func verifiedGroupPrincipal(role GroupActorRole, rights GroupAdminRights) GroupActorPrincipal {
	return GroupActorPrincipal{UserID: 42, Role: role, Rights: rights, Verified: true}
}

func TestGroupAuthorizationRequirementRoleHierarchy(t *testing.T) {
	tests := []struct {
		name        string
		requirement GroupAuthorizationRequirement
		role        GroupActorRole
		allowed     bool
	}{
		{"member accepts member", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleMember, true},
		{"member accepts restricted participant", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleRestricted, true},
		{"member accepts admin", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleAdministrator, true},
		{"member accepts creator", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleCreator, true},
		{"member rejects left", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleLeft, false},
		{"member rejects banned", GroupAuthorizationRequirement{Level: GroupAuthorizationMember}, GroupActorRoleBanned, false},
		{"admin accepts admin", GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator}, GroupActorRoleAdministrator, true},
		{"admin accepts creator", GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator}, GroupActorRoleCreator, true},
		{"admin rejects member", GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator}, GroupActorRoleMember, false},
		{"creator accepts creator", GroupAuthorizationRequirement{Level: GroupAuthorizationCreator}, GroupActorRoleCreator, true},
		{"creator rejects admin", GroupAuthorizationRequirement{Level: GroupAuthorizationCreator}, GroupActorRoleAdministrator, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.requirement.Authorize(verifiedGroupPrincipal(tc.role, GroupAdminRights{}))
			if tc.allowed && err != nil {
				t.Fatalf("Authorize() error=%v", err)
			}
			if !tc.allowed && !errors.Is(err, ErrGroupAuthorizationDenied) {
				t.Fatalf("Authorize() error=%v, want ErrGroupAuthorizationDenied", err)
			}
		})
	}
}

func TestGroupAuthorizationRequirementAdminRights(t *testing.T) {
	requirement := GroupAuthorizationRequirement{
		Level: GroupAuthorizationAdministrator,
		Rights: GroupAdminRights{
			BanUsers:       true,
			DeleteMessages: true,
		},
	}

	if err := requirement.Authorize(verifiedGroupPrincipal(GroupActorRoleAdministrator, GroupAdminRights{
		BanUsers:       true,
		DeleteMessages: true,
		PinMessages:    true,
	})); err != nil {
		t.Fatalf("matching admin rights rejected: %v", err)
	}
	if err := requirement.Authorize(verifiedGroupPrincipal(GroupActorRoleAdministrator, GroupAdminRights{
		BanUsers: true,
	})); !errors.Is(err, ErrGroupAuthorizationDenied) {
		t.Fatalf("missing admin right error=%v", err)
	}
}

func TestGroupAuthorizationRequirementDoesNotTreatOwnerOrSudoAsGroupRole(t *testing.T) {
	requirement := GroupAuthorizationRequirement{Level: GroupAuthorizationAdministrator}
	principal := verifiedGroupPrincipal(GroupActorRoleMember, GroupAdminRights{})
	principal.IsOwner = true
	principal.IsSudo = true

	if err := requirement.Authorize(principal); !errors.Is(err, ErrGroupAuthorizationDenied) {
		t.Fatalf("global owner/sudo unexpectedly bypassed group role: %v", err)
	}
}

func TestGroupAuthorizationRequirementFailsClosedWhenUnverified(t *testing.T) {
	requirement := GroupAuthorizationRequirement{Level: GroupAuthorizationMember}
	if err := requirement.Authorize(GroupActorPrincipal{UserID: 42, Role: GroupActorRoleAdministrator}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unverified principal error=%v, want ErrUnavailable", err)
	}
}

func TestGroupAuthorizationRequirementValidation(t *testing.T) {
	if err := (GroupAuthorizationRequirement{
		Level:  GroupAuthorizationMember,
		Rights: GroupAdminRights{BanUsers: true},
	}).Validate(); err == nil {
		t.Fatal("member requirement with admin rights should be invalid")
	}
	if err := (GroupAuthorizationRequirement{
		Level:  GroupAuthorizationCreator,
		Rights: GroupAdminRights{BanUsers: true},
	}).Validate(); err == nil {
		t.Fatal("creator requirement with admin rights should be invalid")
	}
	if err := (GroupAuthorizationRequirement{
		Level:  GroupAuthorizationAdministrator,
		Rights: GroupAdminRights{BanUsers: true},
	}).Validate(); err != nil {
		t.Fatalf("administrator rights requirement rejected: %v", err)
	}
}

func TestRouterRejectsContextualAuthorizationWithoutGroupOnly(t *testing.T) {
	router := NewRouter(".")
	err := router.Register(Command{
		Name: "unsafe",
		GroupAuthorization: GroupAuthorizationRequirement{
			Level: GroupAuthorizationAdministrator,
		},
		Handler: func(*Context) error { return nil },
	})
	if err == nil {
		t.Fatal("contextual group authorization without GroupOnly should fail registration")
	}
}
