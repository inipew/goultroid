package voice_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
	voiceSvc "github.com/inipew/goultroid/internal/voice"
	"github.com/inipew/goultroid/plugins/voice"
	"go.uber.org/zap"
)

type mockTelegram struct {
	core.MockTelegramServicer
	sentText string
	edited   string
}

func (m *mockTelegram) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sentText = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockTelegram) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.edited = text
	return nil
}

func setupTestDB(t *testing.T) *database.DB {
	t.Helper()
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	t.Cleanup(func() {
		_ = db.Close()
	})
	return db
}

func TestVoicePlugin(t *testing.T) {
	db := setupTestDB(t)
	mockBackend := voiceSvc.NewMockBackend()
	resolver := voiceSvc.NewResolver(nil, nil)
	svc := voiceSvc.NewService(mockBackend, db, resolver, zap.NewNop())

	p := voice.New(svc)
	if p.Name() != "voice" {
		t.Errorf("expected plugin name voice, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 9 {
		t.Fatalf("expected 9 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	mockTG := &mockTelegram{}
	chatID := int64(-100123)

	newCtx := func(args ...string) *core.Context {
		return &core.Context{
			Ctx:     context.Background(),
			Svc:     mockTG,
			PeerID:  &tg.InputPeerChat{ChatID: -100123},
			Message: &core.Message{ID: 1, SenderID: 12345},
			Args:    args,
		}
	}

	// 1. Play track 1
	ctxPlay1 := newCtx("Song 1")
	if err := cmdMap["play"].Handler(ctxPlay1); err != nil {
		t.Fatalf("play command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Now Playing") || !strings.Contains(mockTG.edited, "Song 1") {
		t.Errorf("expected Now Playing card, got %s", mockTG.edited)
	}

	// 2. Play track 2 -> Enqueued
	ctxPlay2 := newCtx("Song 2")
	if err := cmdMap["play"].Handler(ctxPlay2); err != nil {
		t.Fatalf("play 2 command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Track Enqueued") {
		t.Errorf("expected Track Enqueued card, got %s", mockTG.edited)
	}

	// 3. Queue command
	ctxQueue := newCtx()
	if err := cmdMap["queue"].Handler(ctxQueue); err != nil {
		t.Fatalf("queue command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Song 1") || !strings.Contains(mockTG.edited, "Song 2") {
		t.Errorf("expected queue listing both songs, got %s", mockTG.edited)
	}

	// 4. Pause command
	ctxPause := newCtx()
	if err := cmdMap["pause"].Handler(ctxPause); err != nil {
		t.Fatalf("pause command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "paused") {
		t.Errorf("expected pause reply, got %s", mockTG.edited)
	}

	// 5. Resume command
	ctxResume := newCtx()
	if err := cmdMap["resume"].Handler(ctxResume); err != nil {
		t.Fatalf("resume command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "resumed") {
		t.Errorf("expected resume reply, got %s", mockTG.edited)
	}

	// 6. Volume command
	ctxVol := newCtx("125")
	if err := cmdMap["volume"].Handler(ctxVol); err != nil {
		t.Fatalf("volume command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "125%") {
		t.Errorf("expected volume 125%%, got %s", mockTG.edited)
	}

	// 7. Repeat command
	ctxRepeat := newCtx("track")
	if err := cmdMap["repeat"].Handler(ctxRepeat); err != nil {
		t.Fatalf("repeat command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "track") {
		t.Errorf("expected repeat mode track, got %s", mockTG.edited)
	}

	// 8. Skip command
	ctxSkip := newCtx()
	if err := cmdMap["skip"].Handler(ctxSkip); err != nil {
		t.Fatalf("skip command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "Skipped to") {
		t.Errorf("expected skip response, got %s", mockTG.edited)
	}

	// 9. Stop/Leave command
	ctxStop := newCtx()
	if err := cmdMap["vcstop"].Handler(ctxStop); err != nil {
		t.Fatalf("stop command failed: %v", err)
	}
	if !strings.Contains(mockTG.edited, "stopped") {
		t.Errorf("expected stop response, got %s", mockTG.edited)
	}

	_ = chatID
	_ = time.Second
}
