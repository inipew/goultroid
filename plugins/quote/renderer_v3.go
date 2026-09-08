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
	"unicode/utf16"

	"github.com/gotd/td/tg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"github.com/inipew/goultroid/internal/core"
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
	fontCacheLock sync.Mutex
	parsedFonts   = make(map[string]*opentype.Font)
)

func getParsedFont(paths []string) *opentype.Font {
	fontCacheLock.Lock()
	defer fontCacheLock.Unlock()

	for _, p := range paths {
		if f, ok := parsedFonts[p]; ok {
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
		return f
	}
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

	// Fonts (2x scale for sharp output)
	nameFace := loadFont("bold", 27)
	badgeFace := loadFont("bold", 21)
	bodyFace := loadFont("regular", 27)
	boldFace := loadFont("bold", 27)
	italicFace := loadFont("italic", 27)
	codeFace := loadFont("mono", 24)
	replyAuthorFace := loadFont("bold", 23)
	replyTextFace := loadFont("regular", 24)
	timeFace := loadFont("regular", 23)

	// Format text and entities
	rawText := strings.TrimSpace(opts.Text)
	var entities []tg.MessageEntityClass
	var mediaInfo *core.MediaInfo
	if opts.Message != nil {
		entities = opts.Message.Entities
		mediaInfo = opts.Message.Media
		if rawText == "" && opts.Message.MediaType != "" {
			rawText = mediaPlaceholder(opts.Message.MediaType)
		}
	}
	if rawText == "" {
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
	lines := wrapStyledSegments(styledSegments(rawText, entities), bodyFace, maxTextWidth)
	if len(lines) == 0 {
		lines = []styledLine{{segments: []styledSegment{{text: rawText}}}}
	}

	// Calculate widths
	nameWidth := font.MeasureString(nameFace, opts.Name).Ceil()
	badgeWidth := 0
	if opts.Badge != "" {
		badgeWidth = font.MeasureString(badgeFace, opts.Badge).Ceil() + 28 // 14px padding each side
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
		w := measureStyledLine(l, bodyFace, boldFace, italicFace, codeFace)
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
		lastLineWidth = measureStyledLine(lines[len(lines)-1], bodyFace, boldFace, italicFace, codeFace)
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
	drawAvatar(img, avatarImg, avatarCenter, avatarRadius, initialsFor(opts.Name), nameColor)

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
	d.DrawString(opts.Name)

	// 2. Draw Badge (if present)
	if opts.Badge != "" {
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
		bd.DrawString(opts.Badge)
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
		drawMediaCard(img, image.Rect(bubbleX+padLeft, currY, bubbleX+padLeft+contentWidth, currY+90), mediaInfo, mediaKind, codeFace, timeFace)
		currY += 96
	}

	// 5. Draw Body Text Lines
	if len(lines) > 0 {
		currY += 26
		for i, line := range lines {
			drawStyledLine(img, line, bubbleX+padLeft, currY, bodyFace, boldFace, italicFace, codeFace)
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
	if path != "" {
		if f, err := os.Open(path); err == nil {
			defer f.Close()
			if img, _, err := image.Decode(f); err == nil {
				return resizeNearest(img, size, size)
			}
		}
	}
	return nil
}

func drawAvatar(dst draw.Image, avatar image.Image, center image.Point, radius int, fallbackInitial string, fallbackColor color.Color) {
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
	face := loadFont("bold", float64(radius*4/5))
	w := font.MeasureString(face, fallbackInitial).Ceil()
	d := &font.Drawer{
		Dst:  dst,
		Src:  image.NewUniform(color.White),
		Face: face,
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
	for _, entity := range entities {
		start, end, ok := entityRuneRange(text, entity)
		if !ok {
			continue
		}
		style := styleNormal
		switch entity.(type) {
		case *tg.MessageEntityPre, *tg.MessageEntityCode:
			style = styleCode
		case *tg.MessageEntityBold:
			style = styleBold
		case *tg.MessageEntityItalic:
			style = styleItalic
		case *tg.MessageEntityTextURL, *tg.MessageEntityURL:
			style = styleLink
		default:
			continue
		}
		for i := start; i < end && i < len(styles); i++ {
			if style == styleCode || styles[i] == styleNormal || style == styleBold || style == styleItalic {
				styles[i] = style
			}
		}
	}
	var out []styledSegment
	start := 0
	for i := 1; i <= len(runes); i++ {
		if i == len(runes) || styles[i] != styles[start] {
			out = append(out, styledSegment{text: string(runes[start:i]), style: styles[start]})
			start = i
		}
	}
	return out
}

func entityRuneRange(text string, entity tg.MessageEntityClass) (int, int, bool) {
	var offset, length int
	switch e := entity.(type) {
	case *tg.MessageEntityBold:
		offset, length = e.Offset, e.Length
	case *tg.MessageEntityItalic:
		offset, length = e.Offset, e.Length
	case *tg.MessageEntityCode:
		offset, length = e.Offset, e.Length
	case *tg.MessageEntityPre:
		offset, length = e.Offset, e.Length
	case *tg.MessageEntityURL:
		offset, length = e.Offset, e.Length
	case *tg.MessageEntityTextURL:
		offset, length = e.Offset, e.Length
	default:
		return 0, 0, false
	}
	if offset < 0 || length <= 0 {
		return 0, 0, false
	}
	units, startByte, endByte := 0, -1, -1
	for i, r := range text {
		if units == offset {
			startByte = i
		}
		units += len(utf16.Encode([]rune{r}))
		if units == offset+length {
			endByte = i + len(string(r))
			break
		}
	}
	if startByte < 0 {
		if units == offset {
			startByte = len(text)
		} else {
			return 0, 0, false
		}
	}
	if endByte < 0 {
		if units == offset+length {
			endByte = len(text)
		} else {
			return 0, 0, false
		}
	}
	return len([]rune(text[:startByte])), len([]rune(text[:endByte])), true
}

func wrapStyledSegments(segments []styledSegment, face font.Face, maxWidth int) []styledLine {
	var lines []styledLine
	current := styledLine{}
	width := 0
	flush := func() {
		if len(current.segments) > 0 {
			lines = append(lines, current)
		}
		current = styledLine{}
		width = 0
	}
	for _, seg := range segments {
		paragraphs := strings.Split(strings.ReplaceAll(seg.text, "\r\n", "\n"), "\n")
		for pi, paragraph := range paragraphs {
			if pi > 0 {
				flush()
			}
			parts := strings.Fields(paragraph)
			if len(parts) == 0 {
				continue
			}
			for _, word := range parts {
				candidate := word
				if len(current.segments) > 0 {
					candidate = " " + word
				}
				cw := font.MeasureString(face, candidate).Ceil()
				if width > 0 && width+cw > maxWidth {
					flush()
					candidate = word
					cw = font.MeasureString(face, candidate).Ceil()
				}
				current.segments = append(current.segments, styledSegment{text: candidate, style: seg.style})
				width += cw
			}
		}
	}
	flush()
	return lines
}

func measureStyledLine(line styledLine, normal, bold, italic, code font.Face) int {
	w := 0
	for _, seg := range line.segments {
		f := normal
		switch seg.style {
		case styleBold:
			f = bold
		case styleItalic:
			f = italic
		case styleCode:
			f = code
		}
		w += font.MeasureString(f, seg.text).Ceil()
	}
	return w
}

func drawStyledLine(dst draw.Image, line styledLine, x, y int, normal, bold, italic, code font.Face) {
	d := &font.Drawer{Dst: dst, Dot: fixed.P(x, y)}
	for _, seg := range line.segments {
		d.Face = normal
		d.Src = image.NewUniform(colorText)
		switch seg.style {
		case styleBold:
			d.Face = bold
			d.Src = image.NewUniform(color.White)
		case styleItalic:
			d.Face = italic
			d.Src = image.NewUniform(color.White)
		case styleCode:
			d.Face = code
			d.Src = image.NewUniform(colorCodeText)
			sw := font.MeasureString(code, seg.text).Ceil()
			drawRoundedRect(dst, image.Rect(d.Dot.X.Floor()-2, y-20, d.Dot.X.Floor()+sw+2, y+6), 4, colorCodeBg)
		case styleLink:
			d.Src = image.NewUniform(colorCodeText)
		}
		d.DrawString(seg.text)
	}
}

// Media handling

func loadQuoteMedia(path string, media *core.MediaInfo) (image.Image, string) {
	if path == "" {
		return nil, ""
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, ""
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, ""
	}
	maxW := 600
	if b.Dx() > maxW {
		img = resizeNearest(img, maxW, b.Dy()*maxW/b.Dx())
	}
	kind := "media"
	if media != nil {
		kind = media.Type
	}
	return img, kind
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

func resizeNearest(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	sb := src.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			dst.Set(x, y, src.At(sb.Min.X+x*sb.Dx()/width, sb.Min.Y+y*sb.Dy()/height))
		}
	}
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
