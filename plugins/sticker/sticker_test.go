package sticker

import (
	"context"
	"image"
	"image/color"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	core.MockTelegramServicer
	sent         string
	mediaSent    bool
	mediaType    string
	dummyImgPath string
}

func (m *mockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sent = text
	return &tg.Message{ID: 100, Message: text}, nil
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
	if m.dummyImgPath != "" {
		data, err := os.ReadFile(m.dummyImgPath)
		if err != nil {
			return err
		}
		return os.WriteFile(dstPath, data, 0644)
	}
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
	m.mediaSent = true
	m.mediaType = mediaType
	return &tg.Message{ID: 200}, nil
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

func TestStickerPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "sticker" {
		t.Errorf("expected name sticker, got %s", p.Name())
	}
	if p.Description() == "" {
		t.Errorf("expected non-empty description")
	}
	if err := p.Init(); err != nil {
		t.Errorf("Init failed: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected 1 command, got %d", len(cmds))
	}
	if cmds[0].Name != "sticker" {
		t.Errorf("expected sticker command, got %s", cmds[0].Name)
	}
	if len(cmds[0].Aliases) != 1 || cmds[0].Aliases[0] != "stk" {
		t.Errorf("expected stk alias, got %v", cmds[0].Aliases)
	}
	if cmds[0].Permission != core.PermissionSudo {
		t.Errorf("expected PermissionSudo, got %v", cmds[0].Permission)
	}
	if cmds[0].Timeout != 60*time.Second {
		t.Errorf("expected 60s timeout, got %v", cmds[0].Timeout)
	}
}

func TestSticker_NoImage(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".sticker"},
		Svc:     svc,
	}

	err := p.handleSticker(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "No image found") {
		t.Errorf("expected 'No image found', got: %s", svc.sent)
	}
}

func TestSticker_InvalidMediaType(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type: "audio",
			},
		},
		Svc: svc,
	}

	err := p.handleSticker(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Please reply to a photo, sticker, or image document") {
		t.Errorf("expected media type error, got: %s", svc.sent)
	}
}

func TestCalculateStickerDimensions(t *testing.T) {
	tests := []struct {
		w, h       int
		expW, expH int
	}{
		{1000, 500, 512, 256},
		{500, 1000, 256, 512},
		{800, 800, 512, 512},
		{100, 200, 256, 512},
		{0, 0, 512, 512},
	}

	for _, tt := range tests {
		gotW, gotH := calculateStickerDimensions(tt.w, tt.h)
		if gotW != tt.expW || gotH != tt.expH {
			t.Errorf("for (%d, %d): expected (%d, %d), got (%d, %d)", tt.w, tt.h, tt.expW, tt.expH, gotW, gotH)
		}
	}
}

func TestSticker_ConvertProcess(t *testing.T) {
	// Create a dummy 200x100 PNG image
	tmpPath := filepath.Join(t.TempDir(), "test-img.png")
	tmpFile, err := os.Create(tmpPath)
	if err != nil {
		t.Fatalf("failed to create temp file: %v", err)
	}
	defer tmpFile.Close()

	img := image.NewRGBA(image.Rect(0, 0, 200, 100))
	for x := 0; x < 200; x++ {
		for y := 0; y < 100; y++ {
			img.Set(x, y, color.RGBA{R: 255, G: 0, B: 0, A: 255})
		}
	}
	if err := png.Encode(tmpFile, img); err != nil {
		t.Fatalf("failed to encode png: %v", err)
	}
	tmpFile.Close()

	svc := &mockService{dummyImgPath: tmpFile.Name()}
	p := New()
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type:     "photo",
				FileName: "sample.jpg",
				Location: &tg.InputPhotoFileLocation{},
			},
		},
		Svc: svc,
	}

	err = p.handleSticker(ctx)
	if err != nil {
		t.Errorf("unexpected error in handleSticker: %v", err)
	}
	if !svc.mediaSent {
		t.Errorf("expected sticker media to be sent")
	}
	if svc.mediaType != "sticker" {
		t.Errorf("expected mediaType sticker, got %s", svc.mediaType)
	}
}
