package presentation_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui"
)

type testBuilder struct {
	key presentation.ScreenKey
	fn  func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error)
}

func (b *testBuilder) Key() presentation.ScreenKey { return b.key }
func (b *testBuilder) Build(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
	if b.fn != nil {
		return b.fn(ctx, req)
	}
	s := ui.NewScreen("test_screen", "Title", "Body")
	return presentation.BuildResult{Screen: s}, nil
}

func TestRegistry_RegisterAndResolve(t *testing.T) {
	reg := presentation.NewRegistry()
	key := presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1}

	builder := &testBuilder{key: key}
	lease, err := reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: 1,
		Builder:    builder,
		Policy:     presentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("unexpected register error: %v", err)
	}

	found, ok := reg.Resolve(key)
	if !ok {
		t.Fatalf("expected key %s to be resolved", key)
	}
	if found.Owner != "core" || found.Generation != 1 {
		t.Errorf("unexpected registration: %+v", found)
	}

	// Close lease -> unregisters
	lease.Close()
	_, ok = reg.Resolve(key)
	if ok {
		t.Fatalf("expected key to be unregistered after lease close")
	}
}

func TestRegistry_StaleLeaseDoesNotUnregisterNewerGeneration(t *testing.T) {
	reg := presentation.NewRegistry()
	key := presentation.ScreenKey{Namespace: "core", Name: "help", Version: 1}

	builder1 := &testBuilder{key: key}
	lease1, err := reg.Register(presentation.Registration{
		Owner:      "pluginA",
		Generation: 1,
		Builder:    builder1,
	})
	if err != nil {
		t.Fatalf("register gen 1: %v", err)
	}

	builder2 := &testBuilder{key: key}
	lease2, err := reg.Register(presentation.Registration{
		Owner:      "pluginA",
		Generation: 2,
		Builder:    builder2,
	})
	if err != nil {
		t.Fatalf("register gen 2: %v", err)
	}

	// lease1 close should NOT remove gen 2
	lease1.Close()

	found, ok := reg.Resolve(key)
	if !ok || found.Generation != 2 {
		t.Fatalf("expected gen 2 to remain registered, got ok=%v gen=%d", ok, found.Generation)
	}

	lease2.Close()
	_, ok = reg.Resolve(key)
	if ok {
		t.Fatalf("expected screen to be removed after lease2 closed")
	}
}

func TestRegistry_RevokeOwner(t *testing.T) {
	reg := presentation.NewRegistry()
	key1 := presentation.ScreenKey{Namespace: "p1", Name: "s1"}
	key2 := presentation.ScreenKey{Namespace: "p1", Name: "s2"}
	key3 := presentation.ScreenKey{Namespace: "p2", Name: "s3"}

	_, _ = reg.Register(presentation.Registration{Owner: "plugin1", Generation: 1, Builder: &testBuilder{key: key1}})
	_, _ = reg.Register(presentation.Registration{Owner: "plugin1", Generation: 2, Builder: &testBuilder{key: key2}})
	_, _ = reg.Register(presentation.Registration{Owner: "plugin2", Generation: 1, Builder: &testBuilder{key: key3}})

	revoked := reg.RevokeOwner("plugin1", 1)
	if revoked != 1 {
		t.Errorf("expected 1 revoked, got %d", revoked)
	}
	if _, ok := reg.Resolve(key1); ok {
		t.Errorf("key1 should be revoked")
	}
	if _, ok := reg.Resolve(key2); !ok {
		t.Errorf("key2 should still be registered")
	}
	if _, ok := reg.Resolve(key3); !ok {
		t.Errorf("key3 should still be registered")
	}
}

