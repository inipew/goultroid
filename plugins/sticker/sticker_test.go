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

func TestStickerRejectsOversizedImageDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "too-wide.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 9000, 1))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	svc := &mockService{dummyImgPath: path}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type:     "document",
				MimeType: "image/png",
				FileName: "too-wide.png",
				Location: &tg.InputDocumentFileLocation{},
			},
		},
		Svc: svc,
	}
	if err := New().handleSticker(ctx); err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if svc.mediaSent {
		t.Fatal("unsafe image must not be sent as sticker")
	}
	if !strings.Contains(svc.sent, "safety limit") {
		t.Fatalf("expected safety-limit response, got %q", svc.sent)
	}
}

func TestStickerRejectsNonImageDocumentBeforeDownload(t *testing.T) {
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type:     "document",
				MimeType: "application/pdf",
				Location: &tg.InputDocumentFileLocation{},
			},
		},
		Svc: svc,
	}
	if err := New().handleSticker(ctx); err != nil {
		t.Fatalf("unexpected handler error: %v", err)
	}
	if !strings.Contains(svc.sent, "not a supported image") {
		t.Fatalf("unexpected response: %q", svc.sent)
	}
}

func TestStickerCommandResources(t *testing.T) {
	cmds := New().Commands()
	if len(cmds) != 1 {
		t.Fatalf("commands=%d, want 1", len(cmds))
	}
	seen := map[string]int64{}
	for _, resource := range cmds[0].Resources {
		seen[resource.Name] = resource.Amount
	}
	for _, name := range []string{"download", "media"} {
		if seen[name] != 1 {
			t.Fatalf("resource %q=%d, want 1; all=%+v", name, seen[name], cmds[0].Resources)
		}
	}
}

func TestValidateStaticStickerOutputRejectsOversize(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := f.Truncate(staticStickerMaxBytes + 1); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStaticStickerOutput(path); err == nil {
		t.Fatal("expected oversized static sticker to be rejected")
	}
}

func TestValidateStaticStickerOutputRejectsEmptyFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.png")
	if err := os.WriteFile(path, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateStaticStickerOutput(path); err == nil {
		t.Fatal("expected empty static sticker to be rejected")
	}
}

func TestStickerSourceFormat(t *testing.T) {
	cases := []struct {
		media core.MediaInfo
		want  string
	}{
		{core.MediaInfo{Type: "sticker", MimeType: "image/webp"}, "static"},
		{core.MediaInfo{Type: "sticker", FileName: "static.png"}, "static"},
		{core.MediaInfo{Type: "sticker", MimeType: "application/x-tgsticker"}, "animated"},
		{core.MediaInfo{Type: "sticker", FileName: "animated.tgs"}, "animated"},
		{core.MediaInfo{Type: "sticker", MimeType: "video/webm"}, "video"},
		{core.MediaInfo{Type: "sticker", FileName: "video.webm"}, "video"},
		{core.MediaInfo{Type: "sticker", MimeType: "application/octet-stream", FileName: "odd.bin"}, "unsupported"},
	}
	for _, tc := range cases {
		if got := stickerSourceFormat(&tc.media); got != tc.want {
			t.Errorf("stickerSourceFormat(%+v)=%q, want %q", tc.media, got, tc.want)
		}
	}
}

func TestStickerRejectsAnimatedAndVideoSourcesWithSpecificUX(t *testing.T) {
	for _, tc := range []struct {
		mime string
		want string
	}{
		{"application/x-tgsticker", ".tgs"},
		{"video/webm", ".webm"},
	} {
		svc := &mockService{}
		ctx := &core.Context{
			Ctx: context.Background(), PeerID: &tg.InputPeerChat{ChatID: 100}, Svc: svc,
			Message: &core.Message{ID: 1, Media: &core.MediaInfo{
				Type: "sticker", MimeType: tc.mime, Location: &tg.InputDocumentFileLocation{},
			}},
		}
		if err := New().handleSticker(ctx); err != nil {
			t.Fatalf("mime %q: unexpected handler error: %v", tc.mime, err)
		}
		if !strings.Contains(svc.sent, tc.want) {
			t.Fatalf("mime %q: response=%q, want format hint %q", tc.mime, svc.sent, tc.want)
		}
		if svc.mediaSent {
			t.Fatalf("mime %q: unsupported source must not be sent", tc.mime)
		}
	}
}

func TestValidateStaticStickerOutputChecksGeometry(t *testing.T) {
	path := filepath.Join(t.TempDir(), "small.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 128, 128))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateStaticStickerOutput(path); err == nil || !strings.Contains(err.Error(), "exactly 512px") {
		t.Fatalf("geometry error=%v", err)
	}
}

func TestEncodeStaticStickerOutputFallsBackToBoundedPalette(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))
	state := uint32(0x9e3779b9)
	next := func() uint8 {
		state = state*1664525 + 1013904223
		return uint8(state >> 24)
	}
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			img.SetNRGBA(x, y, color.NRGBA{R: next(), G: next(), B: next(), A: 255})
		}
	}
	path := filepath.Join(t.TempDir(), "sticker.png")
	quantized, err := encodeStaticStickerOutput(path, img)
	if err != nil {
		t.Fatal(err)
	}
	if !quantized {
		t.Fatal("expected high-entropy source to require palette fallback")
	}
	if err := validateStaticStickerOutput(path); err != nil {
		t.Fatalf("optimized sticker invalid: %v", err)
	}
	stat, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if stat.Size() > staticStickerMaxBytes {
		t.Fatalf("optimized sticker size=%d, max=%d", stat.Size(), staticStickerMaxBytes)
	}
}
