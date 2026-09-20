package quote

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"golang.org/x/image/font"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/platform/filesystem"
	"github.com/inipew/goultroid/internal/plugin"
)

func TestDisplayUserName(t *testing.T) {
	if got := displayUserName("Alice", "Wonderland", "alice"); got != "Alice Wonderland" {
		t.Fatalf("unexpected full name: %q", got)
	}
	if got := displayUserName("", "", "alice"); got != "@alice" {
		t.Fatalf("unexpected username fallback: %q", got)
	}
	if got := displayUserName("", "", ""); got != "" {
		t.Fatalf("expected empty name, got %q", got)
	}
}

func TestMediaPlaceholder(t *testing.T) {
	if got := mediaPlaceholder("photo"); got != "[Photo]" {
		t.Fatalf("unexpected photo placeholder: %q", got)
	}
	if got := mediaPlaceholder("voice"); got != "[Voice message]" {
		t.Fatalf("unexpected voice placeholder: %q", got)
	}
	if got := mediaPlaceholder("unknown"); got != "[Media]" {
		t.Fatalf("unexpected fallback placeholder: %q", got)
	}
}

func TestPeerToInputUser(t *testing.T) {
	peer := &tg.InputPeerUser{UserID: 123, AccessHash: 456}
	user, ok := peerToInputUser(peer)
	if !ok || user == nil {
		t.Fatal("expected a usable InputUser")
	}
	got, ok := user.(*tg.InputUser)
	if !ok || got.UserID != 123 || got.AccessHash != 456 {
		t.Fatalf("unexpected InputUser: %#v", user)
	}

	if _, ok := peerToInputUser(&tg.InputPeerUser{UserID: 123}); ok {
		t.Fatal("expected zero access hash to be rejected")
	}
}

func TestRenderPreservesUnicodePath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote.jpg")
	text := "Halo dunia 👋 — Indonesia 日本語 العربية"
	msg := &core.Message{Date: time.Date(2026, 9, 8, 12, 0, 0, 0, time.UTC)}
	if err := render(path, "Dhimas", text, msg, ""); err != nil {
		t.Fatalf("render failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("rendered file missing: %v", err)
	}
	if info.Size() == 0 {
		t.Fatal("rendered file is empty")
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := jpeg.Decode(f); err != nil {
		t.Fatalf("rendered file is not a valid JPEG: %v", err)
	}

	face := loadFont("regular", 24)
	faces := styledFaces{normal: face, bold: face, italic: face, code: face}
	lines := wrapStyledSegments(styledSegments(strings.Repeat("Unicode ", 100), nil), faces, 804)
	if len(lines) < 2 {
		t.Fatal("expected long text to wrap into multiple lines")
	}
}

func TestTelegramChatBubbleRender(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_bubble.png")

	err := renderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Kobo Kanaeru [DC2]",
		Badge:     "Bot Mirror",
		Text:      "Memproses link...",
		SenderID:  4, // Cyan
		Timestamp: "10.44",
		ReplyPreview: &ReplyPreview{
			Author: "",
			Text:   "Deleted message",
		},
	})
	if err != nil {
		t.Fatalf("renderV3WithOpts failed: %v", err)
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("failed to open output: %v", err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode rendered PNG: %v", err)
	}

	b := img.Bounds()
	if b.Dx() < 300 || b.Dy() < 100 {
		t.Fatalf("image bounds unexpectedly small: %v", b)
	}

	// Verify chat background color at top-left
	r, g, bVal, a := img.At(0, 0).RGBA()
	r8, g8, b8, a8 := uint8(r>>8), uint8(g>>8), uint8(bVal>>8), uint8(a>>8)
	if r8 != 14 || g8 != 22 || b8 != 33 || a8 != 255 {
		t.Fatalf("expected canvas background #0e1621ff, got #%02x%02x%02x%02x", r8, g8, b8, a8)
	}
}

