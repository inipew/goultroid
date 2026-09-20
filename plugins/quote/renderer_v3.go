package quote

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/gotd/td/tg"
	xdraw "golang.org/x/image/draw"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/imageguard"
)

// RenderOptions specifies all configuration options for rendering a Telegram quote bubble.
type RenderOptions struct {
	Path         string
	Name         string
	Badge        string
	Text         string
	Message      *core.Message
	MediaPath    string
	AvatarPath   string
	ReplyPreview *ReplyPreview
	SenderID     int64
	Timestamp    string
}

// ReplyPreview holds information for the quoted/replied message banner inside the bubble.
type ReplyPreview struct {
	Author string
	Text   string
}

// Telegram desktop dark theme color constants.
var (
	colorCanvasBg     = color.RGBA{14, 22, 33, 255}    // #0e1621 (chat background)
	colorBubbleBg     = color.RGBA{24, 37, 51, 255}    // #182533 (incoming bubble)
	colorReplyBg      = color.RGBA{29, 51, 71, 255}    // #1d3347 (reply banner background)
	colorReplyBar     = color.RGBA{61, 143, 202, 255}  // #3d8fca (reply accent bar)
	colorReplyDeleted = color.RGBA{66, 155, 219, 255}  // #429bdb (deleted message text)
	colorText         = color.RGBA{245, 245, 245, 255} // #f5f5f5 (body text)
	colorTime         = color.RGBA{109, 127, 143, 255} // #6d7f8f (timestamp)
	colorBadgeBg      = color.RGBA{31, 55, 56, 255}    // #1f3738 (badge pill background)
	colorBadgeText    = color.RGBA{73, 163, 85, 255}   // #49a355 (badge green text)
	colorCodeBg       = color.RGBA{19, 31, 43, 255}    // #131f2b (monospace background)
	colorCodeText     = color.RGBA{94, 181, 247, 255}  // #5eb5f7 (code text)

	// Telegram standard 7-color palette for author names.
	telegramNameColors = []color.RGBA{
		{251, 97, 105, 255},  // 0: Red
		{250, 163, 87, 255},  // 1: Orange
		{180, 139, 242, 255}, // 2: Violet
		{106, 197, 111, 255}, // 3: Green
		{98, 212, 227, 255},  // 4: Cyan (#62d4e3, matching screenshot)
		{82, 136, 193, 255},  // 5: Blue
		{246, 123, 176, 255}, // 6: Pink
	}
)

var (
	fontCacheLock      sync.Mutex
	parsedFonts        = make(map[string]*opentype.Font)
	resolvedFontSource = make(map[string]*opentype.Font)

	quoteMediaPolicy = imageguard.Policy{
		MaxInputBytes:   32 << 20,
		MaxWidth:        8192,
		MaxHeight:       8192,
		MaxPixels:       32_000_000,
		MaxDecodedBytes: 128 << 20,
	}
	quoteAvatarPolicy = imageguard.Policy{
		MaxInputBytes:   8 << 20,
		MaxWidth:        4096,
		MaxHeight:       4096,
		MaxPixels:       12_000_000,
		MaxDecodedBytes: 48 << 20,
	}
)

const (
	quoteMediaPreviewMaxWidth  = 600
	quoteMediaPreviewMaxHeight = 720
	maxFallbackRuneCache       = 4096
)

func getParsedFont(paths []string) *opentype.Font {
	key := strings.Join(paths, "\x00")

	fontCacheLock.Lock()
	defer fontCacheLock.Unlock()
	if f, ok := resolvedFontSource[key]; ok {
		return f
	}

	for _, p := range paths {
		if f, ok := parsedFonts[p]; ok {
			resolvedFontSource[key] = f
			return f
		}
		data, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(data)
		if err != nil {
			continue
		}
		parsedFonts[p] = f
		resolvedFontSource[key] = f
		return f
	}
	resolvedFontSource[key] = nil
	return nil
}

type fallbackFace struct {
	primary   font.Face
	fallbacks []font.Face
	cache     map[rune]font.Face
	mu        sync.RWMutex
}

func (f *fallbackFace) faceForRune(r rune) font.Face {
	f.mu.RLock()
	if fc, ok := f.cache[r]; ok {
		f.mu.RUnlock()
		return fc
	}
	f.mu.RUnlock()

	f.mu.Lock()
	defer f.mu.Unlock()
	if fc, ok := f.cache[r]; ok {
		return fc
	}

	if len(f.cache) >= maxFallbackRuneCache {
		clear(f.cache)
	}

	target := f.primary
	if _, _, ok := f.primary.GlyphBounds(r); ok {
		target = f.primary
	} else {
		for _, fb := range f.fallbacks {
			if _, _, ok := fb.GlyphBounds(r); ok {
				target = fb
				break
			}
		}
	}
	f.cache[r] = target
	return target
}

func (f *fallbackFace) Close() error {
	_ = f.primary.Close()
	for _, fb := range f.fallbacks {
		_ = fb.Close()
	}
	return nil
}

