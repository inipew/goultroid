package media

import (
	"context"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

type mockService struct {
	sent         string
	mediaSent    bool
	mediaType    string
	mediaCaption string
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
	if msgID == 88 {
		return &tg.Message{
			ID: 88,
			Media: &tg.MessageMediaDocument{
				Document: &tg.Document{
					ID:       888,
					MimeType: "video/mp4",
					Size:     10485760,
					Attributes: []tg.DocumentAttributeClass{
						&tg.DocumentAttributeFilename{FileName: "replied_video.mp4"},
						&tg.DocumentAttributeVideo{W: 1920, H: 1080, Duration: 75},
					},
				},
			},
		}, nil
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
	m.mediaCaption = caption
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

func TestMediaPlugin_Metadata(t *testing.T) {
	p := New()
	if p.Name() != "media" {
		t.Errorf("expected name media, got %s", p.Name())
	}
	if p.Description() == "" {
		t.Errorf("description should not be empty")
	}
	if err := p.Init(); err != nil {
		t.Errorf("Init failed: %v", err)
	}
	if err := p.Shutdown(); err != nil {
		t.Errorf("Shutdown failed: %v", err)
	}

	cmds := p.Commands()
	if len(cmds) != 2 {
		t.Fatalf("expected 2 commands, got %d", len(cmds))
	}
	if cmds[0].Name != "mediainfo" {
		t.Errorf("expected mediainfo command, got %s", cmds[0].Name)
	}
	if cmds[1].Name != "extractaudio" {
		t.Errorf("expected extractaudio command, got %s", cmds[1].Name)
	}
}

func TestMediaInfo_NoMedia(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".mediainfo"},
		Svc:     svc,
	}

	err := p.handleMediaInfo(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "No media found") {
		t.Errorf("expected 'No media found', got: %s", svc.sent)
	}
}

func TestMediaInfo_WithDirectMedia(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type:     "video",
				FileName: "clip.mp4",
				MimeType: "video/mp4",
				Size:     5242880, // 5MB
				Width:    1920,
				Height:   1080,
				Duration: 135, // 2m 15s
			},
		},
		Svc: svc,
	}

	err := p.handleMediaInfo(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "Media Information") {
		t.Errorf("expected header 'Media Information', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "clip.mp4") {
		t.Errorf("expected filename 'clip.mp4', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "video/mp4") {
		t.Errorf("expected mime 'video/mp4', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "1920x1080") {
		t.Errorf("expected resolution '1920x1080', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "2m 15s") {
		t.Errorf("expected duration '2m 15s', got: %s", svc.sent)
	}
}

func TestMediaInfo_WithRepliedMedia(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID:        1,
			ReplyToID: 88,
		},
		Svc: svc,
	}

	err := p.handleMediaInfo(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}

	if !strings.Contains(svc.sent, "replied_video.mp4") {
		t.Errorf("expected 'replied_video.mp4', got: %s", svc.sent)
	}
	if !strings.Contains(svc.sent, "1920x1080") {
		t.Errorf("expected resolution '1920x1080', got: %s", svc.sent)
	}
}

func TestExtractAudio_NoMedia(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		PeerID:  &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{ID: 1, Text: ".extractaudio"},
		Svc:     svc,
	}

	err := p.handleExtractAudio(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "No media found") {
		t.Errorf("expected 'No media found', got: %s", svc.sent)
	}
}

func TestExtractAudio_Photo(t *testing.T) {
	p := New()
	svc := &mockService{}
	ctx := &core.Context{
		Ctx:    context.Background(),
		PeerID: &tg.InputPeerChat{ChatID: 100},
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type:     "photo",
				FileName: "pic.jpg",
			},
		},
		Svc: svc,
	}

	err := p.handleExtractAudio(ctx)
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
	if !strings.Contains(svc.sent, "Cannot extract audio from a photo") {
		t.Errorf("expected 'Cannot extract audio from a photo', got: %s", svc.sent)
	}
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		sec      int
		expected string
	}{
		{0, ""},
		{-5, ""},
		{45, "45s"},
		{125, "2m 5s"},
		{3665, "1h 1m 5s"},
	}

	for _, c := range cases {
		got := formatDuration(c.sec)
		if got != c.expected {
			t.Errorf("for %d sec: expected %s, got %s", c.sec, c.expected, got)
		}
	}
}

func TestFormatBytes(t *testing.T) {
	cases := []struct {
		bytes    int64
		expected string
	}{
		{500, "500 B"},
		{1024, "1.00 KB"},
		{1048576, "1.00 MB"},
		{1073741824, "1.00 GB"},
	}

	for _, c := range cases {
		got := formatBytes(c.bytes)
		if got != c.expected {
			t.Errorf("for %d bytes: expected %s, got %s", c.bytes, c.expected, got)
		}
	}
}
