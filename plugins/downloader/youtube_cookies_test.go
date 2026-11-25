package downloader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/services/localization"
)

const testCookiesOwnerID int64 = 589287392

type cookiesTestTelegramService struct {
	*mockTelegramService
	getMsgCalls   int
	downloadCalls int
	downloadData  []byte
	downloadErr   error
	deleteCalls   [][]int
	deleteErr     error
	lastReply     string
	lastEdit      string
	events        *[]string
}

func newCookiesTestTelegramService(data []byte) *cookiesTestTelegramService {
	return &cookiesTestTelegramService{
		mockTelegramService: &mockTelegramService{},
		downloadData:        data,
	}
}

func (s *cookiesTestTelegramService) SendMessage(_ context.Context, _ tg.InputPeerClass, text string) (*tg.Message, error) {
	s.lastReply = text
	return &tg.Message{ID: 777}, nil
}

func (s *cookiesTestTelegramService) EditMessage(_ context.Context, _ tg.InputPeerClass, _ int, text string) error {
	s.lastEdit = text
	return nil
}

func (s *cookiesTestTelegramService) GetMessage(ctx context.Context, peer tg.InputPeerClass, id int) (*tg.Message, error) {
	s.getMsgCalls++
	return s.mockTelegramService.GetMessage(ctx, peer, id)
}

func (s *cookiesTestTelegramService) DownloadFile(ctx context.Context, _ tg.InputFileLocationClass, dstPath string) error {
	s.downloadCalls++
	if err := ctx.Err(); err != nil {
		return err
	}
	if s.downloadErr != nil {
		return s.downloadErr
	}
	return os.WriteFile(dstPath, s.downloadData, 0600)
}

func (s *cookiesTestTelegramService) DeleteMessage(_ context.Context, _ tg.InputPeerClass, msgIDs []int) error {
	if s.events != nil {
		*s.events = append(*s.events, "delete")
	}
	cp := append([]int(nil), msgIDs...)
	s.deleteCalls = append(s.deleteCalls, cp)
	if s.deleteErr != nil {
		return s.deleteErr
	}
	return nil
}

type recordingCookieSink struct {
	saved  [][]byte
	err    error
	events *[]string
}

func (r *recordingCookieSink) SaveValidatedCookies(_ context.Context, raw []byte) error {
	if r.events != nil {
		*r.events = append(*r.events, "save")
	}
	if r.err != nil {
		return r.err
	}
	r.saved = append(r.saved, append([]byte(nil), raw...))
	return nil
}

func validNetscapeCookiesFixture(now time.Time) []byte {
	exp := now.Add(24 * time.Hour).Unix()
	return []byte(fmt.Sprintf(
		"# Netscape HTTP Cookie File\n"+
			"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\tsecret_login_token_value\n"+
			".google.com\tTRUE\t/\tTRUE\t%d\t__Secure-1PSID\tsecret_psid_value\n",
		exp, exp,
	))
}

func newCookiesTestContext(svc *cookiesTestTelegramService, senderID, chatID int64, chatType string, peer tg.InputPeerClass, outgoing bool) *core.Context {
	return &core.Context{
		Ctx:      context.Background(),
		Telegram: core.TelegramCapabilitiesFrom(svc),
		Svc:      svc,
		Perms:    core.NewPermissions(testCookiesOwnerID, []int64{999001}),
		Chat: &core.Chat{
			ID:   chatID,
			Type: chatType,
		},
		Sender: &core.User{
			ID: senderID,
		},
		PeerID: peer,
		Message: &core.Message{
			ID:         100,
			SenderID:   senderID,
			IsOutgoing: outgoing,
		},
	}
}

func dummyDocumentMedia(size int64, mediaType string) *core.MediaInfo {
	if mediaType == "" {
		mediaType = "document"
	}
	return &core.MediaInfo{
		Type:     mediaType,
		FileName: "../../etc/passwd", // intentionally adversarial; must be ignored
		Size:     size,
		Location: &tg.InputDocumentFileLocation{ID: 12345},
	}
}

func replyTGDocument(fromID int64, out bool, size int64) *tg.Message {
	var fromPeer tg.PeerClass
	if fromID != 0 {
		fromPeer = &tg.PeerUser{UserID: fromID}
	}
	return &tg.Message{
		ID:     55,
		Out:    out,
		FromID: fromPeer,
		Media: &tg.MessageMediaDocument{
			Document: &tg.Document{
				ID:       98765,
				Size:     size,
				MimeType: "text/plain",
				Attributes: []tg.DocumentAttributeClass{
					&tg.DocumentAttributeFilename{FileName: "cookies.txt"},
				},
			},
		},
	}
}