func TestRenderWithReplyAuthorAndText(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_reply.png")

	err := renderV3WithOpts(RenderOptions{
		Path:     path,
		Name:     "Alice",
		Badge:    "admin",
		Text:     "Check this message",
		SenderID: 12345,
		ReplyPreview: &ReplyPreview{
			Author: "Bob",
			Text:   "Original question here",
		},
	})
	if err != nil {
		t.Fatalf("render with reply author failed: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("output file invalid: %v", err)
	}
}

func TestStyledSegmentsEntities(t *testing.T) {
	text := "Hello bold italic code"
	entities := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 6, Length: 4},
		&tg.MessageEntityItalic{Offset: 11, Length: 6},
		&tg.MessageEntityCode{Offset: 18, Length: 4},
	}
	segs := styledSegments(text, entities)
	if len(segs) < 4 {
		t.Fatalf("expected at least 4 styled segments, got %d", len(segs))
	}
	if segs[0].style != styleNormal || segs[0].text != "Hello " {
		t.Errorf("seg 0 mismatch: %+v", segs[0])
	}
	if segs[1].style != styleBold || segs[1].text != "bold" {
		t.Errorf("seg 1 mismatch: %+v", segs[1])
	}
	if segs[2].style != styleNormal {
		t.Errorf("seg 2 mismatch: %+v", segs[2])
	}
	if segs[3].style != styleItalic || segs[3].text != "italic" {
		t.Errorf("seg 3 mismatch: %+v", segs[3])
	}
}

func TestTelegramNameColorsAndAbsInt64(t *testing.T) {
	if absInt64(-42) != 42 || absInt64(42) != 42 || absInt64(0) != 0 {
		t.Fatal("absInt64 failed")
	}

	for i := -14; i <= 14; i++ {
		idx := int(absInt64(int64(i)) % 7)
		if idx < 0 || idx >= len(telegramNameColors) {
			t.Fatalf("invalid color index %d for input %d", idx, i)
		}
	}
}

func TestFontLoading(t *testing.T) {
	regular := loadFont("regular", 24)
	if regular == nil {
		t.Fatal("regular font face is nil")
	}
	bold := loadFont("bold", 24)
	if bold == nil {
		t.Fatal("bold font face is nil")
	}
	italic := loadFont("italic", 24)
	if italic == nil {
		t.Fatal("italic font face is nil")
	}
	mono := loadFont("mono", 24)
	if mono == nil {
		t.Fatal("mono font face is nil")
	}
}

func TestRenderFullReferenceSample(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_sample.png")
	msg := &core.Message{
		SenderID: 4,
		Date:     time.Date(2026, 9, 8, 10, 44, 0, 0, time.Local),
	}
	err := RenderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Kobo Kanaeru [DC2]",
		Badge:     "Bot Mirror",
		Text:      "Memproses link...",
		Message:   msg,
		SenderID:  4,
		Timestamp: "10.44",
		ReplyPreview: &ReplyPreview{
			Author: "",
			Text:   "Deleted message",
		},
	})
	if err != nil {
		t.Fatalf("failed to render reference sample: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("rendered reference sample file is missing or empty")
	}
}

func TestSymbolsRendering(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "quote_symbols.png")
	symbolsText := "Status: ✓ Success • ✗ Failed • ★ Top • ⚡ Fast • ❤ Love • ➜ Next • ∞ Loop • ₹100 • ₿1"
	err := RenderV3WithOpts(RenderOptions{
		Path:      path,
		Name:      "Symbols Test ⚡",
		Badge:     "VIP ★",
		Text:      symbolsText,
		SenderID:  2,
		Timestamp: "12.30",
	})
	if err != nil {
		t.Fatalf("failed to render symbols: %v", err)
	}

	info, err := os.Stat(path)
	if err != nil || info.Size() == 0 {
		t.Fatalf("rendered symbols file is missing or empty")
	}

	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	img, err := png.Decode(f)
	if err != nil {
		t.Fatalf("failed to decode rendered symbols PNG: %v", err)
	}
	if img.Bounds().Dx() < 400 || img.Bounds().Dy() < 100 {
		t.Fatalf("unexpected image bounds: %v", img.Bounds())
	}
}

func TestQuoteCanPreviewImage(t *testing.T) {
	if !quoteCanPreviewImage(&core.MediaInfo{Type: "photo"}) {
		t.Fatal("photo should be previewable")
	}
	if !quoteCanPreviewImage(&core.MediaInfo{Type: "document", MimeType: "image/png"}) {
		t.Fatal("image document should be previewable")
	}
	if quoteCanPreviewImage(&core.MediaInfo{Type: "video", MimeType: "video/mp4"}) {
		t.Fatal("video must use quote placeholder without image download")
	}
	if quoteCanPreviewImage(&core.MediaInfo{Type: "document", MimeType: "application/pdf"}) {
		t.Fatal("non-image document must use quote placeholder")
	}
}