func (f *fallbackFace) Glyph(dot fixed.Point26_6, r rune) (dr image.Rectangle, mask image.Image, maskp image.Point, advance fixed.Int26_6, ok bool) {
	face := f.faceForRune(r)
	dr, mask, maskp, advance, ok = face.Glyph(dot, r)
	if !ok && face != f.primary {
		return f.primary.Glyph(dot, r)
	}
	return
}

func (f *fallbackFace) GlyphBounds(r rune) (bounds fixed.Rectangle26_6, advance fixed.Int26_6, ok bool) {
	face := f.faceForRune(r)
	bounds, advance, ok = face.GlyphBounds(r)
	if !ok && face != f.primary {
		return f.primary.GlyphBounds(r)
	}
	return
}

func (f *fallbackFace) GlyphAdvance(r rune) (advance fixed.Int26_6, ok bool) {
	face := f.faceForRune(r)
	advance, ok = face.GlyphAdvance(r)
	if !ok && face != f.primary {
		return f.primary.GlyphAdvance(r)
	}
	return
}

func (f *fallbackFace) Kern(r0, r1 rune) fixed.Int26_6 {
	return f.primary.Kern(r0, r1)
}

func (f *fallbackFace) Metrics() font.Metrics {
	return f.primary.Metrics()
}

