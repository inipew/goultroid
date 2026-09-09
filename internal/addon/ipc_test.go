package addon

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestExternalRuntimeStartHandshakeDoesNotDeadlock(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "addon.sh")
	content := `#!/bin/sh
while IFS= read -r line; do
    case "$line" in
        *'"method":"hello"'*)
            id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
            printf '{"id":"%s","ok":true,"result":{"protocol":1}}\n' "$id"
            ;;
    esac
done
`
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatalf("write addon fixture: %v", err)
	}

	manifest := Manifest{
		Name:         "runtime-test",
		Version:      "1.0.0",
		Commands:     []string{"test"},
		Capabilities: []Capability{CapTelegramRead},
	}
	runtime := NewExternalRuntime(manifest, script, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := runtime.Start(ctx); err != nil {
		t.Fatalf("runtime start/handshake failed: %v", err)
	}
	if !runtime.Running() {
		t.Fatal("runtime should be running after successful handshake")
	}
	if err := runtime.Stop(); err != nil {
		t.Fatalf("runtime stop failed: %v", err)
	}
}

func TestExternalRuntimeDetectsUnexpectedProcessExit(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "exit-addon.sh")
	content := `#!/bin/sh
IFS= read -r line
id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
printf '{"id":"%s","ok":true,"result":{"protocol":1}}\n' "$id"
exit 0
`
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatalf("write addon fixture: %v", err)
	}
	runtime := NewExternalRuntime(Manifest{Name: "exiting", Version: "1.0.0"}, script, nil)
	if err := runtime.Start(context.Background()); err != nil {
		t.Fatalf("Start() error = %v", err)
	}

	deadline := time.Now().Add(time.Second)
	for runtime.Running() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if runtime.Running() {
		t.Fatal("runtime still reports running after child process exited")
	}
	if _, err := runtime.Call(context.Background(), "test", nil); err == nil {
		t.Fatal("Call() succeeded after child process exited")
	}
}

func TestExternalRuntimeLifetimeOutlivesStartupContext(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "long-lived-addon.sh")
	content := `#!/bin/sh
while IFS= read -r line; do
    id=$(printf '%s\n' "$line" | sed -n 's/.*"id":"\([^"]*\)".*/\1/p')
    printf '{"id":"%s","ok":true,"result":{"protocol":1}}\n' "$id"
done
`
	if err := os.WriteFile(script, []byte(content), 0700); err != nil {
		t.Fatal(err)
	}
	startupCtx, cancel := context.WithCancel(context.Background())
	runtime := NewExternalRuntime(Manifest{Name: "long-lived", Version: "1.0.0"}, script, nil)
	if err := runtime.Start(startupCtx); err != nil {
		t.Fatal(err)
	}
	cancel()
	time.Sleep(20 * time.Millisecond)
	if !runtime.Running() {
		t.Fatal("startup operation context terminated addon lifetime")
	}
	if err := runtime.Stop(); err != nil {
		t.Fatal(err)
	}
}
