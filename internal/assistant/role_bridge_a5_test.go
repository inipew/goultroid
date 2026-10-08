package assistant

import "testing"

func TestA5CAssistantGroupRoleBridgeUnavailableWhileStopped(t *testing.T) {
	if (*AssistantApp)(nil).GroupRoleResolver() != nil {
		t.Fatal("nil Assistant app returned a role resolver")
	}
	app := NewApp(0, "", "", nil)
	if app.GroupRoleResolver() != nil {
		t.Fatal("stopped Assistant exposed contextual group authorization")
	}
}
