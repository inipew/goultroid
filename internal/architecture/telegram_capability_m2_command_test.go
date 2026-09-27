package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestM2ProductionCommandPathUsesCapabilityBundle(t *testing.T) {
	root := repositoryRoot(t)

	read := func(parts ...string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(append([]string{root}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	wiring := read("internal", "app", "wiring_services.go")
	if strings.Contains(wiring, ".CommandService()") {
		t.Fatal("production service wiring still depends on Dispatcher.CommandService aggregate")
	}
	if strings.Contains(wiring, "corepkg.CommandTelegramServicer") {
		t.Fatal("scheduled command wiring still exposes CommandTelegramServicer")
	}

	executor := read("internal", "core", "executor.go")
	if strings.Contains(executor, "ExecuteExecution(exec CommandExecution, cmd Command, svc CommandTelegramServicer)") {
		t.Fatal("CommandExecutor.ExecuteExecution still accepts CommandTelegramServicer")
	}
	if !strings.Contains(executor, "ExecuteExecution(exec CommandExecution, cmd Command, telegram TelegramCapabilities)") {
		t.Fatal("CommandExecutor.ExecuteExecution does not accept TelegramCapabilities")
	}

	dispatcher := read("internal", "telegram", "dispatcher_boundaries.go")
	if !strings.Contains(dispatcher, "command            core.TelegramCapabilities") {
		t.Fatal("dispatcher command storage is not capability-sized")
	}

	client := read("internal", "telegram", "client.go")
	if strings.Contains(client, "dispatcher.CommandService().(core.TelegramServicer)") {
		t.Fatal("Client.Service compatibility path still asserts from production command capabilities")
	}
}
