package architecture

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestT11AssistantUsesSharedRPCAndPhysicalMediaBoundary(t *testing.T) {
	root := repositoryRoot(t)

	wiringPath := filepath.Join(root, "internal", "app", "wiring_telegram.go")
	wiring, err := os.ReadFile(wiringPath)
	if err != nil {
		t.Fatal(err)
	}
	wiringSource := string(wiring)
	if !strings.Contains(wiringSource, "app.SetRPCExecutor(assistantRPCExecutor{executor: client.Executor()})") {
		t.Fatalf("%s must install the application-owned Telegram RPC executor on Assistant", wiringPath)
	}
	if strings.Contains(wiringSource, "DirectExecutor") {
		t.Fatalf("%s must not wire the Assistant compatibility DirectExecutor in production", wiringPath)
	}

	clientPath := filepath.Join(root, "internal", "assistant", "client", "client.go")
	client, err := os.ReadFile(clientPath)
	if err != nil {
		t.Fatal(err)
	}
	clientSource := string(client)
	for _, required := range []string{
		"managedAPI := &managedAPI{raw: tdClient.API(), executor: c.rpcExecutor}",
		"c.interaction.SetRPCExecutor(c.rpcExecutor)",
		"c.interaction.SetManagedMediaSender(message.NewSender(tdClient.API()), tdClient.API())",
	} {
		if !strings.Contains(clientSource, required) {
			t.Fatalf("%s is missing shared Assistant RPC/media boundary %q", clientPath, required)
		}
	}

	interactionPath := filepath.Join(root, "internal", "assistant", "interaction", "message.go")
	interaction, err := os.ReadFile(interactionPath)
	if err != nil {
		t.Fatal(err)
	}
	interactionSource := string(interaction)
	if strings.Contains(interactionSource, `executeValue(ctx, c.executor, "upload.saveFilePart"`) {
		t.Fatalf("%s reintroduced a whole-transfer saveFilePart executor wrapper", interactionPath)
	}
	if got := strings.Count(interactionSource, "c.uploadMediaFile(ctx, filePath)"); got < 3 {
		t.Fatalf("%s managed upload helper call count=%d, want at least 3 upload paths", interactionPath, got)
	}

	mediaRPCPath := filepath.Join(root, "internal", "assistant", "interaction", "media_rpc.go")
	mediaRPC, err := os.ReadFile(mediaRPCPath)
	if err != nil {
		t.Fatal(err)
	}
	mediaRPCSource := string(mediaRPC)
	for _, required := range []string{
		"newManagedMediaUploader",
		"UploadSaveFilePart",
		"UploadSaveBigFilePart",
		"assistentrpc.IdempotentMutation",
	} {
		if !strings.Contains(mediaRPCSource, required) {
			t.Fatalf("%s is missing physical upload RPC invariant %q", mediaRPCPath, required)
		}
	}
}

func TestT11RetiredCallbackHelpersDoNotReenterProduction(t *testing.T) {
	root := repositoryRoot(t)
	forbidden := []string{
		"internal/services/callback",
		"callback.EncodeCallbackData(",
		"SetPluginScopeResolver(",
		"resolvePluginScope(",
		"AcceptClaim(",
	}

	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			switch entry.Name() {
			case ".git", "vendor":
				return filepath.SkipDir
			default:
				return nil
			}
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		raw, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source := string(raw)
		for _, legacy := range forbidden {
			if strings.Contains(source, legacy) {
				t.Errorf("retired callback/runtime helper %q re-entered production source %s", legacy, path)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
