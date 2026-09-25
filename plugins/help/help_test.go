package help

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
)

type mockService struct {
	core.MockTelegramServicer
	sent        string
	edited      string
	lastMarkup  tg.ReplyMarkupClass
	deleteCalls int
}

type fakeHelpRenderer struct {
	calls   int
	request selfinline.Request
	err     error
}

func (f *fakeHelpRenderer) Render(_ context.Context, request selfinline.Request) (selfinline.Result, error) {
	f.calls++
	f.request = request
	return selfinline.Result{QueryID: 1, ResultID: "assistant_help", RandomID: 2}, f.err
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	m.lastMarkup = nil
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	m.lastMarkup = nil
	return nil
}
func (m *mockService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.edited = text
	m.lastMarkup = markup
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	m.deleteCalls++
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
	return &tg.Message{ID: 100}, nil
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

func TestHelpPlugin(t *testing.T) {
	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Description: "Check latency",
		Category:    "Utility",
	})
	_ = router.Register(core.Command{
		Name:        "ban",
		Description: "Ban user",
		Category:    "Admin",
		Permission:  core.PermissionSudo,
	})

	p := New(router)
	if p.Name() != "help" {
		t.Errorf("expected plugin name help, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}

	svc := &mockService{}
	baseCtx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionAssistant,
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Help without args -> compact category overview (bold category names, command list)
	ctxAll := *baseCtx
	ctxAll.Command = "help"
	if err := cmds[0].Handler(&ctxAll); err != nil {
		t.Fatalf("unexpected error running help all: %v", err)
	}

	if !strings.Contains(svc.edited, "Admin") || !strings.Contains(svc.edited, "Utility") {
		t.Errorf("expected help output to contain Admin and Utility categories, got: %s", svc.edited)
	}
	// Compact view: command names listed inline, no expandable blockquotes in summary
	if !strings.Contains(svc.edited, ".ban") || !strings.Contains(svc.edited, ".ping") {
		t.Errorf("expected help overview to list command names, got: %s", svc.edited)
	}

	// 2. Help for existing command
	ctxTarget := *baseCtx
	ctxTarget.Command = "help"
	ctxTarget.Args = []string{"ping"}
	if err := cmds[0].Handler(&ctxTarget); err != nil {
		t.Fatalf("unexpected error running help ping: %v", err)
	}

	if !strings.Contains(svc.edited, "Command: .ping") || !strings.Contains(svc.edited, "Check latency") {
		t.Errorf("expected help target to show ping details, got: %s", svc.edited)
	}

	// 3. Help for module/category
	ctxCat := *baseCtx
	ctxCat.Command = "help"
	ctxCat.Args = []string{"admin"}
	if err := cmds[0].Handler(&ctxCat); err != nil {
		t.Fatalf("unexpected error running help admin: %v", err)
	}

	if !strings.Contains(svc.edited, "Module: Admin") || !strings.Contains(svc.edited, ".ban") {
		t.Errorf("expected module help to show admin commands, got: %s", svc.edited)
	}

	// 4. Help for non-existent command/module
	ctxUnknown := *baseCtx
	ctxUnknown.Command = "help"
	ctxUnknown.Args = []string{"nonexistent"}
	if err := cmds[0].Handler(&ctxUnknown); err != nil {
		t.Fatalf("unexpected error running help nonexistent: %v", err)
	}

	if !strings.Contains(svc.edited, "not found") {
		t.Errorf("expected not found message, got: %s", svc.edited)
	}
}

func TestGroupAuthorizationLabel(t *testing.T) {
	label := groupAuthorizationLabel(core.GroupAuthorizationRequirement{
		Level: core.GroupAuthorizationAdministrator,
		Rights: core.GroupAdminRights{
			BanUsers:       true,
			DeleteMessages: true,
		},
	})
	if !strings.Contains(label, "administrator") ||
		!strings.Contains(label, "ban_users") ||
		!strings.Contains(label, "delete_messages") {
		t.Fatalf("unexpected group authorization label: %q", label)
	}
}

func TestHelpUserbotCommandUsesSelfInlineAssistantPresentation(t *testing.T) {
	router := core.NewRouter(".")
	p := New(router)
	renderer := &fakeHelpRenderer{}
	p.SetSelfInlineRenderer(renderer)
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		Source: core.ExecutionInteractive,
		Args:   []string{".ping"},
		Message: &core.Message{
			ID:        77,
			ReplyToID: 11,
			TopicID:   9,
		},
		Svc:    svc,
		PeerID: &tg.InputPeerChat{ChatID: 123},
	}
	if err := p.handleHelp(ctx); err != nil {
		t.Fatalf("handleHelp() error=%v", err)
	}
	if renderer.calls != 1 {
		t.Fatalf("renderer calls=%d, want 1", renderer.calls)
	}
	if renderer.request.Query != "help ping" {
		t.Fatalf("renderer query=%q, want %q", renderer.request.Query, "help ping")
	}
	if renderer.request.ReplyToID != 11 || renderer.request.TopicID != 9 {
		t.Fatalf("renderer reply/topic=%d/%d", renderer.request.ReplyToID, renderer.request.TopicID)
	}
	if svc.edited != "" || svc.lastMarkup != nil {
		t.Fatalf("userbot help fell back to native edit/markup after successful self-inline render: edited=%q markup=%T", svc.edited, svc.lastMarkup)
	}
	if svc.deleteCalls != 1 {
		t.Fatalf("successful self-inline help delete calls=%d, want 1", svc.deleteCalls)
	}
	if got := p.Capabilities()[0].Surfaces; got != execution.SurfaceUserbot|execution.SurfaceAssistant {
		t.Fatalf("help surfaces=%v", got)
	}
}