func assertTempDirClean(t *testing.T, p *Plugin) {
	t.Helper()
	if p.files == nil {
		return
	}
	tempRoot, err := p.files.TempDir()
	if err != nil {
		t.Fatalf("TempDir() error = %v", err)
	}
	entries, err := os.ReadDir(tempRoot)
	if err != nil {
		t.Fatalf("ReadDir(%s) error = %v", tempRoot, err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected temp dir to be empty, found %d entries", len(entries))
	}
}

func TestYouTubeCookiesManifestAndCommandRegistration(t *testing.T) {
	caps := Module.Manifest().Capabilities
	if !slices.Contains(caps, plugin.CapTelegramDeleteMessage) {
		t.Fatalf("manifest capabilities %v missing %q", caps, plugin.CapTelegramDeleteMessage)
	}

	p := New()
	var found *core.Command
	for _, cmd := range p.Commands() {
		if cmd.Name == "ytcookies" {
			c := cmd
			found = &c
			break
		}
	}
	if found == nil {
		t.Fatal("expected ytcookies command in Plugin.Commands()")
	}
	if found.Permission != core.PermissionOwner || !found.PrivateOnly {
		t.Fatalf("ytcookies command = (Permission=%v, PrivateOnly=%v), want (PermissionOwner, true)", found.Permission, found.PrivateOnly)
	}
}

func TestYouTubeCookiesAuthorizationGate(t *testing.T) {
	validData := validNetscapeCookiesFixture(time.Now())

	cases := []struct {
		name     string
		senderID int64
		chatID   int64
		chatType string
		peer     tg.InputPeerClass
		outgoing bool
		withSelf bool
	}{
		{"non_owner_private", 999001, 999001, "private", &tg.InputPeerUser{UserID: 999001}, false, false},
		{"owner_other_user_dm_outgoing", testCookiesOwnerID, 888777, "private", &tg.InputPeerUser{UserID: 888777}, true, false},
		{"owner_other_user_dm_peer_mismatch", testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerUser{UserID: 888777}, true, false},
		{"owner_group", testCookiesOwnerID, -100123, "group", &tg.InputPeerChat{ChatID: 123}, true, false},
		{"owner_supergroup", testCookiesOwnerID, -100123456, "supergroup", &tg.InputPeerChannel{ChannelID: 123456}, true, false},
		{"owner_channel", testCookiesOwnerID, -100999999, "channel", &tg.InputPeerChannel{ChannelID: 999999}, true, false},
		{"missing_peer", testCookiesOwnerID, testCookiesOwnerID, "private", nil, true, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := newCookiesTestTelegramService(validData)
			svc.replyMessage = replyTGDocument(testCookiesOwnerID, true, int64(len(validData)))
			sink := &recordingCookieSink{}
			p := New()
			p.cookieSink = sink
			attachDownloaderTestFilesystem(t, p)

			ctx := newCookiesTestContext(svc, tc.senderID, tc.chatID, tc.chatType, tc.peer, tc.outgoing)
			ctx.Message.ReplyToID = 55
			if tc.withSelf {
				ctx.Self = &core.User{ID: testCookiesOwnerID}
			}

			err := p.handleYouTubeCookiesCommand(ctx)
			if !errors.Is(err, core.ErrPermissionDenied) {
				t.Fatalf("err = %v, want ErrPermissionDenied", err)
			}
			if svc.getMsgCalls != 0 {
				t.Fatalf("GetMessage called %d times, want 0 before auth gate", svc.getMsgCalls)
			}
			if svc.downloadCalls != 0 {
				t.Fatalf("DownloadFile called %d times, want 0 before auth gate", svc.downloadCalls)
			}
			if len(sink.saved) != 0 {
				t.Fatalf("sink.saved = %d, want 0", len(sink.saved))
			}
		})
	}
}

