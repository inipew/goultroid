package locks

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent         string
	rightsEdited tg.ChatBannedRights
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockService) EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error {
	m.rightsEdited = rights
	return nil
}

func TestLocksPlugin(t *testing.T) {
	p := New()
	if p.Name() != "locks" {
		t.Errorf("expected name 'locks', got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Fatalf("Init failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 3 {
		t.Fatalf("expected 3 commands, got %d", len(cmds))
	}

	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	svc := &mockService{}
	peer := &tg.InputPeerChannel{ChannelID: 12345, AccessHash: 67890}

	// 1. .lock missing args
	ctxLockEmpty := &core.Context{
		Ctx:     context.Background(),
		Command: "lock",
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["lock"].Handler(ctxLockEmpty); err == nil {
		t.Errorf("expected error for empty lock argument")
	}

	// 2. .lock invalid option
	ctxLockInvalid := &core.Context{
		Ctx:     context.Background(),
		Command: "lock",
		Args:    []string{"invalid_option"},
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["lock"].Handler(ctxLockInvalid); err == nil {
		t.Errorf("expected error for invalid lock option")
	}

	// 3. .lock media
	ctxLockMedia := &core.Context{
		Ctx:     context.Background(),
		Command: "lock",
		Args:    []string{"media"},
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["lock"].Handler(ctxLockMedia); err != nil {
		t.Fatalf("lock media failed: %v", err)
	}
	if !svc.rightsEdited.SendMedia {
		t.Errorf("expected SendMedia to be true (locked)")
	}
	if !strings.Contains(svc.sent, "Locked permission") {
		t.Errorf("expected lock confirmation, got: %s", svc.sent)
	}

	// 4. .unlock media
	ctxUnlockMedia := &core.Context{
		Ctx:     context.Background(),
		Command: "unlock",
		Args:    []string{"media"},
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["unlock"].Handler(ctxUnlockMedia); err != nil {
		t.Fatalf("unlock media failed: %v", err)
	}
	if svc.rightsEdited.SendMedia {
		t.Errorf("expected SendMedia to be false (unlocked)")
	}

	// 5. .lock all
	ctxLockAll := &core.Context{
		Ctx:     context.Background(),
		Command: "lock",
		Args:    []string{"all"},
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["lock"].Handler(ctxLockAll); err != nil {
		t.Fatalf("lock all failed: %v", err)
	}
	if !svc.rightsEdited.SendMessages || !svc.rightsEdited.SendMedia || !svc.rightsEdited.EmbedLinks {
		t.Errorf("expected all permissions to be locked")
	}

	// 6. .locks display
	ctxLocks := &core.Context{
		Ctx:     context.Background(),
		Command: "locks",
		Svc:     svc,
		PeerID:  peer,
	}
	if err := cmdMap["locks"].Handler(ctxLocks); err != nil {
		t.Fatalf("locks failed: %v", err)
	}
	if !strings.Contains(svc.sent, "Chat Permissions & Locks") {
		t.Errorf("expected locks summary, got: %s", svc.sent)
	}

	// 7. Private chat rejection — now returns ErrUnsupported after Reply (observability)
	ctxPrivate := &core.Context{
		Ctx:     context.Background(),
		Command: "lock",
		Args:    []string{"media"},
		Svc:     svc,
		PeerID:  &tg.InputPeerUser{UserID: 999},
	}
	if err := cmdMap["lock"].Handler(ctxPrivate); !errors.Is(err, core.ErrUnsupported) {
		t.Errorf("expected ErrUnsupported when locking in private chat, got: %v", err)
	}
	if !strings.Contains(svc.sent, "hanya dapat digunakan di grup") {
		t.Errorf("expected friendly group notice, got: %s", svc.sent)
	}
}