func loadFont(kind string, size float64) font.Face {
	var paths []string
	switch kind {
	case "bold":
		paths = []string{
			"/usr/share/fonts/noto/NotoSans-Bold.ttf",
			"/usr/share/fonts/noto/NotoSans-SemiBold.ttf",
			"/usr/share/fonts/TTF/OpenSans-Bold.ttf",
			"/usr/share/fonts/TTF/DejaVuSans-Bold.ttf",
			"/usr/share/fonts/liberation/LiberationSans-Bold.ttf",
			"/usr/share/fonts/truetype/noto/NotoSans-Bold.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-Bold.ttf",
		}
	case "semibold":
		paths = []string{
			"/usr/share/fonts/noto/NotoSans-SemiBold.ttf",
			"/usr/share/fonts/noto/NotoSans-Medium.ttf",
			"/usr/share/fonts/noto/NotoSans-Bold.ttf",
			"/usr/share/fonts/TTF/DejaVuSans.ttf",
			"/usr/share/fonts/liberation/LiberationSans-Regular.ttf",
		}
	case "italic":
		paths = []string{
			"/usr/share/fonts/noto/NotoSans-Italic.ttf",
			"/usr/share/fonts/TTF/DejaVuSans-Oblique.ttf",
			"/usr/share/fonts/liberation/LiberationSans-Italic.ttf",
			"/usr/share/fonts/truetype/noto/NotoSans-Italic.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans-Oblique.ttf",
		}
	case "mono":
		paths = []string{
			"/usr/share/fonts/noto/NotoSansMono-Regular.ttf",
			"/usr/share/fonts/TTF/DejaVuSansMono.ttf",
			"/usr/share/fonts/liberation/LiberationMono-Regular.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSansMono.ttf",
		}
	default:
		paths = []string{
			"/usr/share/fonts/noto/NotoSans-Regular.ttf",
			"/usr/share/fonts/TTF/OpenSans-Regular.ttf",
			"/usr/share/fonts/TTF/DejaVuSans.ttf",
			"/usr/share/fonts/liberation/LiberationSans-Regular.ttf",
			"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
			"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		}
	}

	var primaryFace font.Face
	if f := getParsedFont(paths); f != nil {
		face, err := opentype.NewFace(f, &opentype.FaceOptions{
			Size:    size,
			DPI:     72,
			Hinting: font.HintingFull,
		})
		if err == nil {
			primaryFace = face
		}
	}
	if primaryFace == nil {
		primaryFace = basicfont.Face7x13
	}

	// Load symbol fallbacks for unicode symbols, math, currency, arrows, etc.
	fallbackCandidatePaths := [][]string{
		{"/usr/share/fonts/noto/NotoSansSymbols2-Regular.ttf", "/usr/share/fonts/truetype/noto/NotoSansSymbols2-Regular.ttf"},
		{"/usr/share/fonts/noto/NotoSansSymbols-Regular.ttf", "/usr/share/fonts/truetype/noto/NotoSansSymbols-Regular.ttf"},
		{"/usr/share/fonts/noto/NotoSansMath-Regular.ttf", "/usr/share/fonts/truetype/noto/NotoSansMath-Regular.ttf"},
		{"/usr/share/fonts/TTF/DejaVuSans.ttf", "/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf"},
	}

	var fallbacks []font.Face
	for _, group := range fallbackCandidatePaths {
		if f := getParsedFont(group); f != nil {
			face, err := opentype.NewFace(f, &opentype.FaceOptions{
				Size:    size,
				DPI:     72,
				Hinting: font.HintingFull,
			})
			if err == nil {
				fallbacks = append(fallbacks, face)
			}
		}
	}

	if len(fallbacks) == 0 {
		return primaryFace
	}

	return &fallbackFace{
		primary:   primaryFace,
		fallbacks: fallbacks,
		cache:     make(map[rune]font.Face),
	}
}

type styledFaces struct {
	normal font.Face
	bold   font.Face
	italic font.Face
	code   font.Face
}

func (f styledFaces) face(style entityStyle) font.Face {
	switch style {
	case styleBold:
		return f.bold
	case styleItalic:
		return f.italic
	case styleCode:
		return f.code
	default:
		return f.normal
	}
}

type renderFontSet struct {
	name        font.Face
	badge       font.Face
	body        styledFaces
	replyAuthor font.Face
	replyText   font.Face
	timestamp   font.Face
	avatar      font.Face
}

func newRenderFontSet() *renderFontSet {
	bold27 := loadFont("bold", 27)
	return &renderFontSet{
		name:  bold27,
		badge: loadFont("bold", 21),
		body: styledFaces{
			normal: loadFont("regular", 27),
			bold:   bold27,
			italic: loadFont("italic", 27),
			code:   loadFont("mono", 24),
		},
		replyAuthor: loadFont("bold", 23),
		replyText:   loadFont("regular", 24),
		timestamp:   loadFont("regular", 23),
		avatar:      loadFont("bold", 33),
	}
}

var renderFontsPool = sync.Pool{
	New: func() any {
		return newRenderFontSet()
	},
}

func acquireRenderFonts() *renderFontSet {
	return renderFontsPool.Get().(*renderFontSet)
}

func releaseRenderFonts(fonts *renderFontSet) {
	if fonts != nil {
		renderFontsPool.Put(fonts)
	}
}

// renderV3 maintains backward compatibility with callers.
func renderV3(path, name, text string, msg *core.Message, mediaPath, avatarPath string) error {
	var senderID int64
	if msg != nil {
		senderID = msg.SenderID
	}
	return RenderV3WithOpts(RenderOptions{
		Path:       path,
		Name:       name,
		Text:       text,
		Message:    msg,
		MediaPath:  mediaPath,
		AvatarPath: avatarPath,
		SenderID:   senderID,
	})
}

// renderV3WithOpts alias for internal callers
func renderV3WithOpts(opts RenderOptions) error {
	return RenderV3WithOpts(opts)
}

// RenderV3WithOpts renders an authentic Telegram message bubble image at 2x resolution.
func RenderV3WithOpts(opts RenderOptions) error {
	if strings.TrimSpace(opts.Name) == "" {
		opts.Name = "User"
	}

	senderID := opts.SenderID
	if senderID == 0 && opts.Message != nil {
		senderID = opts.Message.SenderID
	}
	colorIndex := int(absInt64(senderID) % 7)
	nameColor := telegramNameColors[colorIndex]

	// Borrow one exclusive font set per render. This keeps font.Face instances
	// reusable without sharing mutable glyph state across concurrent renders.
	fonts := acquireRenderFonts()
	defer releaseRenderFonts(fonts)

	nameFace := fonts.name
	badgeFace := fonts.badge
	textFaces := fonts.body
	replyAuthorFace := fonts.replyAuthor
	replyTextFace := fonts.replyText
	timeFace := fonts.timestamp

	// Preserve the original prefix so Telegram UTF-16 entity offsets remain
	// aligned. The command path already applies prefix-only quote bounds.
	rawText := opts.Text
	var entities []tg.MessageEntityClass
	var mediaInfo *core.MediaInfo
	if opts.Message != nil {
		entities = opts.Message.Entities
		mediaInfo = opts.Message.Media
		if rawText == "" && opts.Message.MediaType != "" {
			rawText = mediaPlaceholder(opts.Message.MediaType)
		}
	}
	if strings.TrimSpace(rawText) == "" {
		rawText = " "
	}

	// Maximum allowable bubble width: ~720px at 2x
	const maxBubbleWidth = 720
	const padLeft = 24
	const padRight = 24
	const padTop = 16
	const padBottom = 14
	const maxTextWidth = maxBubbleWidth - padLeft - padRight

	// Line wrapping
	lines := wrapStyledSegments(styledSegments(rawText, entities), textFaces, maxTextWidth)
	if len(lines) == 0 {
		lines = []styledLine{{segments: []styledSegment{{text: rawText}}}}
	}

	// Bound header labels independently so a custom badge or unusual display
	// name can never force drawing outside the capped Telegram bubble width.
	renderName := truncateToWidth(opts.Name, nameFace, 420)
	renderBadge := truncateToWidth(opts.Badge, badgeFace, 180)

	// Calculate widths
	nameWidth := font.MeasureString(nameFace, renderName).Ceil()
	badgeWidth := 0
	if renderBadge != "" {
		badgeWidth = font.MeasureString(badgeFace, renderBadge).Ceil() + 28 // 14px padding each side
	}
	headerWidth := nameWidth
	if badgeWidth > 0 {
		headerWidth += 14 + badgeWidth
	}

	// Reply block measurement
	replyWidth := 0
	replyHeight := 0
	if opts.ReplyPreview != nil {
		if opts.ReplyPreview.Author != "" {
			aw := font.MeasureString(replyAuthorFace, opts.ReplyPreview.Author).Ceil()
			tw := font.MeasureString(replyTextFace, opts.ReplyPreview.Text).Ceil()
			replyWidth = max(aw, tw) + 36
			replyHeight = 64
		} else {
			tw := font.MeasureString(replyTextFace, opts.ReplyPreview.Text).Ceil()
			replyWidth = tw + 36
			replyHeight = 52
		}
	}

	// Text lines measurement
	maxLineWidth := 0
	for _, l := range lines {
		w := measureStyledLine(l, textFaces)
		if w > maxLineWidth {
			maxLineWidth = w
		}
	}

	// Time string
	timeStr := opts.Timestamp
	if timeStr == "" {
		if opts.Message != nil && !opts.Message.Date.IsZero() {
			timeStr = opts.Message.Date.Local().Format("15.04")
		} else {
			timeStr = time.Now().Format("15.04")
		}
	}
	timeWidth := font.MeasureString(timeFace, timeStr).Ceil()

	// Media loading
	mediaImg, mediaKind := loadQuoteMedia(opts.MediaPath, mediaInfo)
	mediaW, mediaH := 0, 0
	if mediaImg != nil {
		mediaW = mediaImg.Bounds().Dx()
		mediaH = mediaImg.Bounds().Dy()
	} else if mediaInfo != nil {
		mediaW = maxBubbleWidth - padLeft - padRight
		mediaH = 90
	}

	// Determine content width
	contentWidth := max(headerWidth, replyWidth, maxLineWidth, mediaW)
	if contentWidth < 280 {
		contentWidth = 280
	}
	if contentWidth > maxBubbleWidth-padLeft-padRight {
		contentWidth = maxBubbleWidth - padLeft - padRight
	}

	// Check if timestamp can fit on the last line or needs extra bottom space
	lastLineWidth := 0
	if len(lines) > 0 {
		lastLineWidth = measureStyledLine(lines[len(lines)-1], textFaces)
	}
	timeFitsOnLastLine := (len(lines) == 1 && contentWidth >= lastLineWidth+timeWidth+32) ||
		(len(lines) > 1 && contentWidth >= lastLineWidth+timeWidth+32)

	// Determine bubble height
	bubbleHeight := padTop + 28 /* name line */
	if replyHeight > 0 {
		bubbleHeight += 12 /* gap */ + replyHeight
	}
	if mediaH > 0 {
		bubbleHeight += 12 /* gap */ + mediaH
	}
	if len(lines) > 0 {
		bubbleHeight += 14 /* gap */ + len(lines)*34
	}
	if !timeFitsOnLastLine {
		bubbleHeight += 24 // extra row for timestamp
	}
	bubbleHeight += padBottom
	if bubbleHeight < 110 {
		bubbleHeight = 110
	}

	bubbleWidth := contentWidth + padLeft + padRight

	// Canvas dimensions (2x scale)
	const canvasPadX = 16
	const canvasPadY = 12
	const avatarDiameter = 84
	const avatarRadius = 42
	const gapAvatarBubble = 12
	const tailWidth = 14
	const tailHeight = 20

	canvasWidth := canvasPadX + avatarDiameter + gapAvatarBubble + bubbleWidth + canvasPadX
	canvasHeight := canvasPadY + bubbleHeight + canvasPadY

	img := image.NewRGBA(image.Rect(0, 0, canvasWidth, canvasHeight))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: colorCanvasBg}, image.Point{}, draw.Src)

	// Coordinates
	bubbleX := canvasPadX + avatarDiameter + gapAvatarBubble
	bubbleY := canvasPadY
	bubbleRect := image.Rect(bubbleX, bubbleY, bubbleX+bubbleWidth, bubbleY+bubbleHeight)

	// Avatar bottom aligns with bubble bottom
	avatarCenter := image.Pt(canvasPadX+avatarRadius, bubbleY+bubbleHeight-avatarRadius)
	avatarImg := loadAvatarImage(opts.AvatarPath, avatarDiameter)
	drawAvatar(img, avatarImg, avatarCenter, avatarRadius, initialsFor(opts.Name), nameColor, fonts.avatar)

	// Draw incoming message bubble with tail
	drawTelegramBubble(img, bubbleRect, 22, tailWidth, tailHeight, colorBubbleBg)

	// 1. Draw Sender Name
	currX := bubbleX + padLeft
	currY := bubbleY + padTop + 20
	d := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(nameColor),
		Face: nameFace,
		Dot:  fixed.P(currX, currY),
	}
	d.DrawString(renderName)

	// 2. Draw Badge (if present)
	if renderBadge != "" {
		bx := currX + nameWidth + 14
		by := currY - 18
		bh := 26
		bw := badgeWidth
		drawRoundedRect(img, image.Rect(bx, by, bx+bw, by+bh), 13, colorBadgeBg)
		bd := &font.Drawer{
			Dst:  img,
			Src:  image.NewUniform(colorBadgeText),
			Face: badgeFace,
			Dot:  fixed.P(bx+14, by+19),
		}
		bd.DrawString(renderBadge)
	}

	currY += 8 // move past header

	// 3. Draw Reply Banner (if present)
	if opts.ReplyPreview != nil {
		replyBoxX := bubbleX + padLeft
		replyBoxY := currY + 6
		replyBoxW := bubbleWidth - padLeft - padRight
		drawRoundedRect(img, image.Rect(replyBoxX, replyBoxY, replyBoxX+replyBoxW, replyBoxY+replyHeight), 6, colorReplyBg)
		drawRoundedRect(img, image.Rect(replyBoxX, replyBoxY, replyBoxX+6, replyBoxY+replyHeight), 3, colorReplyBar)

		if opts.ReplyPreview.Author != "" {
			rad := &font.Drawer{
				Dst:  img,
				Src:  image.NewUniform(colorReplyBar),
				Face: replyAuthorFace,
				Dot:  fixed.P(replyBoxX+18, replyBoxY+24),
			}
			rad.DrawString(truncateToWidth(opts.ReplyPreview.Author, replyAuthorFace, replyBoxW-30))

			rtd := &font.Drawer{
				Dst:  img,
				Src:  image.NewUniform(color.RGBA{141, 163, 184, 255}),
				Face: replyTextFace,
				Dot:  fixed.P(replyBoxX+18, replyBoxY+50),
			}
			rtd.DrawString(truncateToWidth(opts.ReplyPreview.Text, replyTextFace, replyBoxW-30))
		} else {
			rd := &font.Drawer{
				Dst:  img,
				Src:  image.NewUniform(colorReplyDeleted),
				Face: replyTextFace,
				Dot:  fixed.P(replyBoxX+18, replyBoxY+34),
			}
			rd.DrawString(truncateToWidth(opts.ReplyPreview.Text, replyTextFace, replyBoxW-30))
		}
		currY += replyHeight + 6
	}

	// 4. Draw Media (if present)
	if mediaImg != nil {
		currY += 8
		drawRoundedImage(img, mediaImg, image.Pt(bubbleX+padLeft, currY), 16)
		currY += mediaH + 6
	} else if mediaInfo != nil {
		currY += 8
		drawMediaCard(img, image.Rect(bubbleX+padLeft, currY, bubbleX+padLeft+contentWidth, currY+90), mediaInfo, mediaKind, textFaces.code, timeFace)
		currY += 96
	}

	// 5. Draw Body Text Lines
	if len(lines) > 0 {
		currY += 26
		for i, line := range lines {
			drawStyledLine(img, line, bubbleX+padLeft, currY, textFaces)
			if i < len(lines)-1 {
				currY += 34
			}
		}
	}

	// 6. Draw Timestamp at bottom-right
	timeX := bubbleX + bubbleWidth - padRight - timeWidth
	timeY := currY + 6
	if !timeFitsOnLastLine {
		timeY = bubbleY + bubbleHeight - padBottom - 2
	}
	timed := &font.Drawer{
		Dst:  img,
		Src:  image.NewUniform(colorTime),
		Face: timeFace,
		Dot:  fixed.P(timeX, timeY),
	}
	timed.DrawString(timeStr)

	// Save to file (support PNG and JPEG)
	if err := os.MkdirAll(filepath.Dir(opts.Path), 0o700); err != nil {
		return err
	}
	f, err := os.Create(opts.Path)
	if err != nil {
		return err
	}
	defer f.Close()

	ext := strings.ToLower(filepath.Ext(opts.Path))
	if ext == ".jpg" || ext == ".jpeg" {
		return jpeg.Encode(f, img, &jpeg.Options{Quality: 96})
	}
	return png.Encode(f, img)
}

