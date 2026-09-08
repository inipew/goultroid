package profile

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer

	sent             string
	updateProfileErr error
	blockUserErr     error
	unblockUserErr   error
	uploadPhotoErr   error
	deletePhotoErr   error
	getDialogsErr    error
	getContactsErr   error

	updatedFirstName  *string
	updatedLastName   *string
	updatedAbout      *string
	blockedPeer       tg.InputPeerClass
	unblockedPeer     tg.InputPeerClass
	uploadedPhotoPath string
	deletedPhotoLimit int
	mockDialogs       []*core.Chat
	mockContacts      []*core.User
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 1, Message: text}, nil
}

func (m *mockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.sent = text
	return nil
}

func (m *mockService) GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return &tg.UsersUserFull{
		Users: []tg.UserClass{
			&tg.User{
				ID:        777888,
				FirstName: "Goultroid",
				LastName:  "Bot",
				Username:  "goultroid_bot",
				Phone:     "1234567890",
				Premium:   true,
			},
		},
		FullUser: tg.UserFull{
			About: "I am GoUltroid userbot!",
		},
	}, nil
}

func (m *mockService) UpdateProfile(ctx context.Context, firstName, lastName, about *string) error {
	if m.updateProfileErr != nil {
		return m.updateProfileErr
	}
	m.updatedFirstName = firstName
	m.updatedLastName = lastName
	m.updatedAbout = about
	return nil
}

func (m *mockService) BlockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if m.blockUserErr != nil {
		return m.blockUserErr
	}
	m.blockedPeer = peer
	return nil
}

func (m *mockService) UnblockUser(ctx context.Context, peer tg.InputPeerClass) error {
	if m.unblockUserErr != nil {
		return m.unblockUserErr
	}
	m.unblockedPeer = peer
	return nil
}

func (m *mockService) UploadProfilePhoto(ctx context.Context, filePath string) error {
	if m.uploadPhotoErr != nil {
		return m.uploadPhotoErr
	}
	m.uploadedPhotoPath = filePath
	return nil
}

func (m *mockService) DeleteProfilePhotos(ctx context.Context, limit int) (int, error) {
	if m.deletePhotoErr != nil {
		return 0, m.deletePhotoErr
	}
	m.deletedPhotoLimit = limit
	return limit, nil
}

func (m *mockService) GetDialogs(ctx context.Context, limit int) ([]*core.Chat, error) {
	if m.getDialogsErr != nil {
		return nil, m.getDialogsErr
	}
	return m.mockDialogs, nil
}

func (m *mockService) GetContacts(ctx context.Context) ([]*core.User, error) {
	if m.getContactsErr != nil {
		return nil, m.getContactsErr
	}
	return m.mockContacts, nil
}

func TestProfile_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "profile" {
		t.Errorf("expected name 'profile', got %s", p.Name())
	}
	if p.Description() == "" {
		t.Errorf("description should not be empty")
	}
	if err := p.Init(); err != nil {
		t.Errorf("unexpected error in Init: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Errorf("unexpected error in Shutdown: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 9 {
		t.Fatalf("expected 9 commands, got %d", len(cmds))
	}
}

func TestHandleMe(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		PeerID: &tg.InputPeerSelf{},
	}

	err := p.handleMe(ctx)
	if err != nil {
		t.Fatalf("handleMe returned error: %v", err)
	}

	if !strings.Contains(svc.sent, "My Profile") {
		t.Errorf("expected header 'My Profile', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "777888") {
		t.Errorf("expected user ID 777888, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Goultroid") {
		t.Errorf("expected first name Goultroid, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "I am GoUltroid userbot!") {
		t.Errorf("expected bio text, got: %s", svc.sent)
	}
}

func TestHandleSetBio(t *testing.T) {
	p := New()
	svc := &mockService{}

	// 1. Empty bio
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		RawArgs: "   ",
		PeerID:  &tg.InputPeerSelf{},
	}
	_ = p.handleSetBio(ctx)
	if !strings.Contains(svc.sent, "Please provide bio text") {
		t.Errorf("expected warning for empty bio, got: %s", svc.sent)
	}

	// 2. Over 70 chars
	ctx.RawArgs = strings.Repeat("A", 75)
	_ = p.handleSetBio(ctx)
	if !strings.Contains(svc.sent, "Bio text is too long") {
		t.Errorf("expected warning for long bio, got: %s", svc.sent)
	}

	// 3. Valid bio
	ctx.RawArgs = "Building awesome Telegram bots with Go!"
	err := p.handleSetBio(ctx)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.updatedAbout == nil || *svc.updatedAbout != "Building awesome Telegram bots with Go!" {
		t.Errorf("expected updated bio, got: %v", svc.updatedAbout)
	}
	if !strings.Contains(svc.sent, "Bio updated successfully") {
		t.Errorf("expected success message, got: %s", svc.sent)
	}
}

func TestHandleSetName(t *testing.T) {
	p := New()
	svc := &mockService{}

	// 1. Empty args
	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		Args:   nil,
		PeerID: &tg.InputPeerSelf{},
	}
	_ = p.handleSetName(ctx)
	if !strings.Contains(svc.sent, "Please provide a name") {
		t.Errorf("expected warning, got: %s", svc.sent)
	}

	// 2. First name only
	ctx.Args = []string{"Dhimas"}
	_ = p.handleSetName(ctx)
	if svc.updatedFirstName == nil || *svc.updatedFirstName != "Dhimas" {
		t.Errorf("expected first name Dhimas, got %v", svc.updatedFirstName)
	}
	if svc.updatedLastName == nil || *svc.updatedLastName != "" {
		t.Errorf("expected empty last name, got %v", svc.updatedLastName)
	}

	// 3. First name and last name
	ctx.Args = []string{"Dhimas", "Anugrah", "Putra"}
	_ = p.handleSetName(ctx)
	if svc.updatedFirstName == nil || *svc.updatedFirstName != "Dhimas" {
		t.Errorf("expected first name Dhimas, got %v", svc.updatedFirstName)
	}
	if svc.updatedLastName == nil || *svc.updatedLastName != "Anugrah Putra" {
		t.Errorf("expected last name Anugrah Putra, got %v", svc.updatedLastName)
	}
	if !strings.Contains(svc.sent, "Dhimas Anugrah Putra") {
		t.Errorf("expected full name in response, got: %s", svc.sent)
	}
}

func TestHandleSetPic_LocalFile(t *testing.T) {
	p := New()
	svc := &mockService{}

	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "profile.jpg")
	if err := os.WriteFile(filePath, []byte("fake-jpeg-data"), 0644); err != nil {
		t.Fatalf("failed to write dummy file: %v", err)
	}

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		Args:   []string{filePath},
		PeerID: &tg.InputPeerSelf{},
	}

	err := p.handleSetPic(ctx)
	if err != nil {
		t.Fatalf("handleSetPic error: %v", err)
	}
	if svc.uploadedPhotoPath != filePath {
		t.Errorf("expected uploaded path %s, got %s", filePath, svc.uploadedPhotoPath)
	}
	if !strings.Contains(svc.sent, "Profile photo updated successfully") {
		t.Errorf("expected success reply, got: %s", svc.sent)
	}
}

