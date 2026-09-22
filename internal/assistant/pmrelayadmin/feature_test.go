package pmrelayadmin

import (
	"context"
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
	base := time.Date(2026, 9, 22, 23, 0, 0, 0, time.UTC)
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
	if _, err := repo.GetVisitorBlock(context.Background(), 42); err != pmrelay.ErrBlockNotFound {
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
	base := time.Date(2026, 9, 22, 23, 30, 0, 0, time.UTC)
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