// Drawing primitives

func drawTelegramBubble(dst draw.Image, r image.Rectangle, radius int, tailWidth, tailHeight int, c color.Color) {
	draw.Draw(dst, image.Rect(r.Min.X, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), &image.Uniform{C: c}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(r.Min.X+radius, r.Min.Y, r.Max.X-radius, r.Min.Y+radius), &image.Uniform{C: c}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(r.Min.X, r.Max.Y-radius, r.Max.X-radius, r.Max.Y), &image.Uniform{C: c}, image.Point{}, draw.Src)

	drawQuarterCircle(dst, image.Pt(r.Min.X+radius, r.Min.Y+radius), radius, 2, c)
	drawQuarterCircle(dst, image.Pt(r.Max.X-radius-1, r.Min.Y+radius), radius, 1, c)
	drawQuarterCircle(dst, image.Pt(r.Max.X-radius-1, r.Max.Y-radius-1), radius, 4, c)

	for y := r.Max.Y - tailHeight; y < r.Max.Y; y++ {
		progress := float64(y-(r.Max.Y-tailHeight)) / float64(tailHeight)
		offset := int(math.Pow(progress, 1.25) * float64(tailWidth))
		startX := r.Min.X - offset
		for x := startX; x < r.Min.X; x++ {
			dst.Set(x, y, c)
		}
	}
}

