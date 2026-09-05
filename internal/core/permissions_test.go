package core

import "testing"

func TestPermissions_String(t *testing.T) {
	if PermissionEveryone.String() != "Everyone" {
		t.Errorf("expected Everyone, got %s", PermissionEveryone.String())
	}
	if PermissionSudo.String() != "Sudo" {
		t.Errorf("expected Sudo, got %s", PermissionSudo.String())
	}
	if PermissionOwner.String() != "Owner" {
		t.Errorf("expected Owner, got %s", PermissionOwner.String())
	}
}

func TestPermissions_LevelsAndCanRun(t *testing.T) {
	ownerID := int64(1001)
	sudoIDs := []int64{2001, 2002, 0}
	normalID := int64(3001)

	perms := NewPermissions(ownerID, sudoIDs)

	cmdEveryone := Command{Name: "ping", Permission: PermissionEveryone}
	cmdSudo := Command{Name: "ban", Permission: PermissionSudo}
	cmdOwner := Command{Name: "restart", Permission: PermissionOwner}

	// 1. Owner checks
	if !perms.IsOwner(ownerID) {
		t.Errorf("expected %d to be owner", ownerID)
	}
	if !perms.IsSudo(ownerID) {
		t.Errorf("owner should also have sudo privileges")
	}
	if perms.Level(ownerID) != PermissionOwner {
		t.Errorf("expected PermissionOwner, got %v", perms.Level(ownerID))
	}
	if !perms.CanRun(ownerID, cmdEveryone) {
		t.Errorf("owner should be able to run cmdEveryone")
	}
	if !perms.CanRun(ownerID, cmdSudo) {
		t.Errorf("owner should be able to run cmdSudo")
	}
	if !perms.CanRun(ownerID, cmdOwner) {
		t.Errorf("owner should be able to run cmdOwner")
	}

	// 2. Sudo checks
	for _, sudoID := range []int64{2001, 2002} {
		if perms.IsOwner(sudoID) {
			t.Errorf("sudo %d should not be owner", sudoID)
		}
		if !perms.IsSudo(sudoID) {
			t.Errorf("expected %d to be sudo", sudoID)
		}
		if perms.Level(sudoID) != PermissionSudo {
			t.Errorf("expected PermissionSudo, got %v", perms.Level(sudoID))
		}
		if !perms.CanRun(sudoID, cmdEveryone) {
			t.Errorf("sudo should be able to run cmdEveryone")
		}
		if !perms.CanRun(sudoID, cmdSudo) {
			t.Errorf("sudo should be able to run cmdSudo")
		}
		if perms.CanRun(sudoID, cmdOwner) {
			t.Errorf("sudo should NOT be able to run cmdOwner")
		}
	}

	// 3. Normal user checks
	if perms.IsOwner(normalID) {
		t.Errorf("normal user %d should not be owner", normalID)
	}
	if perms.IsSudo(normalID) {
		t.Errorf("normal user %d should not be sudo", normalID)
	}
	if perms.Level(normalID) != PermissionEveryone {
		t.Errorf("expected PermissionEveryone, got %v", perms.Level(normalID))
	}
	if !perms.CanRun(normalID, cmdEveryone) {
		t.Errorf("normal user should be able to run cmdEveryone")
	}
	if perms.CanRun(normalID, cmdSudo) {
		t.Errorf("normal user should NOT be able to run cmdSudo")
	}
	if perms.CanRun(normalID, cmdOwner) {
		t.Errorf("normal user should NOT be able to run cmdOwner")
	}

	// 4. Nil permissions safety
	var nilPerms *Permissions
	if nilPerms.IsOwner(ownerID) {
		t.Errorf("nil perms should not report owner")
	}
	if nilPerms.IsSudo(ownerID) {
		t.Errorf("nil perms should not report sudo")
	}
	if nilPerms.Level(ownerID) != PermissionEveryone {
		t.Errorf("nil perms level should be PermissionEveryone")
	}
	if !nilPerms.CanRun(ownerID, cmdEveryone) {
		t.Errorf("nil perms should still allow cmdEveryone")
	}
	if nilPerms.CanRun(ownerID, cmdSudo) {
		t.Errorf("nil perms should not allow cmdSudo")
	}
}

func TestPermissions_DynamicSudo(t *testing.T) {
	perms := NewPermissions(100, []int64{200})

	if !perms.IsSudo(200) {
		t.Errorf("200 should be sudo initially")
	}
	if perms.IsSudo(300) {
		t.Errorf("300 should not be sudo initially")
	}

	// Dynamic add
	perms.AddSudo(300)
	if !perms.IsSudo(300) {
		t.Errorf("300 should be sudo after AddSudo")
	}

	list := perms.ListSudo()
	if len(list) != 2 {
		t.Errorf("expected 2 sudo users, got %d", len(list))
	}

	// Dynamic remove
	perms.RemoveSudo(200)
	if perms.IsSudo(200) {
		t.Errorf("200 should not be sudo after RemoveSudo")
	}

	// Nil receiver safety
	var nilPerms *Permissions
	nilPerms.AddSudo(123)
	nilPerms.RemoveSudo(123)
	if nilPerms.ListSudo() != nil {
		t.Errorf("expected nil list from nil perms")
	}
}