func TestService_BuildAndValidation(t *testing.T) {
	reg := presentation.NewRegistry()
	evaluator := presentation.NewEvaluator(12345, []func() []int64{func() []int64 { return []int64{67890} }}[0])
	svc := presentation.NewService(reg, evaluator)

	validKey := presentation.ScreenKey{Namespace: "test", Name: "valid"}
	_, err := reg.Register(presentation.Registration{
		Owner:      "test",
		Generation: 1,
		Builder: &testBuilder{
			key: validKey,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				s := ui.NewScreen("valid_screen", "Test Screen", "Hello World")
				s.AddRow(ui.NewCallbackButton("Click", []byte("test:act:1")))
				return presentation.BuildResult{Screen: s}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("register error: %v", err)
	}

	// 1. Successful build
	res, err := svc.Build(context.Background(), presentation.BuildRequest{
		Key:      validKey,
		Actor:    execution.NewActor(111, 222, false, false),
		Source:   execution.SourceAssistant,
		ChatType: presentation.ChatTypePrivate,
	})
	if err != nil {
		t.Fatalf("unexpected build error: %v", err)
	}
	if res.Screen.ID != "valid_screen" {
		t.Errorf("expected screen ID valid_screen, got %s", res.Screen.ID)
	}

	// 2. Validation failure: callback data > 64 bytes
	invalidKey := presentation.ScreenKey{Namespace: "test", Name: "invalid_cb"}
	_, _ = reg.Register(presentation.Registration{
		Owner:      "test",
		Generation: 1,
		Builder: &testBuilder{
			key: invalidKey,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				s := ui.NewScreen("invalid_cb_screen", "Too long", "Data")
				longData := strings.Repeat("x", 65)
				s.AddRow(ui.NewCallbackButton("Button", []byte(longData)))
				return presentation.BuildResult{Screen: s}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})

	_, err = svc.Build(context.Background(), presentation.BuildRequest{
		Key:      invalidKey,
		Actor:    execution.NewActor(111, 222, false, false),
		Source:   execution.SourceAssistant,
		ChatType: presentation.ChatTypePrivate,
	})
	if !errors.Is(err, presentation.ErrOutputValidationFailed) {
		t.Errorf("expected ErrOutputValidationFailed for long callback data, got %v", err)
	}
}

func TestPolicy_MatrixEvaluation(t *testing.T) {
	eval := presentation.NewEvaluator(100, func() []int64 { return []int64{200} })

	tests := []struct {
		name     string
		policy   presentation.AccessPolicy
		req      presentation.PolicyRequest
		wantCode presentation.DecisionCode
	}{
		{
			name:     "Public policy allows anonymous",
			policy:   presentation.PublicPolicy(),
			req:      presentation.PolicyRequest{Actor: execution.NewActor(0, 0, false, false), Source: execution.SourceAssistant, ChatType: presentation.ChatTypePrivate},
			wantCode: presentation.DecisionAllow,
		},
		{
			name:     "Authenticated rejects zero user ID",
			policy:   presentation.AccessPolicy{Permission: presentation.PermissionAuthenticated, AllowedSources: execution.SurfaceAll},
			req:      presentation.PolicyRequest{Actor: execution.NewActor(0, 0, false, false), Source: execution.SourceAssistant},
			wantCode: presentation.DecisionDenyAuth,
		},
		{
			name:     "Owner only rejects normal user",
			policy:   presentation.OwnerOnlyPolicy(),
			req:      presentation.PolicyRequest{Actor: execution.NewActor(555, 555, false, false), Source: execution.SourceAssistant},
			wantCode: presentation.DecisionDenyPermission,
		},
		{
			name:     "Owner only accepts owner ID",
			policy:   presentation.OwnerOnlyPolicy(),
			req:      presentation.PolicyRequest{Actor: execution.NewActor(100, 100, true, false), Source: execution.SourceAssistant},
			wantCode: presentation.DecisionAllow,
		},
		{
			name:     "Sudo accepts configured sudo user",
			policy:   presentation.AccessPolicy{Permission: presentation.PermissionSudo, AllowedSources: execution.SurfaceAll},
			req:      presentation.PolicyRequest{Actor: execution.NewActor(200, 200, false, false), Source: execution.SourceAssistant},
			wantCode: presentation.DecisionAllow,
		},
		{
			name:     "Private required rejects group chat",
			policy:   presentation.AccessPolicy{Permission: presentation.PermissionPublic, AllowedSources: execution.SurfaceAll, RequirePrivate: true},
			req:      presentation.PolicyRequest{Actor: execution.NewActor(555, 555, false, false), Source: execution.SourceAssistant, ChatType: presentation.ChatTypeGroup},
			wantCode: presentation.DecisionDenyPrivate,
		},
		{
			name:     "Surface mask rejects disallowed source",
			policy:   presentation.AccessPolicy{Permission: presentation.PermissionPublic, AllowedSources: execution.SurfaceUserbot},
			req:      presentation.PolicyRequest{Actor: execution.NewActor(555, 555, false, false), Source: execution.SourceInline},
			wantCode: presentation.DecisionDenySource,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			dec := eval.Evaluate(context.Background(), tc.policy, tc.req)
			if dec.Code != tc.wantCode {
				t.Errorf("got code %s, want %s (reason: %s)", dec.Code, tc.wantCode, dec.AuditReason)
			}
		})
	}
}

type fakeDeeplinkIssuer struct {
	issuedURL string
	issuedAt  time.Time
}

func (f *fakeDeeplinkIssuer) IssueStartLink(ctx context.Context, req presentation.DeepLinkRequest) (string, time.Time, error) {
	exp := time.Now().Add(req.TTL)
	return "https://t.me/TestBot?start=mocktoken123", exp, nil
}

func TestHandoff_Modes(t *testing.T) {
	reg := presentation.NewRegistry()
	evaluator := presentation.NewEvaluator(100, nil)
	svc := presentation.NewService(reg, evaluator)
	deeplink := &fakeDeeplinkIssuer{}
	handoff := presentation.NewHandoffService(svc, deeplink)

	// Screen 1: Public, group supported
	keyNormal := presentation.ScreenKey{Namespace: "test", Name: "normal"}
	_, _ = reg.Register(presentation.Registration{
		Owner:      "test",
		Generation: 1,
		Builder: &testBuilder{
			key: keyNormal,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				s := ui.NewScreen("normal_s", "Normal", "Body")
				return presentation.BuildResult{Screen: s}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})

	// Screen 2: Private-only (sensitive)
	keyPrivate := presentation.ScreenKey{Namespace: "test", Name: "private"}
	_, _ = reg.Register(presentation.Registration{
		Owner:      "test",
		Generation: 1,
		Builder: &testBuilder{
			key: keyPrivate,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				s := ui.NewScreen("private_s", "Private", "Secret data")
				return presentation.BuildResult{Screen: s}, nil
			},
		},
		Policy: presentation.AccessPolicy{
			Permission:     presentation.PermissionPublic,
			AllowedSources: execution.SurfaceAll,
			RequirePrivate: true,
		},
	})

	ctx := context.Background()

	// 1. Normal screen in group -> Auto chooses RenderHere
	res1, err := handoff.Handoff(ctx, presentation.HandoffRequest{
		Actor:    execution.NewActor(10, -1001, false, false),
		Source:   execution.SourceUserbot,
		ChatType: presentation.ChatTypeGroup,
		Screen:   keyNormal,
	})
	if err != nil {
		t.Fatalf("handoff normal error: %v", err)
	}
	if res1.Mode != presentation.HandoffRenderHere || res1.Screen == nil {
		t.Errorf("expected HandoffRenderHere with Screen, got mode %s", res1.Mode)
	}

	// 2. Private screen in group -> Auto chooses DeepLink
	res2, err := handoff.Handoff(ctx, presentation.HandoffRequest{
		Actor:    execution.NewActor(10, -1001, false, false),
		Source:   execution.SourceUserbot,
		ChatType: presentation.ChatTypeGroup,
		Screen:   keyPrivate,
	})
	if err != nil {
		t.Fatalf("handoff private error: %v", err)
	}
	if res2.Mode != presentation.HandoffDeepLink || !strings.Contains(res2.DeepLinkURL, "https://t.me/TestBot?start=") {
		t.Errorf("expected HandoffDeepLink with URL, got %+v", res2)
	}
}

func TestRegistry_DuplicateRegistrationPolicy(t *testing.T) {
	reg := presentation.NewRegistry()
	key := presentation.ScreenKey{Namespace: "custom", Name: "panel", Version: 1}

	builder1 := &testBuilder{key: key}
	_, err := reg.Register(presentation.Registration{
		Owner:      "pluginA",
		Generation: 1,
		Builder:    builder1,
	})
	if err != nil {
		t.Fatalf("first register failed: %v", err)
	}

	// 1. Different owner -> ErrDuplicateScreenKey
	builder2 := &testBuilder{key: key}
	_, err = reg.Register(presentation.Registration{
		Owner:      "pluginB",
		Generation: 1,
		Builder:    builder2,
	})
	if !errors.Is(err, presentation.ErrDuplicateScreenKey) {
		t.Fatalf("expected ErrDuplicateScreenKey for different owner, got %v", err)
	}

	// 2. Same owner with same generation -> ErrDuplicateScreenKey
	_, err = reg.Register(presentation.Registration{
		Owner:      "pluginA",
		Generation: 1,
		Builder:    builder2,
	})
	if !errors.Is(err, presentation.ErrDuplicateScreenKey) {
		t.Fatalf("expected ErrDuplicateScreenKey for same generation, got %v", err)
	}

	// 3. Same owner with higher generation -> succeeds
	_, err = reg.Register(presentation.Registration{
		Owner:      "pluginA",
		Generation: 2,
		Builder:    builder2,
	})
	if err != nil {
		t.Fatalf("expected higher generation replacement to succeed, got %v", err)
	}
}

func TestService_Build_MenuOwnerEnforced(t *testing.T) {
	reg := presentation.NewRegistry()
	evaluator := presentation.NewEvaluator(100, nil)
	svc := presentation.NewService(reg, evaluator)

	key := presentation.ScreenKey{Namespace: "core", Name: "menu_test", Version: 1}
	_, err := reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: 1,
		Builder: &testBuilder{
			key: key,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				return presentation.BuildResult{Screen: ui.NewScreen("menu_test", "Title", "Content")}, nil
			},
		},
		Policy: presentation.AccessPolicy{
			Permission:       presentation.PermissionPublic,
			AllowedSources:   execution.SurfaceAll,
			RequireMenuOwner: true,
		},
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	ctx := context.Background()

	// 1. Caller matches MenuOwner -> Allowed
	res, err := svc.Build(ctx, presentation.BuildRequest{
		Key:       key,
		Actor:     execution.NewActor(12345, 12345, false, false),
		Source:    execution.SourceAssistant,
		ChatType:  presentation.ChatTypePrivate,
		MenuOwner: 12345,
	})
	if err != nil {
		t.Fatalf("expected allowed for menu owner, got error: %v", err)
	}
	if res.Screen == nil {
		t.Fatalf("expected screen output")
	}

	// 2. Caller does not match MenuOwner -> Denied
	_, err = svc.Build(ctx, presentation.BuildRequest{
		Key:       key,
		Actor:     execution.NewActor(99999, 99999, false, false),
		Source:    execution.SourceAssistant,
		ChatType:  presentation.ChatTypePrivate,
		MenuOwner: 12345,
	})
	if !errors.Is(err, presentation.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for non-menu-owner, got %v", err)
	}

	// 3. MenuOwner is 0 -> Fail-closed Denied even if actor is authenticated
	_, err = svc.Build(ctx, presentation.BuildRequest{
		Key:       key,
		Actor:     execution.NewActor(12345, 12345, false, false),
		Source:    execution.SourceAssistant,
		ChatType:  presentation.ChatTypePrivate,
		MenuOwner: 0,
	})
	if !errors.Is(err, presentation.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied when MenuOwner is 0, got %v", err)
	}

	// 4. Bot owner with MenuOwner == 0 -> Still fail-closed Denied
	_, err = svc.Build(ctx, presentation.BuildRequest{
		Key:       key,
		Actor:     execution.NewActor(100, 100, true, false),
		Source:    execution.SourceAssistant,
		ChatType:  presentation.ChatTypePrivate,
		MenuOwner: 0,
	})
	if !errors.Is(err, presentation.ErrAccessDenied) {
		t.Fatalf("expected ErrAccessDenied for bot owner when MenuOwner is 0, got %v", err)
	}

	// 5. Bot owner inspecting another user's valid MenuOwner -> Allowed
	resOwner, err := svc.Build(ctx, presentation.BuildRequest{
		Key:       key,
		Actor:     execution.NewActor(100, 100, true, false),
		Source:    execution.SourceAssistant,
		ChatType:  presentation.ChatTypePrivate,
		MenuOwner: 12345,
	})
	if err != nil {
		t.Fatalf("expected bot owner allowed to inspect menu session, got: %v", err)
	}
	if resOwner.Screen == nil {
		t.Fatalf("expected screen output for bot owner")
	}
}

func TestService_Build_Timeout(t *testing.T) {
	reg := presentation.NewRegistry()
	evaluator := presentation.NewEvaluator(100, nil)
	svc := presentation.NewService(reg, evaluator).WithBuildTimeout(50 * time.Millisecond)

	key := presentation.ScreenKey{Namespace: "core", Name: "slow", Version: 1}
	_, err := reg.Register(presentation.Registration{
		Owner:      "core",
		Generation: 1,
		Builder: &testBuilder{
			key: key,
			fn: func(ctx context.Context, req presentation.BuildRequest) (presentation.BuildResult, error) {
				// Ignores cancellation and sleeps longer than build timeout
				time.Sleep(200 * time.Millisecond)
				return presentation.BuildResult{Screen: ui.NewScreen("slow", "Slow", "Done")}, nil
			},
		},
		Policy: presentation.PublicPolicy(),
	})
	if err != nil {
		t.Fatalf("register failed: %v", err)
	}

	ctx := context.Background()
	_, err = svc.Build(ctx, presentation.BuildRequest{
		Key:      key,
		Actor:    execution.NewActor(1, 1, false, false),
		Source:   execution.SourceAssistant,
		ChatType: presentation.ChatTypePrivate,
	})
	if !errors.Is(err, presentation.ErrBuildTimeout) {
		t.Fatalf("expected ErrBuildTimeout, got %v", err)
	}
}

func TestHandoffResult_AsScreen(t *testing.T) {
	// 1. RenderHere mode
	origScreen := ui.NewScreen("screen1", "Original", "Body")
	res1 := presentation.HandoffResult{
		Mode:   presentation.HandoffRenderHere,
		Screen: origScreen,
	}
	s1 := res1.AsScreen("Title", "Message")
	if s1 != origScreen {
		t.Errorf("expected original screen for RenderHere, got %v", s1)
	}

	// 2. DeepLink mode
	res2 := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/Bot?start=tok123",
	}
	s2 := res2.AsScreen("Settings", "Open in bot:")
	if len(s2.Rows) != 1 || len(s2.Rows[0]) != 1 {
		t.Fatalf("expected 1 button row with 1 button for DeepLink screen")
	}
	btn := s2.Rows[0][0]
	if btn.Type != ui.ButtonURL || btn.URL != "https://t.me/Bot?start=tok123" {
		t.Errorf("unexpected button in DeepLink screen: %+v", btn)
	}

	// 3. SwitchInline mode
	res3 := presentation.HandoffResult{
		Mode:        presentation.HandoffSwitchInline,
		InlineQuery: "help",
	}
	s3 := res3.AsScreen("Search", "Tap to search:")
	if len(s3.Rows) != 1 || len(s3.Rows[0]) != 1 {
		t.Fatalf("expected 1 button row with 1 button for SwitchInline screen")
	}
	btn3 := s3.Rows[0][0]
	if btn3.Type != ui.ButtonSwitchInline || btn3.InlineQuery != "help" {
		t.Errorf("unexpected button in SwitchInline screen: %+v", btn3)
	}
}

func TestHandoffResult_FallbackTextAndScreen(t *testing.T) {
	// 1. DeepLink mode
	res := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/GoUltroidBot?start=token_12345",
	}
	s := res.AsFallbackScreen("Help", "Open help in bot:")
	if len(s.Rows) != 0 {
		t.Errorf("expected no button rows on fallback screen, got %d", len(s.Rows))
	}
	text := res.FallbackText("Help", "Open help in bot:")
	if !strings.Contains(text, "<b>Help</b>") {
		t.Errorf("expected title in fallback text, got %s", text)
	}
	if !strings.Contains(text, "<a href=\"https://t.me/GoUltroidBot?start=token_12345\">Open in Assistant</a>") {
		t.Errorf("expected clickable assistant link in fallback text, got: %s", text)
	}

	// 2. SwitchInline mode
	resInline := presentation.HandoffResult{
		Mode:        presentation.HandoffSwitchInline,
		InlineQuery: "ping",
	}
	textInline := resInline.FallbackText("Search", "Search query:")
	if !strings.Contains(textInline, "<code>@bot ping</code>") {
		t.Errorf("expected inline query in fallback text, got: %s", textInline)
	}
}