func drawQuarterCircle(dst draw.Image, center image.Point, radius int, quadrant int, c color.Color) {
	r2 := radius * radius
	for y := -radius; y <= radius; y++ {
		for x := -radius; x <= radius; x++ {
			if x*x+y*y <= r2 {
				match := false
				switch quadrant {
				case 1:
					match = (x >= 0 && y <= 0)
				case 2:
					match = (x <= 0 && y <= 0)
				case 3:
					match = (x <= 0 && y >= 0)
				case 4:
					match = (x >= 0 && y >= 0)
				}
				if match {
					dst.Set(center.X+x, center.Y+y, c)
				}
			}
		}
	}
}

func drawCircle(dst draw.Image, center image.Point, radius int, c color.Color) {
	r2 := radius * radius
	for y := -radius; y <= radius; y++ {
		for x := -radius; x <= radius; x++ {
			if x*x+y*y <= r2 {
				dst.Set(center.X+x, center.Y+y, c)
			}
		}
	}
}

func drawRoundedRect(dst draw.Image, r image.Rectangle, radius int, c color.Color) {
	if radius <= 0 {
		draw.Draw(dst, r, &image.Uniform{C: c}, image.Point{}, draw.Src)
		return
	}
	draw.Draw(dst, image.Rect(r.Min.X+radius, r.Min.Y, r.Max.X-radius, r.Max.Y), &image.Uniform{C: c}, image.Point{}, draw.Src)
	draw.Draw(dst, image.Rect(r.Min.X, r.Min.Y+radius, r.Max.X, r.Max.Y-radius), &image.Uniform{C: c}, image.Point{}, draw.Src)
	for _, center := range []image.Point{
		{r.Min.X + radius, r.Min.Y + radius},
		{r.Max.X - radius - 1, r.Min.Y + radius},
		{r.Min.X + radius, r.Max.Y - radius - 1},
		{r.Max.X - radius - 1, r.Max.Y - radius - 1},
	} {
		drawCircle(dst, center, radius, c)
	}
}