func TestYouTubeCookiesReplySenderVerification(t *testing.T) {
	validData := validNetscapeCookiesFixture(time.Now())

	t.Run("saved_messages_outgoing_reply_sender_zero_accepted", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		svc.replyMessage = replyTGDocument(0, true, int64(len(validData)))
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.ReplyToID = 55

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("handleYouTubeCookiesCommand() error = %v", err)
		}
		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1", len(sink.saved))
		}
		if len(svc.deleteCalls) != 1 || !slices.Equal(svc.deleteCalls[0], []int{100, 55}) {
			t.Fatalf("deleteCalls = %v, want [[100 55]]", svc.deleteCalls)
		}
		assertTempDirClean(t, p)
	})

	t.Run("saved_messages_incoming_owner_reply_accepted", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		svc.replyMessage = replyTGDocument(testCookiesOwnerID, false, int64(len(validData)))
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerUser{UserID: testCookiesOwnerID}, true)
		ctx.Message.ReplyToID = 55

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("handleYouTubeCookiesCommand() error = %v", err)
		}
		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1", len(sink.saved))
		}
		assertTempDirClean(t, p)
	})

	t.Run("reply_from_other_user_rejected_before_download", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		svc.replyMessage = replyTGDocument(777888, false, int64(len(validData)))
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.ReplyToID = 55

		err := p.handleYouTubeCookiesCommand(ctx)
		if !errors.Is(err, core.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if svc.downloadCalls != 0 || len(sink.saved) != 0 {
			t.Fatalf("downloadCalls=%d saved=%d, want 0/0", svc.downloadCalls, len(sink.saved))
		}
	})

	t.Run("reply_sender_zero_without_outgoing_rejected_before_download", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		svc.replyMessage = replyTGDocument(0, false, int64(len(validData)))
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.ReplyToID = 55

		err := p.handleYouTubeCookiesCommand(ctx)
		if !errors.Is(err, core.ErrPermissionDenied) {
			t.Fatalf("err = %v, want ErrPermissionDenied", err)
		}
		if svc.downloadCalls != 0 || len(sink.saved) != 0 {
			t.Fatalf("downloadCalls=%d saved=%d, want 0/0", svc.downloadCalls, len(sink.saved))
		}
	})
}

