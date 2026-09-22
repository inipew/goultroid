package admin

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
)

func TestP7GAssistantMutationMetadataMatchesCanonicalRights(t *testing.T) {
	actions := map[string]core.GroupMutationAction{
		"ban":     core.GroupMutationBan,
		"unban":   core.GroupMutationUnban,
		"kick":    core.GroupMutationKick,
		"mute":    core.GroupMutationMute,
		"unmute":  core.GroupMutationUnmute,
		"purge":   core.GroupMutationPurge,
		"promote": core.GroupMutationPromote,
		"demote":  core.GroupMutationDemote,
	}

	commands := make(map[string]core.Command)
	for _, command := range New().Commands() {
		commands[command.Name] = command
	}

	for name, action := range actions {
		command, ok := commands[name]
		if !ok {
			t.Fatalf("P7-G command %q is missing", name)
		}
		if command.Permission != core.PermissionSudo {
			t.Fatalf("%s Userbot permission=%s, want Sudo", name, command.Permission)
		}
		if got := command.EffectivePermission(core.ExecutionAssistant); got != core.PermissionEveryone {
			t.Fatalf("%s Assistant permission=%s, want Everyone", name, got)
		}
		if !command.GroupOnly {
			t.Fatalf("%s must remain GroupOnly", name)
		}

		want, err := core.GroupMutationRequirement(action)
		if err != nil {
			t.Fatal(err)
		}
		if command.GroupAuthorization != want {
			t.Fatalf("%s authorization=%+v, want %+v", name, command.GroupAuthorization, want)
		}
	}
}

func TestP7IWarningWorkflowUsesAssistantContextualAuthorization(t *testing.T) {
	commands := make(map[string]core.Command)
	for _, command := range New().Commands() {
		commands[command.Name] = command
	}

	warn := commands["warn"]
	if warn.Permission != core.PermissionSudo {
		t.Fatalf("warn Userbot permission=%s, want Sudo", warn.Permission)
	}
	if got := warn.EffectivePermission(core.ExecutionAssistant); got != core.PermissionEveryone {
		t.Fatalf("warn Assistant permission=%s, want Everyone", got)
	}
	wantWarn, err := core.GroupMutationRequirement(core.GroupMutationMute)
	if err != nil {
		t.Fatal(err)
	}
	if warn.GroupAuthorization != wantWarn {
		t.Fatalf("warn authorization=%+v, want %+v", warn.GroupAuthorization, wantWarn)
	}

	for _, name := range []string{"warns", "resetwarns"} {
		command := commands[name]
		if command.Permission != core.PermissionSudo {
			t.Fatalf("%s Userbot permission=%s, want Sudo", name, command.Permission)
		}
		if got := command.EffectivePermission(core.ExecutionAssistant); got != core.PermissionEveryone {
			t.Fatalf("%s Assistant permission=%s, want Everyone", name, got)
		}
		want := core.GroupAuthorizationRequirement{Level: core.GroupAuthorizationAdministrator}
		if command.GroupAuthorization != want {
			t.Fatalf("%s authorization=%+v, want %+v", name, command.GroupAuthorization, want)
		}
	}
}