func loadAvatarImage(path string, size int) image.Image {
	if path == "" {
		return nil
	}
	img, _, err := imageguard.Decode(path, quoteAvatarPolicy)
	if err != nil {
		return nil
	}
	return resizeAvatarCover(img, size)
}

func drawAvatar(dst draw.Image, avatar image.Image, center image.Point, radius int, fallbackInitial string, fallbackColor color.Color, initialFace font.Face) {
	r2 := radius * radius
	if avatar != nil {
		ab := avatar.Bounds()
		for y := -radius; y <= radius; y++ {
			for x := -radius; x <= radius; x++ {
				if x*x+y*y <= r2 {
					sx := (x + radius) * ab.Dx() / (radius*2 + 1)
					sy := (y + radius) * ab.Dy() / (radius*2 + 1)
					if sx >= ab.Dx() {
						sx = ab.Dx() - 1
					}
					if sy >= ab.Dy() {
						sy = ab.Dy() - 1
					}
					dst.Set(center.X+x, center.Y+y, avatar.At(ab.Min.X+sx, ab.Min.Y+sy))
				}
			}
		}
		return
	}

	drawCircle(dst, center, radius, fallbackColor)
	if initialFace == nil {
		initialFace = basicfont.Face7x13
	}
	w := font.MeasureString(initialFace, fallbackInitial).Ceil()
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.White),
		Face: initialFace,
		Dot:  fixed.P(center.X-w/2, center.Y+radius/3),
	}
	d.DrawString(fallbackInitial)
}

// Text styling & Entities

type styledSegment struct {
	text  string
	style entityStyle
}
type styledLine struct{ segments []styledSegment }
type entityStyle uint8

const (
	styleNormal entityStyle = iota
	styleBold
	styleItalic
	styleCode
	styleLink
)

func styledSegments(text string, entities []tg.MessageEntityClass) []styledSegment {
	runes := []rune(text)
	if len(runes) == 0 {
		return nil
	}

	styles := make([]entityStyle, len(runes))
	boundaries := utf16RuneBoundaries(runes)
	maxUnits := len(boundaries) - 1
	for _, entity := range entities {
		offset, length, style, ok := entityStyleSpan(entity)
		if !ok || offset < 0 || length <= 0 || offset >= maxUnits {
			continue
		}
		end := offset + length
		if end > maxUnits {
			end = maxUnits
		}
		startRune, endRune := boundaries[offset], boundaries[end]
		if startRune < 0 || endRune < 0 || startRune >= endRune || endRune > len(styles) {
			continue
		}
		for i := startRune; i < endRune; i++ {
			styles[i] = mergeEntityStyle(styles[i], style)
		}
	}

	out := make([]styledSegment, 0, len(entities)*2+1)
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || styles[i] != styles[start] {
			out = append(out, styledSegment{text: string(runes[start:i]), style: styles[start]})
			start = i
		}
	}
	return out
}

func utf16RuneBoundaries(runes []rune) []int {
	boundaries := make([]int, len(runes)*2+1)
	for i := range boundaries {
		boundaries[i] = -1
	}
	boundaries[0] = 0
	units := 0
	for i, r := range runes {
		if r > 0xffff {
			units += 2
		} else {
			units++
		}
		boundaries[units] = i + 1
	}
	return boundaries[:units+1]
}

func entityStyleSpan(entity tg.MessageEntityClass) (offset, length int, style entityStyle, ok bool) {
	switch e := entity.(type) {
	case *tg.MessageEntityBold:
		return e.Offset, e.Length, styleBold, true
	case *tg.MessageEntityItalic:
		return e.Offset, e.Length, styleItalic, true
	case *tg.MessageEntityCode:
		return e.Offset, e.Length, styleCode, true
	case *tg.MessageEntityPre:
		return e.Offset, e.Length, styleCode, true
	case *tg.MessageEntityURL:
		return e.Offset, e.Length, styleLink, true
	case *tg.MessageEntityTextURL:
		return e.Offset, e.Length, styleLink, true
	default:
		return 0, 0, styleNormal, false
	}
}

