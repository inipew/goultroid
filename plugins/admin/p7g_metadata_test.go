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

func TestP7GDoesNotImplicitlyOpenWarningWorkflow(t *testing.T) {
	commands := make(map[string]core.Command)
	for _, command := range New().Commands() {
		commands[command.Name] = command
	}

	for _, name := range []string{"warn", "warns", "resetwarns"} {
		command := commands[name]
		if command.AssistantPermission != nil {
			t.Fatalf("%s unexpectedly received Assistant permission override", name)
		}
		if command.GroupAuthorization.Required() {
			t.Fatalf("%s unexpectedly entered P7-G mutation contract: %+v", name, command.GroupAuthorization)
		}
	}
}