func TestHandleDelPhoto(t *testing.T) {
	p := New()
	svc := &mockService{}

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		Args:   []string{"2"},
		PeerID: &tg.InputPeerSelf{},
	}

	err := p.handleDelPhoto(ctx)
	if err != nil {
		t.Fatalf("handleDelPhoto error: %v", err)
	}
	if svc.deletedPhotoLimit != 2 {
		t.Errorf("expected limit 2, got %d", svc.deletedPhotoLimit)
	}
	if !strings.Contains(svc.sent, "Successfully deleted 2 profile photo") {
		t.Errorf("expected reply with deleted count, got: %s", svc.sent)
	}
}

func TestHandleBlockAndUnblock(t *testing.T) {
	p := New()
	svc := &mockService{}

	ctx := &core.Context{
		Ctx:      context.Background(),
		Svc:      svc,
		Args:     []string{"12345678"},
		PeerID:   &tg.InputPeerSelf{},
		Resolver: &core.MockPeerResolver{UserID: 12345678, UserPeer: &tg.InputPeerUser{UserID: 12345678, AccessHash: 12345}},
	}

	// 1. Block
	err := p.handleBlock(ctx)
	if err != nil {
		t.Fatalf("handleBlock error: %v", err)
	}
	if !strings.Contains(svc.sent, "User blocked") || !strings.Contains(svc.sent, "12345678") {
		t.Errorf("expected block confirmation, got: %s", svc.sent)
	}

	// 2. Unblock
	err = p.handleUnblock(ctx)
	if err != nil {
		t.Fatalf("handleUnblock error: %v", err)
	}
	if !strings.Contains(svc.sent, "User unblocked") || !strings.Contains(svc.sent, "12345678") {
		t.Errorf("expected unblock confirmation, got: %s", svc.sent)
	}

	// 3. Missing user
	ctx.Args = nil
	_ = p.handleBlock(ctx)
	if !strings.Contains(svc.sent, "provide a valid user") {
		t.Errorf("expected warning for missing user, got: %s", svc.sent)
	}
}

func TestHandleContacts(t *testing.T) {
	p := New()
	svc := &mockService{
		mockContacts: []*core.User{
			{ID: 101, FirstName: "Alice", Username: "alice_tg"},
			{ID: 102, FirstName: "Bob", LastName: "Smith"},
		},
	}

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		PeerID: &tg.InputPeerSelf{},
	}

	err := p.handleContacts(ctx)
	if err != nil {
		t.Fatalf("handleContacts error: %v", err)
	}
	if !strings.Contains(svc.sent, "Contacts List (2 total)") {
		t.Errorf("expected 2 total contacts, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Alice") || !strings.Contains(svc.sent, "Bob Smith") {
		t.Errorf("expected Alice and Bob Smith in contacts list, got: %s", svc.sent)
	}
}

func TestHandleDialogs(t *testing.T) {
	p := New()
	svc := &mockService{
		mockDialogs: []*core.Chat{
			{ID: 999, Title: "Golang Developers", Type: "supergroup"},
			{ID: 888, Title: "Secret Channel", Type: "channel"},
		},
	}

	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		Args:   []string{"5"},
		PeerID: &tg.InputPeerSelf{},
	}

	err := p.handleDialogs(ctx)
	if err != nil {
		t.Fatalf("handleDialogs error: %v", err)
	}
	if !strings.Contains(svc.sent, "Recent Dialogs (2 fetched)") {
		t.Errorf("expected 2 fetched dialogs, got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "Golang Developers") || !strings.Contains(svc.sent, "Secret Channel") {
		t.Errorf("expected chat titles in dialogs list, got: %s", svc.sent)
	}
}