func mergeEntityStyle(current, next entityStyle) entityStyle {
	if next == styleCode {
		return styleCode
	}
	if current == styleCode {
		return current
	}
	if next == styleLink {
		if current == styleNormal {
			return styleLink
		}
		return current
	}
	return next
}

func wrapStyledSegments(segments []styledSegment, faces styledFaces, maxWidth int) []styledLine {
	if maxWidth <= 0 {
		return nil
	}

	lines := make([]styledLine, 0, 4)
	current := styledLine{}
	width := 0
	lastWasNewline := false

	flush := func(force bool) {
		if len(current.segments) > 0 || force {
			lines = append(lines, current)
		}
		current = styledLine{}
		width = 0
	}

	appendText := func(text string, style entityStyle) {
		if text == "" {
			return
		}
		face := faces.face(style)
		if len(current.segments) > 0 && current.segments[len(current.segments)-1].style == style {
			last := &current.segments[len(current.segments)-1]
			width += appendStyledAdvance(face, last.text, text)
			last.text += text
			return
		}
		current.segments = append(current.segments, styledSegment{text: text, style: style})
		width += font.MeasureString(face, text).Ceil()
	}

	addToken := func(token string, style entityStyle) {
		for token != "" {
			face := faces.face(style)
			tokenWidth := font.MeasureString(face, token).Ceil()
			if len(current.segments) == 0 && tokenWidth <= maxWidth {
				appendText(token, style)
				return
			}

			candidateWidth := width + tokenWidth
			if len(current.segments) > 0 && current.segments[len(current.segments)-1].style == style {
				last := current.segments[len(current.segments)-1]
				candidateWidth = width + appendStyledAdvance(face, last.text, token)
			}
			if candidateWidth <= maxWidth {
				appendText(token, style)
				return
			}
			if len(current.segments) > 0 {
				flush(false)
				continue
			}

			prefix, rest := fitStyledPrefix(token, face, maxWidth)
			if prefix == "" {
				runes := []rune(token)
				prefix = string(runes[:1])
				rest = string(runes[1:])
			}
			appendText(prefix, style)
			token = rest
			if token != "" {
				flush(false)
			}
		}
	}

	for _, segment := range segments {
		text := strings.ReplaceAll(strings.ReplaceAll(segment.text, "\r\n", "\n"), "\r", "\n")
		text = strings.ReplaceAll(text, "\t", "    ")
		runes := []rune(text)
		for i := 0; i < len(runes); {
			if runes[i] == '\n' {
				flush(true)
				lastWasNewline = true
				i++
				continue
			}

			space := unicode.IsSpace(runes[i])
			start := i
			for i < len(runes) && runes[i] != '\n' && unicode.IsSpace(runes[i]) == space {
				i++
			}
			addToken(string(runes[start:i]), segment.style)
			lastWasNewline = false
		}
	}
	if len(current.segments) > 0 {
		flush(false)
	} else if lastWasNewline {
		flush(true)
	}
	return lines
}

func appendStyledAdvance(face font.Face, existing, appended string) int {
	if appended == "" {
		return 0
	}
	advance := font.MeasureString(face, appended)
	if existing != "" {
		last, _ := utf8.DecodeLastRuneInString(existing)
		first, _ := utf8.DecodeRuneInString(appended)
		advance += face.Kern(last, first)
	}
	return advance.Ceil()
}

func fitStyledPrefix(text string, face font.Face, maxWidth int) (string, string) {
	runes := []rune(text)
	if len(runes) == 0 {
		return "", ""
	}
	low, high := 1, len(runes)
	best := 0
	for low <= high {
		mid := low + (high-low)/2
		if font.MeasureString(face, string(runes[:mid])).Ceil() <= maxWidth {
			best = mid
			low = mid + 1
		} else {
			high = mid - 1
		}
	}
	if best == 0 {
		return "", text
	}
	return string(runes[:best]), string(runes[best:])
}

func measureStyledLine(line styledLine, faces styledFaces) int {
	width := 0
	for _, segment := range line.segments {
		width += font.MeasureString(faces.face(segment.style), segment.text).Ceil()
	}
	return width
}

func drawStyledLine(dst draw.Image, line styledLine, x, y int, faces styledFaces) {
	d := &font.Drawer{Dst: dst, Dot: fixed.P(x, y)}
	for _, seg := range line.segments {
		d.Face = faces.face(seg.style)
		d.Src = image.NewUniform(colorText)
		switch seg.style {
		case styleBold, styleItalic:
			d.Src = image.NewUniform(color.White)
		case styleCode:
			d.Src = image.NewUniform(colorCodeText)
			sw := font.MeasureString(d.Face, seg.text).Ceil()
			drawRoundedRect(dst, image.Rect(d.Dot.X.Floor()-2, y-20, d.Dot.X.Floor()+sw+2, y+6), 4, colorCodeBg)
		case styleLink:
			d.Src = image.NewUniform(colorCodeText)
		}
		d.DrawString(seg.text)
	}
}