func TestLoadQuoteMediaBoundsPreviewDimensions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "portrait.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, 700, 1400))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	img, kind := loadQuoteMedia(path, &core.MediaInfo{Type: "photo"})
	if img == nil {
		t.Fatal("expected quote media preview")
	}
	if kind != "photo" {
		t.Fatalf("kind=%q, want photo", kind)
	}
	b := img.Bounds()
	if b.Dx() > quoteMediaPreviewMaxWidth || b.Dy() > quoteMediaPreviewMaxHeight {
		t.Fatalf("preview exceeds bounds: %v", b)
	}
	if b.Dx() != 360 || b.Dy() != 720 {
		t.Fatalf("unexpected fitted size %dx%d", b.Dx(), b.Dy())
	}
}

func TestLoadQuoteMediaRejectsUnsafeDimensions(t *testing.T) {
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

	img, kind := loadQuoteMedia(path, &core.MediaInfo{Type: "photo"})
	if img != nil {
		t.Fatal("unsafe media should be rejected before full decode")
	}
	if kind != "photo" {
		t.Fatalf("kind=%q, want photo", kind)
	}
}

func TestQuotePluginRequiresTempFilesystemCapability(t *testing.T) {
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	gate := plugin.NewCapabilityGate()
	gate.Register("quote", []string{})

	p := New()
	denied := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner: "quote",
		Gate:  gate,
		Files: manager,
	})
	if err := p.InitPlugin(denied); err == nil {
		t.Fatal("expected InitPlugin to reject missing filesystem.temp capability")
	}

	gate.Register("quote", []string{plugin.CapFilesystemTemp})
	granted := plugin.NewPluginContext(context.Background(), plugin.ContextConfig{
		Owner: "quote",
		Gate:  gate,
		Files: manager,
	})
	if err := p.InitPlugin(granted); err != nil {
		t.Fatalf("InitPlugin with filesystem.temp failed: %v", err)
	}
	if p.files == nil {
		t.Fatal("filesystem scope was not installed")
	}
}