func TestHelpUserbotFallsBackToNativeWithoutRenderer(t *testing.T) {
	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name:        "ping",
		Aliases:     []string{"p"},
		Description: "Check latency",
		Category:    "Utility",
	})
	_ = router.Register(core.Command{
		Name:        "assistantonly",
		Description: "Assistant-only command",
		Category:    "Utility",
		Surfaces:    execution.SurfaceAssistant,
	})
	p := New(router)

	tests := []struct {
		name      string
		args      []string
		want      []string
		doNotWant []string
	}{
		{
			name:      "overview",
			want:      []string{"GoUltroid Help", ".ping", "Utility"},
			doNotWant: []string{"assistantonly", "Assistant inline renderer"},
		},
		{
			name:      "module",
			args:      []string{"utility"},
			want:      []string{"Module: Utility", ".ping"},
			doNotWant: []string{"assistantonly"},
		},
		{
			name: "command",
			args: []string{"ping"},
			want: []string{"Command: .ping", "Check latency", ".p"},
		},
		{
			name: "alias",
			args: []string{"p"},
			want: []string{"Command: .ping", "Check latency"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			svc := &mockService{}
			ctx := userbotHelpContext(svc, tc.args...)
			if err := p.handleHelp(ctx); err != nil {
				t.Fatalf("handleHelp() error=%v", err)
			}
			for _, want := range tc.want {
				if !strings.Contains(svc.edited, want) {
					t.Fatalf("native help output=%q, want %q", svc.edited, want)
				}
			}
			for _, forbidden := range tc.doNotWant {
				if strings.Contains(svc.edited, forbidden) {
					t.Fatalf("native help output=%q unexpectedly contains %q", svc.edited, forbidden)
				}
			}
			if svc.deleteCalls != 0 {
				t.Fatalf("native fallback deleted command, calls=%d", svc.deleteCalls)
			}
		})
	}
}

func TestHelpUserbotFallsBackAfterSafeSelfInlineFailure(t *testing.T) {
	tests := []struct {
		name  string
		stage selfinline.RenderStage
		err   error
	}{
		{name: "preflight inline disabled", stage: selfinline.RenderStagePreflight, err: selfinline.ErrInlineDisabled},
		{name: "query failure", stage: selfinline.RenderStageQuery, err: selfinline.ErrQueryFailed},
		{name: "selection failure", stage: selfinline.RenderStageSelect, err: selfinline.ErrNoResults},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			router := core.NewRouter(".")
			_ = router.Register(core.Command{
				Name:        "ping",
				Description: "Check latency",
				Category:    "Utility",
			})
			p := New(router)
			p.SetSelfInlineRenderer(&fakeHelpRenderer{err: &selfinline.RenderFailure{
				Stage: tc.stage,
				Err:   tc.err,
			}})
			svc := &mockService{}
			ctx := userbotHelpContext(svc, "ping")

			if err := p.handleHelp(ctx); err != nil {
				t.Fatalf("handleHelp() error=%v", err)
			}
			if !strings.Contains(svc.edited, "Command: .ping") {
				t.Fatalf("safe self-inline failure did not fall back to native help: %q", svc.edited)
			}
			if svc.deleteCalls != 0 {
				t.Fatalf("safe fallback deleted command, calls=%d", svc.deleteCalls)
			}
		})
	}
}

func TestHelpUserbotSendStageFailureDoesNotEmitNativeDuplicate(t *testing.T) {
	router := core.NewRouter(".")
	_ = router.Register(core.Command{
		Name:        "ping",
		Description: "Check latency",
		Category:    "Utility",
	})
	p := New(router)
	p.SetSelfInlineRenderer(&fakeHelpRenderer{err: &selfinline.RenderFailure{
		Stage:            selfinline.RenderStageSend,
		MayHaveCommitted: true,
		Err:              selfinline.ErrSendFailed,
	}})
	svc := &mockService{}
	ctx := userbotHelpContext(svc, "ping")

	if err := p.handleHelp(ctx); err != nil {
		t.Fatalf("handleHelp() error=%v", err)
	}
	if strings.Contains(svc.edited, "Command: .ping") || strings.Contains(svc.sent, "Command: .ping") {
		t.Fatalf("send-stage failure emitted native duplicate: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if !strings.Contains(svc.edited, "could not be confirmed") && !strings.Contains(svc.sent, "could not be confirmed") {
		t.Fatalf("send-stage failure missing concise diagnostic: edited=%q sent=%q", svc.edited, svc.sent)
	}
	if svc.deleteCalls != 0 {
		t.Fatalf("ambiguous send deleted command, calls=%d", svc.deleteCalls)
	}
}

func userbotHelpContext(svc *mockService, args ...string) *core.Context {
	return &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionInteractive,
		Args:    args,
		Message: &core.Message{ID: 77, IsOutgoing: true},
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: 123},
	}
}
