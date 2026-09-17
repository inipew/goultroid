package render_test

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/ui/render"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

type deliveryMockService struct {
	core.MockTelegramServicer
	editMarkupErr error
	sendMarkupErr error

	sentText        string
	editedText      string
	sentMarkup      tg.ReplyMarkupClass
	editedMarkup    tg.ReplyMarkupClass
	editCount       int
	editMarkupCount int
	sendCount       int
	sendMarkupCount int
}

func (m *deliveryMockService) EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error {
	m.editMarkupCount++
	m.editedText = text
	m.editedMarkup = markup
	if m.editMarkupErr != nil {
		return m.editMarkupErr
	}
	return nil
}

func (m *deliveryMockService) EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error {
	m.editCount++
	m.editedText = text
	m.editedMarkup = nil
	return nil
}

func (m *deliveryMockService) SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error) {
	m.sendMarkupCount++
	m.sentText = text
	m.sentMarkup = markup
	if m.sendMarkupErr != nil {
		return nil, m.sendMarkupErr
	}
	return &tg.Message{ID: 10, Message: text}, nil
}

func (m *deliveryMockService) SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error) {
	m.sendCount++
	m.sentText = text
	m.sentMarkup = nil
	return &tg.Message{ID: 10, Message: text}, nil
}

func TestDeliverHandoff_EditWithMarkup_Success(t *testing.T) {
	svc := &deliveryMockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	hRes := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/GoUltroidBot?start=token123",
	}

	err := render.DeliverHandoff(ctx, hRes, "Help Browser", "Open in assistant:", render.WithDeliveryMode(render.DeliveryEdit))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if svc.editMarkupCount != 1 {
		t.Fatalf("expected 1 EditMessageMarkup call, got %d", svc.editMarkupCount)
	}
	if svc.editedMarkup == nil {
		t.Fatal("expected non-nil markup on success")
	}
	if svc.editCount != 0 {
		t.Fatalf("expected 0 EditMessage fallback calls, got %d", svc.editCount)
	}
}

func TestDeliverHandoff_AutoUserbotUsesClickableTextWithoutMarkup(t *testing.T) {
	svc := &deliveryMockService{}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Source:  core.ExecutionInteractive,
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	hRes := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/GoUltroidBot?start=auto_userbot_token",
	}

	if err := render.DeliverHandoff(ctx, hRes, "Help Browser", "Open in assistant:"); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if svc.editMarkupCount != 0 || svc.sendMarkupCount != 0 {
		t.Fatalf("automatic userbot delivery must not attempt bot markup: edit=%d send=%d", svc.editMarkupCount, svc.sendMarkupCount)
	}
	if svc.editCount != 1 {
		t.Fatalf("expected one plain edit, got %d", svc.editCount)
	}
	if !strings.Contains(svc.editedText, `<a href="https://t.me/GoUltroidBot?start=auto_userbot_token">Open in Assistant</a>`) {
		t.Fatalf("expected clickable deep-link fallback, got: %s", svc.editedText)
	}
}

func TestDeliverHandoff_EditWithMarkup_FailureFallsBackToClickableLink(t *testing.T) {
	var logBuf bytes.Buffer
	encoder := zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
	writer := zapcore.AddSync(&logBuf)
	logger := zap.New(zapcore.NewCore(encoder, writer, zap.DebugLevel))

	svc := &deliveryMockService{
		editMarkupErr: errors.New("BOT_METHOD_INVALID"),
	}
	ctx := &core.Context{
		Ctx:     context.Background(),
		Command: "help",
		Svc:     svc,
		PeerID:  &tg.InputPeerSelf{},
		Message: &core.Message{ID: 1, IsOutgoing: true},
	}

	token := "token_secret_xyz123"
	hRes := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/GoUltroidBot?start=" + token,
	}

	err := render.DeliverHandoff(ctx, hRes, "Help Browser", "Open in assistant:",
		render.WithDeliveryMode(render.DeliveryEdit),
		render.WithLogger(logger),
	)
	if err != nil {
		t.Fatalf("unexpected error during fallback: %v", err)
	}

	// 1. Markup was attempted
	if svc.editMarkupCount != 1 {
		t.Fatalf("expected 1 EditMessageMarkup attempt, got %d", svc.editMarkupCount)
	}

	// 2. Fallback EditMessage was called
	if svc.editCount != 1 {
		t.Fatalf("expected 1 EditMessage fallback call, got %d", svc.editCount)
	}

	// 3. Fallback text contains clickable URL
	expectedURL := "https://t.me/GoUltroidBot?start=" + token
	if !strings.Contains(svc.editedText, expectedURL) {
		t.Fatalf("expected fallback text to contain deep-link URL %q, got: %s", expectedURL, svc.editedText)
	}
	if !strings.Contains(svc.editedText, "<a href=") {
		t.Fatalf("expected fallback text to contain HTML anchor tag, got: %s", svc.editedText)
	}

	// 4. Ensure token was NOT logged
	logged := logBuf.String()
	if strings.Contains(logged, token) {
		t.Fatalf("security violation: secret token leaked into logs: %s", logged)
	}
	if !strings.Contains(logged, "presentation markup delivery rejected") {
		t.Fatalf("expected markup rejection warning in log, got: %s", logged)
	}
}

func TestDeliverHandoff_ReplyWithMarkup_FailureFallsBackToClickableLink(t *testing.T) {
	svc := &deliveryMockService{
		sendMarkupErr: errors.New("REPLY_MARKUP_INVALID"),
	}
	ctx := &core.Context{
		Ctx:    context.Background(),
		Svc:    svc,
		PeerID: &tg.InputPeerSelf{},
	}

	hRes := presentation.HandoffResult{
		Mode:        presentation.HandoffDeepLink,
		DeepLinkURL: "https://t.me/GoUltroidBot?start=tok_reply_456",
	}

	err := render.DeliverHandoff(ctx, hRes, "MyXL", "Access account:", render.WithDeliveryMode(render.DeliveryReply))
	if err != nil {
		t.Fatalf("unexpected error during reply fallback: %v", err)
	}

	if svc.sendMarkupCount != 1 {
		t.Fatalf("expected 1 SendMessageWithMarkup attempt, got %d", svc.sendMarkupCount)
	}
	if svc.sendCount != 1 {
		t.Fatalf("expected 1 SendMessage fallback call, got %d", svc.sendCount)
	}
	if !strings.Contains(svc.sentText, "https://t.me/GoUltroidBot?start=tok_reply_456") {
		t.Fatalf("expected deep-link URL in sent text, got: %s", svc.sentText)
	}
	if !strings.Contains(svc.sentText, "Open in Assistant") {
		t.Fatalf("expected 'Open in Assistant' anchor text in sent text, got: %s", svc.sentText)
	}
}