func TestQuoteCommandResources(t *testing.T) {
	cmds := New().Commands()
	if len(cmds) != 1 {
		t.Fatalf("expected one quote command, got %d", len(cmds))
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

type concurrentQuoteService struct {
	core.MockTelegramServicer
	mu        sync.Mutex
	sentPaths []string
	payloads  [][]byte
}

func (s *concurrentQuoteService) GetMessage(_ context.Context, _ tg.InputPeerClass, msgID int) (*tg.Message, error) {
	return &tg.Message{
		ID:      msgID,
		Message: "same quoted message",
		Date:    int(time.Now().Unix()),
	}, nil
}

func (s *concurrentQuoteService) SendMedia(_ context.Context, _ tg.InputPeerClass, mediaType, filePath, _ string) (*tg.Message, error) {
	if mediaType != "photo" {
		return nil, fmt.Errorf("unexpected media type %q", mediaType)
	}
	payload, err := os.ReadFile(filePath)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.sentPaths = append(s.sentPaths, filePath)
	s.payloads = append(s.payloads, payload)
	id := len(s.sentPaths)
	s.mu.Unlock()
	return &tg.Message{ID: id}, nil
}

func TestQuoteConcurrentHandlersUseIsolatedWorkspacesAndCleanup(t *testing.T) {
	base := t.TempDir()
	manager, err := filesystem.NewManager(base, "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetFiles(manager)
	root, err := manager.ForOwner("quote").TempDir()
	if err != nil {
		t.Fatal(err)
	}

	svc := &concurrentQuoteService{}
	const callers = 2
	var wg sync.WaitGroup
	errCh := make(chan error, callers)
	wg.Add(callers)
	for i := range callers {
		go func(commandID int) {
			defer wg.Done()
			ctx := &core.Context{
				Ctx:    context.Background(),
				PeerID: &tg.InputPeerChat{ChatID: 100},
				Message: &core.Message{
					ID:         commandID + 1,
					ReplyToID:  77,
					IsOutgoing: true,
				},
				Svc: svc,
			}
			errCh <- p.handle(ctx)
		}(i)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil {
			t.Fatalf("concurrent quote handler failed: %v", err)
		}
	}

	svc.mu.Lock()
	paths := append([]string(nil), svc.sentPaths...)
	payloads := append([][]byte(nil), svc.payloads...)
	svc.mu.Unlock()
	if len(paths) != callers {
		t.Fatalf("sent paths=%d, want %d", len(paths), callers)
	}
	if paths[0] == paths[1] {
		t.Fatalf("concurrent quote outputs collided at %q", paths[0])
	}
	for i, path := range paths {
		if filepath.Base(path) != "quote.png" {
			t.Fatalf("output %d filename=%q, want quote.png", i, filepath.Base(path))
		}
		rel, err := filepath.Rel(root, path)
		if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			t.Fatalf("output %d escaped quote temp root: root=%q path=%q rel=%q err=%v", i, root, path, rel, err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("output %d still exists after handler cleanup: %q err=%v", i, path, err)
		}
		if len(payloads[i]) == 0 {
			t.Fatalf("output %d was empty when SendMedia read it", i)
		}
	}
	if filepath.Dir(paths[0]) == filepath.Dir(paths[1]) {
		t.Fatalf("concurrent handlers shared workspace %q", filepath.Dir(paths[0]))
	}
}

func TestQuoteWorkspaceCleanupDoesNotAffectSibling(t *testing.T) {
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetFiles(manager)

	first, err := p.createWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	second, err := p.createWorkspace()
	if err != nil {
		_ = p.files.RemoveTempDir(first)
		t.Fatal(err)
	}
	defer func() { _ = p.files.RemoveTempDir(second) }()

	firstMedia, err := p.workspacePath(first, "same-name.png")
	if err != nil {
		t.Fatal(err)
	}
	secondMedia, err := p.workspacePath(second, "same-name.png")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(firstMedia, []byte("first"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(secondMedia, []byte("second"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := p.files.RemoveTempDir(first); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(secondMedia)
	if err != nil {
		t.Fatalf("cleaning first workspace affected sibling: %v", err)
	}
	if string(data) != "second" {
		t.Fatalf("sibling workspace content changed: %q", data)
	}
}

func TestStyledSegmentsUsesUTF16BoundariesForAstralRunes(t *testing.T) {
	text := "A😀 bold"
	segments := styledSegments(text, []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 4, Length: 4},
	})
	found := false
	for _, segment := range segments {
		if segment.text == "bold" {
			found = true
			if segment.style != styleBold {
				t.Fatalf("bold segment style=%v", segment.style)
			}
		}
	}
	if !found {
		t.Fatalf("bold segment not found: %+v", segments)
	}
}

func TestStyledSegmentsRejectsEntitySplittingSurrogatePair(t *testing.T) {
	segments := styledSegments("A😀B", []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 2, Length: 1},
	})
	for _, segment := range segments {
		if segment.style != styleNormal {
			t.Fatalf("surrogate-splitting entity must be ignored: %+v", segments)
		}
	}
}

func TestWrapStyledSegmentsPreservesWhitespaceAndBlankLines(t *testing.T) {
	face := loadFont("regular", 24)
	faces := styledFaces{normal: face, bold: face, italic: face, code: face}
	lines := wrapStyledSegments(styledSegments("A  B\tC\n\nD", nil), faces, 1000)
	if len(lines) != 3 {
		t.Fatalf("lines=%d, want 3: %+v", len(lines), lines)
	}
	if got := styledLineText(lines[0]); got != "A  B    C" {
		t.Fatalf("first line=%q, want preserved whitespace", got)
	}
	if got := styledLineText(lines[1]); got != "" {
		t.Fatalf("blank line=%q, want empty", got)
	}
	if got := styledLineText(lines[2]); got != "D" {
		t.Fatalf("last line=%q, want D", got)
	}
}

func TestWrapStyledSegmentsHardWrapsLongTokenWithinWidth(t *testing.T) {
	face := loadFont("regular", 24)
	faces := styledFaces{normal: face, bold: face, italic: face, code: face}
	maxWidth := font.MeasureString(face, "abcdefgh").Ceil()
	lines := wrapStyledSegments(styledSegments(strings.Repeat("x", 200), nil), faces, maxWidth)
	if len(lines) < 2 {
		t.Fatalf("expected hard wrapping, got %d line(s)", len(lines))
	}
	for i, line := range lines {
		if width := measureStyledLine(line, faces); width > maxWidth {
			t.Fatalf("line %d width=%d exceeds %d", i, width, maxWidth)
		}
	}
}

func TestWrapStyledSegmentsMeasuresActualStyleFace(t *testing.T) {
	normal := loadFont("regular", 27)
	bold := loadFont("bold", 27)
	faces := styledFaces{normal: normal, bold: bold, italic: normal, code: normal}
	text := strings.Repeat("W", 24)
	segments := []styledSegment{{text: text, style: styleBold}}
	maxWidth := font.MeasureString(bold, text[:12]).Ceil()
	lines := wrapStyledSegments(segments, faces, maxWidth)
	for i, line := range lines {
		if width := measureStyledLine(line, faces); width > maxWidth {
			t.Fatalf("styled line %d width=%d exceeds %d", i, width, maxWidth)
		}
	}
}

func TestRenderFontPoolProvidesExclusiveSets(t *testing.T) {
	first := acquireRenderFonts()
	second := acquireRenderFonts()
	if first == second {
		t.Fatal("concurrent borrowers received the same render font set")
	}
	releaseRenderFonts(first)
	releaseRenderFonts(second)
}

func TestResizeAvatarCoverCenterCropsWithoutDistortion(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			if x < 2 {
				src.Set(x, y, color.RGBA{R: 255, A: 255})
			} else {
				src.Set(x, y, color.RGBA{B: 255, A: 255})
			}
		}
	}
	got := resizeAvatarCover(src, 8)
	if got == nil || got.Bounds().Dx() != 8 || got.Bounds().Dy() != 8 {
		t.Fatalf("unexpected avatar result: %v", got)
	}
	leftR, _, leftB, _ := got.At(0, 4).RGBA()
	rightR, _, rightB, _ := got.At(7, 4).RGBA()
	if leftR <= leftB || rightB <= rightR {
		t.Fatalf("center crop lost left/right image content")
	}
}

func styledLineText(line styledLine) string {
	var b strings.Builder
	for _, segment := range line.segments {
		b.WriteString(segment.text)
	}
	return b.String()
}

func TestBoundedQuoteTextPreservesPrefixAndCapsLogicalLines(t *testing.T) {
	input := "  😀 bold\n" + strings.Repeat("line\n", maxQuoteLogicalLines+5)
	got := boundedQuoteText(input)
	if !strings.HasPrefix(got, "  😀 bold\n") {
		t.Fatalf("quote prefix changed: %q", got[:min(len(got), 20)])
	}
	if !strings.HasSuffix(got, "…") {
		t.Fatal("bounded quote text should signal truncation")
	}
	if lines := strings.Count(got, "\n") + 1; lines > maxQuoteLogicalLines {
		t.Fatalf("logical lines=%d, max=%d", lines, maxQuoteLogicalLines)
	}

	carriageReturns := boundedQuoteText(strings.Repeat("line\r", maxQuoteLogicalLines+5))
	if !strings.HasSuffix(carriageReturns, "…") {
		t.Fatal("carriage-return logical lines must also be bounded")
	}

	crlf := boundedQuoteText(strings.Repeat("line\r\n", maxQuoteLogicalLines+5))
	if !strings.HasSuffix(crlf, "…") {
		t.Fatal("CRLF logical lines must be bounded")
	}

	long := strings.Repeat("界", maxQuoteTextRunes+50)
	got = boundedQuoteText(long)
	if runes := len([]rune(got)); runes != maxQuoteTextRunes+1 {
		t.Fatalf("bounded runes=%d, want %d including ellipsis", runes, maxQuoteTextRunes+1)
	}
}

func TestRenderBoundsPathologicalLogicalLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bounded-quote.png")
	if err := RenderV3WithOpts(RenderOptions{
		Path: path,
		Name: "Alice",
		Text: strings.Repeat("line\n", 5000),
	}); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	img, err := png.Decode(f)
	if err != nil {
		t.Fatal(err)
	}
	if height := img.Bounds().Dy(); height > 1800 {
		t.Fatalf("bounded quote canvas height=%d, want <=1800", height)
	}
}

