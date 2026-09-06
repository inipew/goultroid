package system

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent         string
	edited       string
	mediaSent    bool
	mediaType    string
	mediaCaption string
	mediaPath    string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 100, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return nil, nil
}
func (m *mockService) PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error {
	return nil
}
func (m *mockService) UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error {
	return nil
}
func (m *mockService) ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error {
	return nil
}
func (m *mockService) BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error {
	return nil
}
func (m *mockService) UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error {
	return nil
}
func (m *mockService) PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error) {
	return 0, nil
}
func (m *mockService) SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error) {
	m.mediaSent = true
	m.mediaType = mediaType
	m.mediaPath = filePath
	m.mediaCaption = caption
	return &tg.Message{ID: 200}, nil
}
func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return nil, nil
}
func (m *mockService) ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error) {
	return nil, nil
}
func (m *mockService) GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error) {
	return nil, nil
}

func TestSystemPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "system" {
		t.Errorf("expected name system, got %s", p.Name())
	}
	if p.Description() == "" {
		t.Errorf("expected non-empty description")
	}
	if err := p.Init(); err != nil {
		t.Errorf("Init failed: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 4 {
		t.Fatalf("expected 4 commands, got %d", len(cmds))
	}

	for _, c := range cmds {
		if c.Permission != core.PermissionOwner {
			t.Errorf("expected command %s to have PermissionOwner permission", c.Name)
		}
	}
}

func TestExec_EmptyArgs(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec"},
		Args:    nil,
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.edited, "Usage:") {
		t.Errorf("expected usage message, got: %s", svc.edited)
	}
}

func TestExec_ShortOutput(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec echo Hello GoUltroid"},
		Args:    []string{"echo", "Hello", "GoUltroid"},
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.edited, "Hello GoUltroid") {
		t.Errorf("expected 'Hello GoUltroid' in output, got: %s", svc.edited)
	}
	if !strings.Contains(svc.edited, "Shell Execution") {
		t.Errorf("expected header 'Shell Execution', got: %s", svc.edited)
	}
}

func TestExec_LongOutput(t *testing.T) {
	p := New()
	svc := &mockService{}
	// Generates 4000 chars
	cmdStr := "head -c 4000 < /dev/zero | tr '\\0' 'A'"
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".exec " + cmdStr},
		Args:    []string{"head", "-c", "4000", "<", "/dev/zero", "|", "tr", "'\\0'", "'A'"},
		Svc:     svc,
	}

	err := p.handleExec(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !svc.mediaSent {
		t.Errorf("expected long output to be uploaded as file")
	}
	if svc.mediaType != "file" {
		t.Errorf("expected mediaType file, got %s", svc.mediaType)
	}
}

func TestRestart_CustomHandler(t *testing.T) {
	var calledState RestartState
	var called bool

	p := NewWithCustomRestart("", func(state RestartState) error {
		called = true
		calledState = state
		return nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChannel{ChannelID: 777, AccessHash: 888},
		Message: &core.Message{ID: 42, Text: ".restart"},
		Svc:     svc,
	}

	err := p.handleRestart(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !called {
		t.Errorf("expected custom restart handler to be called")
	}
	// With edit-in-place UX, restart edits the trigger message (ID 42) in-place.
	// MsgID in RestartState should be the trigger message ID, not a new reply ID.
	if calledState.PeerType != "channel" || calledState.ChatID != 777 || !calledState.IsChannel || calledState.AccessHash != 888 || calledState.MsgID != 42 {
		t.Errorf("unexpected restart state captured: %+v", calledState)
	}
	if !strings.Contains(svc.edited, "Restarting GoUltroid") {
		t.Errorf("expected restart response via edit, got: %s", svc.edited)
	}

	// Test with InputPeerSelf
	called = false
	ctxSelf := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 55, Text: ".restart"},
		Svc:     svc,
	}
	if err := p.handleRestart(ctxSelf); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called || calledState.PeerType != "self" {
		t.Errorf("expected peer_type self, got: %+v", calledState)
	}

	// Test with InputPeerUser
	called = false
	ctxUser := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerUser{UserID: 12345, AccessHash: 67890},
		Message: &core.Message{ID: 66, Text: ".restart"},
		Svc:     svc,
	}
	if err := p.handleRestart(ctxUser); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called || calledState.PeerType != "user" || calledState.ChatID != 12345 || calledState.AccessHash != 67890 {
		t.Errorf("expected peer_type user with hash, got: %+v", calledState)
	}
}

func TestUpdate_UpToDate(t *testing.T) {
	p := New()
	p.SetCmdRunner(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 {
			switch args[0] {
			case "fetch":
				return []byte(""), nil
			case "rev-parse":
				return []byte("abcdef1"), nil
			case "log":
				return []byte(""), nil // no pending commits
			}
		}
		return []byte(""), nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".update"},
		Args:    nil,
		Svc:     svc,
	}

	err := p.handleUpdate(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.edited, "already up to date") || !strings.Contains(svc.edited, "abcdef1") {
		t.Errorf("expected up to date message with commit hash, got: %s", svc.edited)
	}
}

