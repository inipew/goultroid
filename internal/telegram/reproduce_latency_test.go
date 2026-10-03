package telegram

import (
	"context"
	"fmt"
	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/taskengine"
	"github.com/inipew/goultroid/plugins/afk"
	"github.com/inipew/goultroid/plugins/alive"
	"go.uber.org/zap"
	"testing"
	"time"
)

type dummyService struct {
	core.MockTelegramServicer
	sentMessages []*tg.Message
}

func (s *dummyService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	msg := &tg.Message{ID: len(s.sentMessages) + 1, Message: text}
	s.sentMessages = append(s.sentMessages, msg)
	return msg, nil
}
func (s *dummyService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	return nil
}
func (s *dummyService) IsBotSent(msgID int) bool {
	return false
}
func (s *dummyService) GetMessage(ctx context.Context, peer tg.InputPeerClass, id int) (*tg.Message, error) {
	return nil, nil
}
func TestReproduceLatencyIssue(t *testing.T) {
	const ownerID int64 = 589287392
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	router := core.NewRouter(".")
	dispatcher := NewDispatcher(router, core.NewPermissions(ownerID, nil), nil, zap.NewNop())
	engine := taskengine.NewEngine(taskengine.DefaultConfig)
	if err := engine.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = engine.Stop(context.Background()) }()
	dispatcher.SetTasks(engine)
	dispatcher.SetSelfID(ownerID)
	svc := &dummyService{}
	dispatcher.SetService(svc)
	mgr := plugin.NewManager(router)
	mgr.SetHookRegistrar(dispatcher)
	mgr.SetTaskClient(engine)
	afkRepo := afk.NewSQLiteRepository(db)
	afkPlugin := afk.New(afkRepo, ownerID, func() core.TelegramServicer { return svc })
	if err := mgr.Register(afkPlugin); err != nil {
		t.Fatal(err)
	}
	alivePlugin := alive.New(time.Now())
	if err := mgr.Register(alivePlugin); err != nil {
		t.Fatal(err)
	}
	entities := tg.Entities{Users: map[int64]*tg.User{
		ownerID: {ID: ownerID, AccessHash: 123},
	}}
	dispatchMsg := func(id int, text string) time.Duration {
		start := time.Now()
		err := dispatcher.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
			Message: &tg.Message{
				ID:      id,
				Out:     true,
				Message: text,
				PeerID:  &tg.PeerUser{UserID: ownerID},
				FromID:  &tg.PeerUser{UserID: ownerID},
			},
		})
		elapsed := time.Since(start)
		if err != nil {
			t.Fatalf("dispatch %s: %v", text, err)
		}
		return elapsed
	}
	// Step 1: test .alive (AFK inactive)
	d1 := dispatchMsg(1001, ".alive")
	fmt.Printf("Step 1 (.alive when inactive): %v\n", d1)
	// Step 2: test .afk (Toggle AFK to ON)
	d2 := dispatchMsg(1002, ".afk")
	fmt.Printf("Step 2 (.afk enable): %v\n", d2)
	// Check AFK state
	st, _ := afkRepo.GetAFK(context.Background(), ownerID)
	fmt.Printf("AFK state after Step 2: isAFK=%v\n", st.IsAFK)
	// Step 3: test .alive again (now AFK is active!)
	d3 := dispatchMsg(1003, ".alive")
	fmt.Printf("Step 3 (.alive when active): %v\n", d3)
	// Step 4: test .afk again (when AFK is active)
	// Enable it again first if step 3 disabled it
	if !st.IsAFK {
		_ = afkRepo.SetAFK(context.Background(), ownerID, true, "test")
	}
	d4 := dispatchMsg(1004, ".afk")
	fmt.Printf("Step 4 (.afk when active): %v\n", d4)
}
