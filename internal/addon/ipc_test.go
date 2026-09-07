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