func TestStyledSegmentsClipsEntityAtBoundedTextEnd(t *testing.T) {
	text := "A😀bold"
	segments := styledSegments(text, []tg.MessageEntityClass{
		// Starts at UTF-16 offset 3 (after A + 😀) and extends beyond this
		// bounded prefix. The visible suffix should stay bold.
		&tg.MessageEntityBold{Offset: 3, Length: 100},
	})
	var found bool
	for _, segment := range segments {
		if segment.text == "bold" {
			found = true
			if segment.style != styleBold {
				t.Fatalf("clipped segment style=%v, want bold", segment.style)
			}
		}
	}
	if !found {
		t.Fatalf("clipped styled suffix not found: %+v", segments)
	}
}

type quoteMediaSelectionService struct {
	core.MockTelegramServicer
	payload      []byte
	downloadedID int64
}

func (s *quoteMediaSelectionService) DownloadFile(
	_ context.Context,
	location tg.InputFileLocationClass,
	dstPath string,
) error {
	if document, ok := location.(*tg.InputDocumentFileLocation); ok {
		s.downloadedID = document.ID
	}
	return os.WriteFile(dstPath, s.payload, 0o600)
}

func TestDownloadQuotedMediaUsesReplyAttachmentNotCommandAttachment(t *testing.T) {
	var payload bytes.Buffer
	if err := png.Encode(&payload, image.NewRGBA(image.Rect(0, 0, 64, 64))); err != nil {
		t.Fatal(err)
	}
	svc := &quoteMediaSelectionService{payload: payload.Bytes()}
	ctx := &core.Context{
		Ctx: context.Background(),
		Svc: svc,
		Message: &core.Message{
			ID: 1,
			Media: &core.MediaInfo{
				Type: "document", MimeType: "image/png", FileName: "command.png",
				Location: &tg.InputDocumentFileLocation{ID: 111},
			},
		},
	}
	reply := &core.Message{
		ID: 2,
		Media: &core.MediaInfo{
			Type: "document", MimeType: "image/png", FileName: "reply.png",
			Location: &tg.InputDocumentFileLocation{ID: 222},
		},
	}
	manager, err := filesystem.NewManager(t.TempDir(), "", "", nil)
	if err != nil {
		t.Fatal(err)
	}
	p := New()
	p.SetFiles(manager)
	workspace, err := p.createWorkspace()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = p.files.RemoveTempDir(workspace) }()

	path := p.downloadQuotedMedia(ctx, reply, workspace)
	if path == "" {
		t.Fatal("expected quoted media preview download")
	}
	if svc.downloadedID != 222 {
		t.Fatalf("downloaded document ID=%d, want replied media ID 222", svc.downloadedID)
	}
	if filepath.Base(path) != "reply.png" {
		t.Fatalf("downloaded path=%q, want reply.png", path)
	}
}

