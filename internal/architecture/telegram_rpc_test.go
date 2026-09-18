package architecture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTelegramRPCIngressGuards protects the production wiring points that have
// historically bypassed RPCExecutor. Low-level calls inside the Telegram
// service/resolver are permitted because their enclosing operation supplies the
// explicit RPC metadata; auxiliary runtimes must receive a managed API instead.
func TestTelegramRPCIngressGuards(t *testing.T) {
	root := repositoryRoot(t)

	servicePath := filepath.Join(root, "internal", "telegram", "service.go")
	resolverPath := filepath.Join(root, "internal", "telegram", "resolver.go")
	assertFileExcludes(t, servicePath, "retryOnFloodWait")
	assertFileExcludes(t, resolverPath, "RetryRPC(")
	for _, forbidden := range []string{"ResolveUserID(", "ResolveChannelID("} {
		assertFileExcludes(t, resolverPath, forbidden)
		assertFileExcludes(t, servicePath, forbidden)
	}
	serviceData, err := os.ReadFile(servicePath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(serviceData), "func NewServiceWithExecutor(") {
		t.Error("Telegram service must expose shared-executor construction")
	}
	if !strings.Contains(string(serviceData), "telegram RPC executor is not configured") {
		t.Error("Telegram service must fail closed when executor wiring is absent")
	}
	getExecutorStart := strings.Index(string(serviceData), "func (s *Service) getExecutor() *RPCExecutor")
	if getExecutorStart >= 0 {
		getExecutorTail := string(serviceData)[getExecutorStart:]
		if end := strings.Index(getExecutorTail, "\n}\n"); end >= 0 {
			getExecutorTail = getExecutorTail[:end+3]
		}
		if strings.Contains(getExecutorTail, "NewRPCExecutor(") {
			t.Error("getExecutor must not construct a second RPC policy on demand")
		}
	}
	for _, forbidden := range []string{"uploader.NewUploader(api)", ".Download(s.api,", "execMediaTransfer("} {
		if strings.Contains(string(serviceData), forbidden) {
			t.Errorf("%s reintroduced logical-only media RPC boundary %q", servicePath, forbidden)
		}
	}
	mediaRPCPath := filepath.Join(root, "internal", "telegram", "media_rpc.go")
	mediaData, err := os.ReadFile(mediaRPCPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{"managedUploadRPCClient", "managedDownloadRPCClient", "executePhysicalMediaRPC"} {
		if !strings.Contains(string(mediaData), required) {
			t.Errorf("%s is missing physical media RPC invariant %q", mediaRPCPath, required)
		}
	}

	assistantClient := filepath.Join(root, "internal", "assistant", "client", "client.go")
	clientData, err := os.ReadFile(assistantClient)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(clientData), "managedAPI := &managedAPI{") {
		t.Error("assistant client must construct the managed Telegram API before wiring handlers")
	}
	telegramClientPath := filepath.Join(root, "internal", "telegram", "client.go")
	telegramClientData, err := os.ReadFile(telegramClientPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(telegramClientData), "go checkRestartState(") {
		t.Errorf("%s reintroduced an unowned restart notification goroutine", telegramClientPath)
	}
	if !strings.Contains(string(telegramClientData), "func (c *Client) NotifyRestartState(") {
		t.Errorf("%s must expose restart notification as lifecycle-owned work", telegramClientPath)
	}
	appPath := filepath.Join(root, "internal", "app", "app.go")
	appData, err := os.ReadFile(appPath)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(appData), `Name:    "telegram-restart-notification"`) {
		t.Errorf("%s must supervise Telegram restart notification work", appPath)
	}

	for _, forbidden := range []string{
		"NewClientInteraction(tdClient.API()",
		"NewTelegramEntityFetcher(tdClient.API()",
		"newAssistantInlineQueryServicer(tdClient.API()",
		"RegisterTelegramCommandMenu(ctx, tdClient.API()",
	} {
		assertFileExcludes(t, assistantClient, forbidden)
	}

	managed := filepath.Join(root, "internal", "assistant", "client", "managed_api.go")
	data, err := os.ReadFile(managed)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"assistentrpc.ReadOnly",
		"assistentrpc.IdempotentMutation",
		"assistentrpc.NonIdempotentMutation",
	} {
		if !strings.Contains(string(data), required) {
			t.Errorf("managed assistant API is missing explicit operation kind %q", required)
		}
	}
}

func assertFileExcludes(t *testing.T, path, forbidden string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(data), forbidden) {
		t.Errorf("%s contains forbidden Telegram RPC bypass %q", path, forbidden)
	}
}
