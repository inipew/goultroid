package pmrelayadmin

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/feature"
	"github.com/inipew/goultroid/internal/services/pmrelay"
)

type recordingServicer struct {
	core.MockTelegramServicer
	messages []string
}

func (s *recordingServicer) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.messages = append(s.messages, text)
	return &tg.Message{ID: len(s.messages), Message: text}, nil
}

func newControlFeature(t *testing.T) (*Feature, *pmrelay.Service, *pmrelay.SQLiteRepository) {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := database.RunFeatureMigrations(context.Background(), db, pmrelay.MigrationProvider{}); err != nil {
		t.Fatal(err)
	}
	repo := pmrelay.NewSQLiteRepositoryWithLimits(db, pmrelay.Limits{
		Mappings: 8, Deliveries: 8, Audience: 8, Blocked: 8,
	})
	service := pmrelay.NewService(repo, 7)
	service.SetEnabled(true)
	return New(service), service, repo
}

func newOwnerContext(replyTo int, args ...string) (*core.Context, *recordingServicer) {
	svc := &recordingServicer{}
	return &core.Context{
		Ctx:      context.Background(),
		Source:   core.ExecutionAssistant,
		Command:  "relay",
		Args:     args,
		RawArgs:  strings.Join(args, " "),
		Message:  &core.Message{ID: 900, SenderID: 7, ReplyToID: replyTo},
		Chat:     &core.Chat{ID: 7, Type: "private"},
		Sender:   &core.User{ID: 7},
		Principal: &core.Principal{UserID: 7, IsOwner: true, IsSudo: true},
		Svc:      svc,
		PeerID:   &tg.InputPeerUser{UserID: 7, AccessHash: 1},
	}, svc
}