func TestUpdate_HasUpdates(t *testing.T) {
	p := New()
	p.SetCmdRunner(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 {
			switch args[0] {
			case "fetch":
				return []byte(""), nil
			case "rev-parse":
				return []byte("abcdef1"), nil
			case "log":
				return []byte("1234567 fix: some bug\n89abcde feat: new feature"), nil
			}
		}
		return []byte(""), nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".update"},
		Args:    nil,
		Svc:     svc,
	}

	err := p.handleUpdate(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.edited, "New updates available") || !strings.Contains(svc.edited, "Pending Commits (2)") || !strings.Contains(svc.edited, "fix: some bug") {
		t.Errorf("expected new updates message with commits, got: %s", svc.edited)
	}
}

func TestUpdate_PullAndRebuild(t *testing.T) {
	var executedCommands []string
	var restartCalled bool

	p := NewWithCustomRestart("", func(state RestartState) error {
		restartCalled = true
		return nil
	})

	p.SetCmdRunner(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		executedCommands = append(executedCommands, name+" "+strings.Join(args, " "))
		if name == "git" && len(args) > 0 && args[0] == "status" {
			return []byte(""), nil // clean tree
		}
		if name == "go" && len(args) > 2 && args[0] == "build" {
			tmpBin := args[2]
			_ = os.MkdirAll(filepath.Dir(tmpBin), 0755)
			_ = os.WriteFile(tmpBin, []byte("binary"), 0755)
		}
		return []byte("ok"), nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".update pull"},
		Args:    []string{"pull"},
		Svc:     svc,
	}

	err := p.handleUpdate(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !restartCalled {
		t.Errorf("expected restart to be called after pull and rebuild")
	}

	expectedGitPull := "git pull --ff-only"
	expectedBuild := "go build -o bin/goultroid.tmp ./cmd/goultroid"
	hasPull := false
	hasBuild := false
	for _, c := range executedCommands {
		if c == expectedGitPull {
			hasPull = true
		}
		if c == expectedBuild {
			hasBuild = true
		}
	}
	if !hasPull || !hasBuild {
		t.Errorf("expected git pull and go build commands executed, got: %v", executedCommands)
	}
}

func TestUpdate_DirtyWorkingTree(t *testing.T) {
	p := New()
	p.SetCmdRunner(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "git" && len(args) > 0 && args[0] == "status" {
			return []byte(" M internal/core/router.go"), nil // dirty working tree
		}
		return []byte(""), nil
	})

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".update pull"},
		Args:    []string{"pull"},
		Svc:     svc,
	}

	err := p.handleUpdate(ctx)
	if err == nil {
		t.Fatalf("expected error on dirty working tree, got nil")
	}

	if !strings.Contains(svc.edited, "working directory has uncommitted modifications") {
		t.Errorf("expected dirty tree warning message, got: %s", svc.edited)
	}
}

func TestBuildSanitizedEnv(t *testing.T) {
	// Set dummy sensitive environment variables
	t.Setenv("TG_SESSION", "super_secret_session_token_123")
	t.Setenv("BOT_TOKEN", "123456:ABC-DEF1234ghIkl-zyx57W2v1u123ew11")
	t.Setenv("API_HASH", "0123456789abcdef0123456789abcdef")
	t.Setenv("NORMAL_VAR", "harmless_value")

	env := buildSanitizedEnv()

	var hasNormal, hasRedactedSession, hasRedactedToken, hasRedactedHash bool
	for _, e := range env {
		if e == "NORMAL_VAR=harmless_value" {
			hasNormal = true
		}
		if e == "TG_SESSION=[REDACTED]" {
			hasRedactedSession = true
		}
		if e == "BOT_TOKEN=[REDACTED]" {
			hasRedactedToken = true
		}
		if e == "API_HASH=[REDACTED]" {
			hasRedactedHash = true
		}
		if strings.Contains(e, "super_secret_session_token_123") {
			t.Errorf("found unredacted secret session in env: %s", e)
		}
	}

	if !hasNormal {
		t.Errorf("expected NORMAL_VAR to be preserved")
	}
	if !hasRedactedSession {
		t.Errorf("expected TG_SESSION to be redacted")
	}
	if !hasRedactedToken {
		t.Errorf("expected BOT_TOKEN to be redacted")
	}
	if !hasRedactedHash {
		t.Errorf("expected API_HASH to be redacted")
	}
}

func TestHealth_WithMetrics(t *testing.T) {
	p := New()
	metrics := core.NewDefaultMetricsTracker()
	metrics.RecordCommand("ping", 15*time.Millisecond, nil)
	metrics.RecordSchedulerJob(1, "remind", 50*time.Millisecond, nil)
	p.SetMetrics(metrics)

	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1},
		Svc:     svc,
	}

	err := p.handleHealth(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.edited, "Operational Telemetry") {
		t.Errorf("expected Operational Telemetry section in output, got: %s", svc.edited)
	}
	if !strings.Contains(svc.edited, "Commands:") {
		t.Errorf("expected Commands count in output, got: %s", svc.edited)
	}
}