type replyPreviewService struct {
	core.MockTelegramServicer
	message *tg.Message
}

func (s *replyPreviewService) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	return s.message, nil
}

func TestResolveReplyPreviewUsesCanonicalMediaType(t *testing.T) {
	svc := &replyPreviewService{message: &tg.Message{
		ID: 9,
		Media: &tg.MessageMediaDocument{Document: &tg.Document{
			ID: 99, MimeType: "video/mp4",
			Attributes: []tg.DocumentAttributeClass{
				&tg.DocumentAttributeVideo{W: 640, H: 360, Duration: 5},
			},
		}},
	}}
	ctx := &core.Context{Ctx: context.Background(), Svc: svc, PeerID: &tg.InputPeerSelf{}}
	preview := New().resolveReplyPreview(ctx, 9)
	if preview == nil || preview.Text != "[Video]" {
		t.Fatalf("reply preview=%+v, want canonical [Video] placeholder", preview)
	}
}

func BenchmarkStyledSegmentsManyEntities(b *testing.B) {
	text := strings.Repeat("A😀bcdefghij ", 100)
	entities := make([]tg.MessageEntityClass, 0, 100)
	for i := 0; i < 100; i++ {
		entities = append(entities, &tg.MessageEntityBold{Offset: i * 13, Length: 4})
	}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = styledSegments(text, entities)
	}
}

func BenchmarkWrapStyledSegments(b *testing.B) {
	face := loadFont("regular", 27)
	faces := styledFaces{normal: face, bold: face, italic: face, code: face}
	segments := styledSegments(strings.Repeat("alpha  beta gamma delta ", 50), nil)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = wrapStyledSegments(segments, faces, 672)
	}
}