func seedMappedVisitor(t *testing.T, repo *pmrelay.SQLiteRepository, base time.Time) {
	t.Helper()
	ctx := context.Background()
	if _, err := repo.EnsureMapping(ctx, pmrelay.Mapping{
		OwnerChatID: 7, OwnerMessageID: 100,
		VisitorUserID: 42, VisitorMessageID: 11,
		CreatedAt: base, ExpiresAt: base.Add(time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.TouchAudience(ctx, pmrelay.AudienceTouch{
		UserID: 42, Source: pmrelay.AudienceSourceRelay, SeenAt: base,
	}); err != nil {
		t.Fatal(err)
	}
}

func TestFeatureCommandsAreCanonicalOwnerPrivateAssistantSurfaces(t *testing.T) {
	f, _, _ := newControlFeature(t)
	cmds := f.Commands()
	if len(cmds) != 2 {
		t.Fatalf("commands=%d, want 2", len(cmds))
	}
	var relay, who *core.Command
	for i := range cmds {
		switch cmds[i].Name {
		case "relay":
			relay = &cmds[i]
		case "who":
			who = &cmds[i]
		}
	}
	if relay == nil || who == nil {
		t.Fatalf("commands=%+v", cmds)
	}
	for _, cmd := range []*core.Command{relay, who} {
		if cmd.Permission != core.PermissionOwner || !cmd.PrivateOnly ||
			cmd.Surfaces != execution.SurfaceAssistant ||
			cmd.Invocation.Assistant != core.InvocationSelfOnly {
			t.Fatalf("command policy for %s = %+v", cmd.Name, *cmd)
		}
	}
	if !who.ReplyOnly {
		t.Fatal("/who must remain reply-only")
	}
	if _, err := feature.BindCanonicalCommands(f.FeatureSpec(), cmds); err != nil {
		t.Fatalf("BindCanonicalCommands() error=%v", err)
	}
}

func TestRelayControlReplyBanWhoAndUnban(t *testing.T) {
	f, service, repo := newControlFeature(t)
	base := time.Now().UTC().Add(-time.Minute)
	seedMappedVisitor(t, repo, base)

	banCtx, banSvc := newOwnerContext(100, "ban", "spam")
	if err := f.handleRelay(banCtx); err != nil {
		t.Fatalf("handleRelay(ban) error=%v", err)
	}
	if len(banSvc.messages) != 1 || !strings.Contains(banSvc.messages[0], "Visitor blocked") {
		t.Fatalf("ban reply=%+v", banSvc.messages)
	}
	block, err := repo.GetVisitorBlock(context.Background(), 42)
	if err != nil || block.Reason != "spam" {
		t.Fatalf("durable block=%+v err=%v", block, err)
	}

	whoCtx, whoSvc := newOwnerContext(100)
	whoCtx.Command = "who"
	if err := f.handleWho(whoCtx); err != nil {
		t.Fatalf("handleWho() error=%v", err)
	}
	if len(whoSvc.messages) != 1 ||
		!strings.Contains(whoSvc.messages[0], "<code>42</code>") ||
		!strings.Contains(whoSvc.messages[0], "<code>blocked</code>") ||
		!strings.Contains(whoSvc.messages[0], "spam") {
		t.Fatalf("/who reply=%q", strings.Join(whoSvc.messages, "\n"))
	}

	unbanCtx, unbanSvc := newOwnerContext(100, "unban")
	if err := f.handleRelay(unbanCtx); err != nil {
		t.Fatalf("handleRelay(unban) error=%v", err)
	}
	if len(unbanSvc.messages) != 1 || !strings.Contains(unbanSvc.messages[0], "Visitor unblocked") {
		t.Fatalf("unban reply=%+v", unbanSvc.messages)
	}
	if _, err := repo.GetVisitorBlock(context.Background(), 42); !errors.Is(err, pmrelay.ErrBlockNotFound) {
		t.Fatalf("block after unban error=%v", err)
	}

	details, err := service.VisitorDetails(context.Background(), 100)
	if err != nil {
		t.Fatal(err)
	}
	if details.Block != nil {
		t.Fatalf("visitor remains blocked: %+v", details.Block)
	}
}

func TestRelayControlStatusBlockedListAndNumericTarget(t *testing.T) {
	f, service, repo := newControlFeature(t)
	base := time.Now().UTC().Add(-time.Minute)
	seedMappedVisitor(t, repo, base)

	numericCtx, _ := newOwnerContext(0, "ban", "43", "manual abuse")
	if err := f.handleRelay(numericCtx); err != nil {
		t.Fatalf("handleRelay(numeric ban) error=%v", err)
	}
	if _, err := repo.GetVisitorBlock(context.Background(), 43); err != nil {
		t.Fatalf("numeric durable block missing: %v", err)
	}

	statusCtx, statusSvc := newOwnerContext(0)
	if err := f.handleRelay(statusCtx); err != nil {
		t.Fatalf("handleRelay(status) error=%v", err)
	}
	if len(statusSvc.messages) != 1 ||
		!strings.Contains(statusSvc.messages[0], "<code>enabled</code>") ||
		!strings.Contains(statusSvc.messages[0], "Blocked visitors:") {
		t.Fatalf("status reply=%+v", statusSvc.messages)
	}

	listCtx, listSvc := newOwnerContext(0, "blocked")
	if err := f.handleRelay(listCtx); err != nil {
		t.Fatalf("handleRelay(blocked) error=%v", err)
	}
	if len(listSvc.messages) != 1 ||
		!strings.Contains(listSvc.messages[0], "<code>43</code>") ||
		!strings.Contains(listSvc.messages[0], "manual abuse") {
		t.Fatalf("blocked list=%+v", listSvc.messages)
	}

	status, err := service.Status(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Blocked != 1 || status.Audience != 1 || status.Mappings != 1 {
		t.Fatalf("status=%+v", status)
	}
}


func TestRelayBlockedListBoundsPageAndReasonPreview(t *testing.T) {
	f, _, repo := newControlFeature(t)
	now := time.Now().UTC()
	for i := int64(1); i <= blockedPageSize+1; i++ {
		reason := strings.Repeat("&", blockedReasonPreviewRunes+32)
		if _, err := repo.SetVisitorBlock(context.Background(), pmrelay.VisitorBlock{
			VisitorUserID: 100 + i,
			BlockedAt:     now.Add(time.Duration(i) * time.Second),
			Reason:        reason,
		}); err != nil {
			t.Fatal(err)
		}
	}

	ctx, svc := newOwnerContext(0, "blocked")
	if err := f.handleRelay(ctx); err != nil {
		t.Fatalf("handleRelay(blocked) error=%v", err)
	}
	if len(svc.messages) != 1 {
		t.Fatalf("blocked list replies=%d, want 1", len(svc.messages))
	}
	reply := svc.messages[0]
	if len([]rune(reply)) >= 4096 {
		t.Fatalf("blocked page output too large: %d runes", len([]rune(reply)))
	}
	if strings.Count(reply, "• <code>") != blockedPageSize {
		t.Fatalf("blocked page rows=%d, want %d", strings.Count(reply, "• <code>"), blockedPageSize)
	}
	if !strings.Contains(reply, "Next page:") || !strings.Contains(reply, "…") {
		t.Fatalf("blocked page missing pagination/reason truncation: %q", reply)
	}
}


func TestRelayControlForceSubDefaultsClosedAndPersistsRevision(t *testing.T) {
	f, service, _ := newControlFeature(t)

	statusCtx, statusSvc := newOwnerContext(0, "forcesub")
	if err := f.handleRelay(statusCtx); err != nil {
		t.Fatalf("handleRelay(forcesub status) error=%v", err)
	}
	if len(statusSvc.messages) != 1 || !strings.Contains(statusSvc.messages[0], "Force-sub disabled") {
		t.Fatalf("initial force-sub status=%+v", statusSvc.messages)
	}

	setCtx, setSvc := newOwnerContext(0, "forcesub", "set", "@Required_Channel")
	if err := f.handleRelay(setCtx); err != nil {
		t.Fatalf("handleRelay(forcesub set) error=%v", err)
	}
	if len(setSvc.messages) != 1 ||
		!strings.Contains(setSvc.messages[0], "@required_channel") ||
		!strings.Contains(setSvc.messages[0], "fail-closed") {
		t.Fatalf("force-sub set reply=%+v", setSvc.messages)
	}
	config, err := service.ForceSubConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !config.Enabled ||
		config.ChannelUsername != "required_channel" ||
		config.JoinURL != "https://t.me/required_channel" ||
		config.FailureMode != pmrelay.ForceSubFailClosed ||
		config.Revision != 2 {
		t.Fatalf("force-sub config=%+v", config)
	}

	openCtx, openSvc := newOwnerContext(
		0,
		"forcesub", "set", "@Required_Channel", "open", "https://t.me/+invite-code",
	)
	if err := f.handleRelay(openCtx); err != nil {
		t.Fatalf("handleRelay(forcesub fail-open) error=%v", err)
	}
	if len(openSvc.messages) != 1 || !strings.Contains(openSvc.messages[0], "fail-open") {
		t.Fatalf("force-sub fail-open reply=%+v", openSvc.messages)
	}
	config, err = service.ForceSubConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.FailureMode != pmrelay.ForceSubFailOpen ||
		config.JoinURL != "https://t.me/+invite-code" ||
		config.Revision != 3 {
		t.Fatalf("force-sub fail-open config=%+v", config)
	}

	offCtx, offSvc := newOwnerContext(0, "forcesub", "off")
	if err := f.handleRelay(offCtx); err != nil {
		t.Fatalf("handleRelay(forcesub off) error=%v", err)
	}
	if len(offSvc.messages) != 1 || !strings.Contains(offSvc.messages[0], "Force-sub disabled") {
		t.Fatalf("force-sub off reply=%+v", offSvc.messages)
	}
	config, err = service.ForceSubConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.FailureMode != pmrelay.ForceSubFailClosed || config.Revision != 4 {
		t.Fatalf("disabled force-sub config=%+v", config)
	}
}

func TestRelayControlForceSubRejectsUnsafeConfiguration(t *testing.T) {
	f, service, _ := newControlFeature(t)
	ctx, svc := newOwnerContext(
		0,
		"forcesub", "set", "@required_channel", "closed", "https://example.com/join",
	)
	if err := f.handleRelay(ctx); err != nil {
		t.Fatalf("handleRelay(invalid force-sub) error=%v", err)
	}
	if len(svc.messages) != 1 || !strings.Contains(svc.messages[0], "Invalid force-sub config") {
		t.Fatalf("invalid force-sub reply=%+v", svc.messages)
	}
	config, err := service.ForceSubConfig(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if config.Enabled || config.Revision != 1 {
		t.Fatalf("invalid config mutated durable policy=%+v", config)
	}
}
