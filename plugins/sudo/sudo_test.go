package sudo

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/database"
)

type mockService struct {
	sent string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 10, Message: text}, nil
}
func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.sent = text
	return nil
}
func (m *mockService) DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error {
	return nil
}
func (m *mockService) React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error {
	return nil
}
func (m *mockService) GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error) {
	if msgID == 55 {
		return &tg.Message{ID: 55, FromID: &tg.PeerUser{UserID: 3003}}, nil
	}
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

func TestSudoPlugin(t *testing.T) {
	db, err := database.Open(":memory:")
	if err != nil {
		t.Fatalf("failed to open in-memory db: %v", err)
	}
	defer db.Close()

	ownerID := int64(1001)
	perms := core.NewPermissions(ownerID, nil)
	p := New(db, perms)

	if p.Name() != "sudo" {
		t.Errorf("expected name sudo, got %s", p.Name())
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}

	cmds := p.Commands()
	cmdMap := make(map[string]core.Command)
	for _, c := range cmds {
		cmdMap[c.Name] = c
	}

	svc := &mockService{}
	baseCtx := &core.Context{
		Ctx:     context.Background(),
		Sender:  &core.User{ID: ownerID},
		Message: &core.Message{ID: 1},
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
	}

	// 1. Initial sudolist -> empty
	ctxList := *baseCtx
	if err := cmdMap["sudolist"].Handler(&ctxList); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "No dynamic sudo users") {
		t.Errorf("expected empty list message, got: %s", svc.sent)
	}

	// 2. addsudo without args or reply -> usage error
	ctxNoArg := *baseCtx
	if err := cmdMap["addsudo"].Handler(&ctxNoArg); err == nil {
		t.Errorf("expected error on no arg/reply")
	}

	// 3. addsudo with owner ID -> rejected
	ctxOwner := *baseCtx
	ctxOwner.Args = []string{"1001"}
	if err := cmdMap["addsudo"].Handler(&ctxOwner); err == nil {
		t.Errorf("expected error when adding owner")
	}

	// 4. addsudo with arg
	ctxAdd := *baseCtx
	ctxAdd.Args = []string{"2002"}
	if err := cmdMap["addsudo"].Handler(&ctxAdd); err != nil {
		t.Fatalf("unexpected error adding sudo: %v", err)
	}
	if !strings.Contains(svc.sent, "2002") || !strings.Contains(svc.sent, "added") {
		t.Errorf("expected success message, got: %s", svc.sent)
	}
	if !perms.IsSudo(2002) {
		t.Errorf("expected 2002 to be in permissions")
	}

	// 5. sudolist after add
	if err := cmdMap["sudolist"].Handler(&ctxList); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "2002") {
		t.Errorf("expected sudolist to contain 2002, got: %s", svc.sent)
	}

	// 6. delsudo with arg
	ctxDel := *baseCtx
	ctxDel.Args = []string{"2002"}
	if err := cmdMap["delsudo"].Handler(&ctxDel); err != nil {
		t.Fatalf("unexpected error deleting sudo: %v", err)
	}
	if !strings.Contains(svc.sent, "2002") || !strings.Contains(svc.sent, "removed") {
		t.Errorf("expected removed message, got: %s", svc.sent)
	}
	if perms.IsSudo(2002) {
		t.Errorf("expected 2002 to be removed from permissions")
	}

	// 7. delsudo non-existent
	if err := cmdMap["delsudo"].Handler(&ctxDel); err == nil {
		t.Errorf("expected error when removing non-existent user")
	}
	if !strings.Contains(svc.sent, "Failed to remove") {
		t.Errorf("expected failure message, got: %s", svc.sent)
	}

	// 8. addsudo with reply
	ctxReply := *baseCtx
	ctxReply.Message = &core.Message{
		ID:        2,
		ReplyToID: 55,
	}
	if err := cmdMap["addsudo"].Handler(&ctxReply); err != nil {
		t.Fatalf("unexpected error adding via reply: %v", err)
	}
	if !perms.IsSudo(3003) {
		t.Errorf("expected 3003 to be sudo via reply")
	}
}
