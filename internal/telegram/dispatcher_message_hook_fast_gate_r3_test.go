package telegram

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/plugins/afk"
	"go.uber.org/zap"
)

type r3AFKFixture struct {
	dispatcher *Dispatcher
	tasks      *r0RecordingTaskClient
	service    *afkTestService
	plugin     *afk.Plugin
	repo       afk.Repository
	ownerID    int64
}

func newR3AFKFixture(t *testing.T, active bool) *r3AFKFixture {
	t.Helper()

	const ownerID int64 = 1001
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("open database: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })

	repo := afk.NewSQLiteRepository(db)
	if active {
		if err := repo.SetAFK(context.Background(), ownerID, true, "r3"); err != nil {
			t.Fatalf("seed AFK state: %v", err)
		}
	}

	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	tasksClient := &r0RecordingTaskClient{}
	dispatcher.SetTasks(tasksClient)
	dispatcher.SetSelfID(ownerID)

	svc := &afkTestService{
		botSentIDs: make(map[int]bool),
		messages:   make(map[int]*tg.Message),
	}
	dispatcher.SetService(svc)

	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	p := afk.New(repo, ownerID, func() core.TelegramServicer { return svc })
	if err := p.InitContext(context.Background()); err != nil {
		t.Fatalf("load AFK state: %v", err)
	}
	if err := mgr.Register(p); err != nil {
		t.Fatalf("register AFK plugin: %v", err)
	}
	t.Cleanup(func() { _ = mgr.Disable(context.Background(), "afk") })

	return &r3AFKFixture{
		dispatcher: dispatcher,
		tasks:      tasksClient,
		service:    svc,
		plugin:     p,
		repo:       repo,
		ownerID:    ownerID,
	}
}

func TestR3AFKAutomationOriginSkipsTransitionAdmission(t *testing.T) {
	fixture := newR3AFKFixture(t, true)
	fixture.service.mu.Lock()
	fixture.service.botSentIDs[701] = true
	fixture.service.mu.Unlock()

	if err := fixture.dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      701,
			Out:     true,
			Message: "automated output",
			PeerID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatalf("dispatch automation output: %v", err)
	}

	if specs := fixture.tasks.snapshot(); len(specs) != 0 {
		t.Fatalf("automation-origin AFK submissions=%d, want 0: %+v", len(specs), specs)
	}
	state, err := fixture.repo.GetAFK(context.Background(), fixture.ownerID)
	if err != nil || state == nil || !state.IsAFK {
		t.Fatalf("automation output changed AFK state: state=%+v err=%v", state, err)
	}
}

func TestR3AFKCommandSkipsAutoTransitionAdmission(t *testing.T) {
	fixture := newR3AFKFixture(t, true)

	if err := fixture.dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      702,
			Out:     true,
			Message: ".afk status",
			PeerID:  &tg.PeerUser{UserID: fixture.ownerID},
			FromID:  &tg.PeerUser{UserID: fixture.ownerID},
		},
	}); err != nil {
		t.Fatalf("dispatch AFK command: %v", err)
	}

	specs := fixture.tasks.snapshot()
	var decisions, commands int
	for _, spec := range specs {
		switch {
		case strings.HasPrefix(string(spec.ID), "decision:"):
			decisions++
		case strings.HasPrefix(string(spec.ID), "cmd:"):
			commands++
		}
	}
	if decisions != 0 {
		t.Fatalf("AFK command admitted %d auto-transition decision tasks, want 0", decisions)
	}
	if commands != 1 {
		t.Fatalf("AFK command TaskEngine submissions=%d, want 1 command task; all=%+v", commands, specs)
	}
}

func TestR3AFKManualOutgoingStillAdmitsTransition(t *testing.T) {
	fixture := newR3AFKFixture(t, true)

	if err := fixture.dispatcher.OnNewMessage(context.Background(), tg.Entities{}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      703,
			Out:     true,
			Message: "manual owner message",
			PeerID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatalf("dispatch manual outgoing: %v", err)
	}

	specs := fixture.tasks.snapshot()
	if len(specs) != 1 || !strings.HasPrefix(string(specs[0].ID), "decision:") {
		t.Fatalf("manual active-AFK submissions=%+v, want one decision task", specs)
	}
}

func TestR3AFKIncomingInactiveSkipsEventAdmission(t *testing.T) {
	fixture := newR3AFKFixture(t, false)

	if err := fixture.dispatcher.OnNewMessage(context.Background(), tg.Entities{
		Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 1}},
	}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      704,
			Message: "hello",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatalf("dispatch inactive AFK incoming: %v", err)
	}

	if specs := fixture.tasks.snapshot(); len(specs) != 0 {
		t.Fatalf("inactive AFK incoming submissions=%d, want 0: %+v", len(specs), specs)
	}
}

func TestR3AFKAutoReplyDisabledSkipsEventAdmission(t *testing.T) {
	fixture := newR3AFKFixture(t, true)
	fixture.plugin.SetAutoReply(false)

	if err := fixture.dispatcher.OnNewMessage(context.Background(), tg.Entities{
		Users: map[int64]*tg.User{2002: {ID: 2002, AccessHash: 1}},
	}, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID:      705,
			Message: "hello",
			PeerID:  &tg.PeerUser{UserID: 2002},
			FromID:  &tg.PeerUser{UserID: 2002},
		},
	}); err != nil {
		t.Fatalf("dispatch auto-reply-disabled AFK incoming: %v", err)
	}

	if specs := fixture.tasks.snapshot(); len(specs) != 0 {
		t.Fatalf("auto-reply-disabled AFK submissions=%d, want 0: %+v", len(specs), specs)
	}
}