// Media handling

func loadQuoteMedia(path string, media *core.MediaInfo) (image.Image, string) {
	kind := "media"
	if media != nil && strings.TrimSpace(media.Type) != "" {
		kind = media.Type
	}
	if path == "" {
		return nil, kind
	}
	img, _, err := imageguard.Decode(path, quoteMediaPolicy)
	if err != nil {
		return nil, kind
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, kind
	}
	dstW, dstH := fitWithin(b.Dx(), b.Dy(), quoteMediaPreviewMaxWidth, quoteMediaPreviewMaxHeight)
	if dstW != b.Dx() || dstH != b.Dy() {
		img = resizeImage(img, dstW, dstH)
	}
	return img, kind
}

func fitWithin(width, height, maxWidth, maxHeight int) (int, int) {
	if width <= 0 || height <= 0 || maxWidth <= 0 || maxHeight <= 0 {
		return 1, 1
	}
	if width <= maxWidth && height <= maxHeight {
		return width, height
	}
	scale := math.Min(float64(maxWidth)/float64(width), float64(maxHeight)/float64(height))
	w := int(math.Round(float64(width) * scale))
	h := int(math.Round(float64(height) * scale))
	if w < 1 {
		w = 1
	}
	if h < 1 {
		h = 1
	}
	return w, h
}

func drawMediaCard(dst draw.Image, r image.Rectangle, media *core.MediaInfo, kind string, titleFace, subFace font.Face) {
	drawRoundedRect(dst, r, 12, color.RGBA{38, 48, 60, 255})
	d := &font.Drawer{
		Dst:  dst,
		Face: titleFace,
		Src:  image.NewUniform(colorCodeText),
		Dot:  fixed.P(r.Min.X+24, r.Min.Y+38),
	}
	d.DrawString(mediaPlaceholder(kind))
	meta := ""
	if media != nil {
		if media.FileName != "" {
			meta = media.FileName
		}
		if media.Size > 0 {
			if meta != "" {
				meta += "  •  "
			}
			meta += formatBytes(media.Size)
		}
	}
	if meta != "" {
		d.Face = subFace
		d.Src = image.NewUniform(colorTime)
		d.Dot = fixed.P(r.Min.X+24, r.Min.Y+68)
		d.DrawString(truncateToWidth(meta, subFace, r.Dx()-48))
	}
}

func drawRoundedImage(dst draw.Image, src image.Image, at image.Point, radius int) {
	r := src.Bounds()
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			lx, ly := x-r.Min.X, y-r.Min.Y
			if insideRounded(lx, ly, r.Dx(), r.Dy(), radius) {
				dst.Set(at.X+lx, at.Y+ly, src.At(x, y))
			}
		}
	}
}

func insideRounded(x, y, w, h, radius int) bool {
	if (x >= radius && x < w-radius) || (y >= radius && y < h-radius) {
		return true
	}
	cx, cy := radius, radius
	if x >= w-radius {
		cx = w - radius - 1
	}
	if y >= h-radius {
		cy = h - radius - 1
	}
	dx, dy := x-cx, y-cy
	return dx*dx+dy*dy <= radius*radius
}

func resizeImage(src image.Image, width, height int) image.Image {
	if src == nil || width <= 0 || height <= 0 {
		return nil
	}
	bounds := src.Bounds()
	if bounds.Dx() == width && bounds.Dy() == height {
		return src
	}
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), src, bounds, draw.Src, nil)
	return dst
}

func resizeAvatarCover(src image.Image, size int) image.Image {
	if src == nil || size <= 0 {
		return nil
	}
	bounds := src.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if width <= 0 || height <= 0 {
		return nil
	}
	side := min(width, height)
	minX := bounds.Min.X + (width-side)/2
	minY := bounds.Min.Y + (height-side)/2
	sourceRect := image.Rect(minX, minY, minX+side, minY+side)
	dst := image.NewRGBA(image.Rect(0, 0, size, size))
	xdraw.BiLinear.Scale(dst, dst.Bounds(), src, sourceRect, draw.Src, nil)
	return dst
}

func truncateToWidth(s string, face font.Face, maxWidth int) string {
	if font.MeasureString(face, s).Ceil() <= maxWidth {
		return s
	}
	for len(s) > 1 {
		r := []rune(s)
		s = string(r[:len(r)-1])
		if font.MeasureString(face, s+"…").Ceil() <= maxWidth {
			return s + "…"
		}
	}
	return "…"
}

func formatBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	v := float64(n)
	for _, u := range []string{"KB", "MB", "GB", "TB"} {
		v /= 1024
		if v < 1024 {
			return fmt.Sprintf("%.1f %s", v, u)
		}
	}
	return fmt.Sprintf("%.1f PB", v/1024)
}

func absInt64(n int64) int64 {
	if n < 0 {
		return -n
	}
	return n
}

func max(a int, rest ...int) int {
	m := a
	for _, v := range rest {
		if v > m {
			m = v
		}
	}
	return m
}