func TestYouTubeCookiesUploadMediaValidationAndCleanup(t *testing.T) {
	validData := validNetscapeCookiesFixture(time.Now())

	t.Run("caption_document_upload_succeeds_and_cleans_temp", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1", len(sink.saved))
		}
		if !strings.Contains(svc.lastEdit, "validated and accepted") {
			t.Fatalf("unexpected response = %q", svc.lastEdit)
		}
		assertTempDirClean(t, p)
	})

	t.Run("wrong_media_type_rejected_without_download", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		p := New()
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(1024, "photo")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if svc.downloadCalls != 0 {
			t.Fatalf("downloadCalls = %d, want 0", svc.downloadCalls)
		}
		if !strings.Contains(svc.lastEdit, "document") {
			t.Fatalf("response = %q, want wrong media message", svc.lastEdit)
		}
	})

	t.Run("oversized_declared_rejected_without_download", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		p := New()
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(maxCookieFileBytes+1, "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if svc.downloadCalls != 0 {
			t.Fatalf("downloadCalls = %d, want 0", svc.downloadCalls)
		}
		if !strings.Contains(svc.lastEdit, "too large") {
			t.Fatalf("response = %q, want too large message", svc.lastEdit)
		}
	})

	t.Run("empty_file_rejected_and_temp_cleaned", func(t *testing.T) {
		svc := newCookiesTestTelegramService(nil)
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(10, "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if len(sink.saved) != 0 {
			t.Fatalf("sink.saved = %d, want 0", len(sink.saved))
		}
		if !strings.Contains(svc.lastEdit, "empty") {
			t.Fatalf("response = %q, want empty message", svc.lastEdit)
		}
		assertTempDirClean(t, p)
	})

	t.Run("download_failure_and_cancellation_clean_temp_and_preserve_previous", func(t *testing.T) {
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		// First upload succeeds.
		okSvc := newCookiesTestTelegramService(validData)
		okCtx := newCookiesTestContext(okSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		okCtx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")
		if err := p.handleYouTubeCookiesCommand(okCtx); err != nil {
			t.Fatalf("initial upload error = %v", err)
		}

		// Second upload fails download.
		failSvc := newCookiesTestTelegramService(nil)
		failSvc.downloadErr = errors.New("network reset")
		failCtx := newCookiesTestContext(failSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		failCtx.Message.Media = dummyDocumentMedia(100, "document")
		_ = p.handleYouTubeCookiesCommand(failCtx)
		assertTempDirClean(t, p)

		// Third upload is cancelled.
		cancelSvc := newCookiesTestTelegramService(validData)
		cancelCtx := newCookiesTestContext(cancelSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		cctx, cancel := context.WithCancel(context.Background())
		cancel()
		cancelCtx.Ctx = cctx
		cancelCtx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")
		_ = p.handleYouTubeCookiesCommand(cancelCtx)
		assertTempDirClean(t, p)

		// Previous configuration remains untouched (only 1 saved entry).
		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1 (previous credential preserved)", len(sink.saved))
		}
	})

	t.Run("telegram_delete_failure_reported_honestly", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		svc.deleteErr = errors.New("MESSAGE_DELETE_FORBIDDEN")
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if strings.Contains(svc.lastEdit, "removed from chat") {
			t.Fatalf("must not claim message was deleted when DeleteMessage failed: %q", svc.lastEdit)
		}
		if !strings.Contains(svc.lastEdit, "Could not confirm Telegram message deletion") {
			t.Fatalf("expected honest delete-unconfirmed warning, got: %q", svc.lastEdit)
		}
		assertTempDirClean(t, p)
	})

	t.Run("invalid_replacement_preserves_previous_cookies", func(t *testing.T) {
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		okSvc := newCookiesTestTelegramService(validData)
		okCtx := newCookiesTestContext(okSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		okCtx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")
		if err := p.handleYouTubeCookiesCommand(okCtx); err != nil {
			t.Fatalf("initial upload error = %v", err)
		}

		badData := []byte("# Netscape HTTP Cookie File\n.youtube.com\tTRUE\t/\tTRUE\t1999999999\tCONSENT\tYES+1\n")
		badSvc := newCookiesTestTelegramService(badData)
		badCtx := newCookiesTestContext(badSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		badCtx.Message.Media = dummyDocumentMedia(int64(len(badData)), "document")
		_ = p.handleYouTubeCookiesCommand(badCtx)

		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1", len(sink.saved))
		}
		assertTempDirClean(t, p)
	})

	t.Run("nil_cookie_sink_fails_closed_and_does_not_delete_upload", func(t *testing.T) {
		svc := newCookiesTestTelegramService(validData)
		p := New() // p.cookieSink is nil
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if len(svc.deleteCalls) != 0 {
			t.Fatalf("deleteCalls = %v, want 0 when cookieSink is nil", svc.deleteCalls)
		}
		if strings.Contains(svc.lastEdit, "validated and accepted") {
			t.Fatalf("must not claim cookies accepted when cookieSink is nil: %q", svc.lastEdit)
		}
		if !strings.Contains(svc.lastEdit, "unavailable") {
			t.Fatalf("expected storage unavailable message, got: %q", svc.lastEdit)
		}
		assertTempDirClean(t, p)
	})

	t.Run("save_failure_preserves_upload_message_and_previous_cookies", func(t *testing.T) {
		sink := &recordingCookieSink{}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		// First upload succeeds.
		okSvc := newCookiesTestTelegramService(validData)
		okCtx := newCookiesTestContext(okSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		okCtx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")
		if err := p.handleYouTubeCookiesCommand(okCtx); err != nil {
			t.Fatalf("initial upload error = %v", err)
		}

		// Second upload fails in SaveValidatedCookies.
		sink.err = errors.New("disk full")
		failSvc := newCookiesTestTelegramService(validData)
		failCtx := newCookiesTestContext(failSvc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		failCtx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")
		if err := p.handleYouTubeCookiesCommand(failCtx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if len(failSvc.deleteCalls) != 0 {
			t.Fatalf("deleteCalls = %v, want 0 when SaveValidatedCookies fails", failSvc.deleteCalls)
		}
		if strings.Contains(failSvc.lastEdit, "validated and accepted") {
			t.Fatalf("must not claim cookies accepted when SaveValidatedCookies fails: %q", failSvc.lastEdit)
		}
		if len(sink.saved) != 1 {
			t.Fatalf("sink.saved = %d, want 1 (previous preserved)", len(sink.saved))
		}
		assertTempDirClean(t, p)
	})

	t.Run("persist_runs_before_telegram_delete", func(t *testing.T) {
		var events []string
		svc := newCookiesTestTelegramService(validData)
		svc.events = &events
		sink := &recordingCookieSink{events: &events}
		p := New()
		p.cookieSink = sink
		attachDownloaderTestFilesystem(t, p)

		ctx := newCookiesTestContext(svc, testCookiesOwnerID, testCookiesOwnerID, "private", &tg.InputPeerSelf{}, true)
		ctx.Message.Media = dummyDocumentMedia(int64(len(validData)), "document")

		if err := p.handleYouTubeCookiesCommand(ctx); err != nil {
			t.Fatalf("error = %v", err)
		}
		if !slices.Equal(events, []string{"save", "delete"}) {
			t.Fatalf("operation order = %v, want [save delete]", events)
		}
		assertTempDirClean(t, p)
	})
}

func TestParseAndValidateYouTubeCookies(t *testing.T) {
	now := time.Unix(1795600000, 0)
	future := now.Add(48 * time.Hour).Unix()
	past := now.Add(-1 * time.Hour).Unix()

	cases := []struct {
		name    string
		raw     string
		wantErr error
	}{
		{
			name: "valid_youtube_and_google_auth",
			raw: fmt.Sprintf(
				"# Netscape HTTP Cookie File\n"+
					"#HttpOnly_.youtube.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\tsecret123\n"+
					".google.com\tTRUE\t/\tTRUE\t%d\t__Secure-1PSID\tsecret456\n",
				future, future,
			),
			wantErr: nil,
		},
		{
			name:    "valid_session_expiry_zero",
			raw:     ".youtube.com\tTRUE\t/\tTRUE\t0\tSAPISID\tsapisid_val\n",
			wantErr: nil,
		},
		{
			name:    "valid_host_only_youtube_exact_match",
			raw:     fmt.Sprintf("youtube.com\tFALSE\t/\tTRUE\t%d\tLOGIN_INFO\tval1\nwww.youtube.com\tFALSE\t/\tTRUE\t%d\tSID\tval2\n", future, future),
			wantErr: nil,
		},
		{
			name:    "host_only_m_youtube_rejected_as_www_youtube_auth",
			raw:     fmt.Sprintf("m.youtube.com\tFALSE\t/\tTRUE\t%d\tLOGIN_INFO\tval_mobile\n", future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "host_only_accounts_google_rejected_as_youtube_auth",
			raw:     fmt.Sprintf("accounts.google.com\tFALSE\t/\tTRUE\t%d\t__Secure-1PSID\tval_accounts\n", future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "inconsistent_leading_dot_with_false_flag_rejected",
			raw:     fmt.Sprintf(".youtube.com\tFALSE\t/\tTRUE\t%d\tLOGIN_INFO\tsecret123\n", future),
			wantErr: errCookieInvalidFormat,
		},
		{
			name:    "inconsistent_host_only_with_true_flag_rejected",
			raw:     fmt.Sprintf("youtube.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\tsecret123\n", future),
			wantErr: errCookieInvalidFormat,
		},
		{
			name:    "consent_and_preference_only_rejected",
			raw:     fmt.Sprintf(".youtube.com\tTRUE\t/\tTRUE\t%d\tCONSENT\tYES+cb\n.youtube.com\tTRUE\t/\tFALSE\t%d\tPREF\ttz=UTC\n.google.com\tTRUE\t/\tTRUE\t%d\tSOCS\tCAISHAg\n.youtube.com\tTRUE\t/\tTRUE\t%d\tVISITOR_INFO1_LIVE\tabc\n", future, future, future, future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "auth_cookie_on_mismatched_domain_youtu_be_rejected",
			raw:     fmt.Sprintf(".youtu.be\tTRUE\t/\tTRUE\t%d\tSID\tsome_sid_val\n", future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "login_info_on_google_com_rejected",
			raw:     fmt.Sprintf(".google.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\tsome_val\n", future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "lookalike_domains_rejected",
			raw:     fmt.Sprintf(".evil-youtube.com\tTRUE\t/\tTRUE\t%d\tSID\tval\n.google.com.evil.org\tTRUE\t/\tTRUE\t%d\t__Secure-1PSID\tval\nmyyoutube.com\tFALSE\t/\tTRUE\t%d\tLOGIN_INFO\tval\n", future, future, future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "expired_auth_cookie_rejected",
			raw:     fmt.Sprintf(".youtube.com\tTRUE\t/\tTRUE\t%d\tLOGIN_INFO\texpired_val\n", past),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "empty_auth_cookie_value_rejected",
			raw:     fmt.Sprintf(".youtube.com\tTRUE\t/\tTRUE\t%d\tSID\t\n", future),
			wantErr: errCookieNoYouTubeAuth,
		},
		{
			name:    "empty_file",
			raw:     "   \n\t\n",
			wantErr: errCookieEmpty,
		},
		{
			name:    "json_format_rejected",
			raw:     `[{"name":"SID","value":"secret","domain":".youtube.com"}]`,
			wantErr: errCookieInvalidFormat,
		},
		{
			name:    "malformed_columns_rejected",
			raw:     ".youtube.com\tTRUE\t/\tTRUE\n",
			wantErr: errCookieInvalidFormat,
		},
		{
			name:    "binary_nul_rejected",
			raw:     ".youtube.com\tTRUE\t/\tTRUE\t0\tSID\tval\x00",
			wantErr: errCookieInvalidFormat,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := parseAndValidateYouTubeCookies([]byte(tc.raw), now)
			if tc.wantErr == nil && err != nil {
				t.Fatalf("parseAndValidateYouTubeCookies() = %v, want nil", err)
			}
			if tc.wantErr != nil && !errors.Is(err, tc.wantErr) {
				t.Fatalf("parseAndValidateYouTubeCookies() = %v, want %v", err, tc.wantErr)
			}
			if err != nil {
				// Ensure secret values or cookie names are never leaked in error strings.
				for _, secret := range []string{"secret123", "secret456", "expired_val", "some_sid_val", "LOGIN_INFO", "__Secure-1PSID"} {
					if strings.Contains(err.Error(), secret) {
						t.Fatalf("error leaked %q: %v", secret, err)
					}
				}
			}
		})
	}
}

func TestCookieCoversHostScope(t *testing.T) {
	// Domain cookie (.youtube.com, includeSubdomains=TRUE) covers youtube.com and subdomains.
	if !cookieCoversHost("youtube.com", "youtube.com", true) {
		t.Fatal("domain cookie .youtube.com should cover youtube.com")
	}
	if !cookieCoversHost("youtube.com", "www.youtube.com", true) {
		t.Fatal("domain cookie .youtube.com should cover www.youtube.com")
	}
	if !cookieCoversHost("youtube.com", "m.youtube.com", true) {
		t.Fatal("domain cookie .youtube.com should cover m.youtube.com")
	}

	// Host-only cookie (includeSubdomains=FALSE) matches ONLY its exact host.
	if !cookieCoversHost("youtube.com", "youtube.com", false) {
		t.Fatal("host-only youtube.com should match youtube.com")
	}
	if cookieCoversHost("youtube.com", "www.youtube.com", false) {
		t.Fatal("host-only youtube.com must NOT match www.youtube.com")
	}
	if cookieCoversHost("m.youtube.com", "www.youtube.com", false) {
		t.Fatal("host-only m.youtube.com must NOT match www.youtube.com")
	}
	if cookieCoversHost("accounts.google.com", "google.com", false) {
		t.Fatal("host-only accounts.google.com must NOT match google.com")
	}

	// Lookalike domains must never match.
	if cookieCoversHost("evil-youtube.com", "www.youtube.com", true) {
		t.Fatal("lookalike evil-youtube.com must NOT match www.youtube.com")
	}
	if cookieCoversHost("youtube.com.evil.org", "youtube.com", true) {
		t.Fatal("lookalike youtube.com.evil.org must NOT match youtube.com")
	}
}

func TestYouTubeCookiesLocalizationENAndID(t *testing.T) {
	keys := []string{
		"downloader.cookies.unauthorized",
		"downloader.cookies.usage",
		"downloader.cookies.wrong_media",
		"downloader.cookies.too_large",
		"downloader.cookies.empty",
		"downloader.cookies.invalid_format",
		"downloader.cookies.no_youtube_auth",
		"downloader.cookies.storage_unavailable",
		"downloader.cookies.download_failed",
		"downloader.cookies.accepted",
		"downloader.cookies.accepted_delete_unconfirmed",
		"downloader.cookies.delete_unconfirmed_suffix",
		"downloader.cookies.management_pending",
	}
	for _, key := range keys {
		en := downloaderTR(localization.LocaleEnglish, key)
		id := downloaderTR(localization.LocaleIndonesian, key)
		if en == "" || id == "" || en == key || id == key {
			t.Fatalf("missing translation for %s: en=%q id=%q", key, en, id)
		}
		if en == id {
			t.Fatalf("expected distinct EN and ID translations for %s, both=%q", key, en)
		}
	}
}
