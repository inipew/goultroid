package native

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestA5CNativeGroupRoleProviderTracksLifecycleWithoutRetainingAuthority(t *testing.T) {
	adapter := &Adapter{}
	if adapter.GroupRoleResolver() != nil {
		t.Fatal("native adapter must fail closed without a role provider")
	}
	roles := &a5GroupRoles{}
	var active core.GroupRoleResolver = roles
	adapter.SetGroupRoleProvider(func() core.GroupRoleResolver { return active })
	if adapter.GroupRoleResolver() != roles {
		t.Fatal("native provider did not expose the live authoritative resolver")
	}
	active = nil // Assistant stopped: no previous resolver may remain usable.
	if adapter.GroupRoleResolver() != nil {
		t.Fatal("native provider retained authority after Assistant shutdown")
	}
	adapter.SetGroupRoleProvider(nil)
	if adapter.GroupRoleResolver() != nil {
		t.Fatal("detached provider must fail closed")
	}
}

func TestA5CNativeGroupRoleProviderNilAdapter(t *testing.T) {
	var adapter *Adapter
	adapter.SetGroupRoleProvider(nil)
	if adapter.GroupRoleResolver() != nil {
		t.Fatal("nil native adapter cannot provide authority")
	}
}
