package quote

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"strings"
	"unicode/utf16"

	"github.com/gotd/td/tg"
	"golang.org/x/image/font"
	"golang.org/x/image/math/fixed"
	_ "golang.org/x/image/webp"

	"github.com/inipew/goultroid/internal/core"
)

func renderV3(path, name, text string, msg *core.Message, mediaPath, avatarPath string) error {
	if msg == nil { return fmt.Errorf("message is nil") }
	const width = 1080
	const pad = 76
	const headerBottom = 184
	const lineHeight = 44
	theme := themeFor(msg.SenderID)
	nameFace, bodyFace, boldFace, italicFace := loadFont(31), loadFont(29), loadFont(29), loadFont(29)
	codeFace, metaFace := loadFont(26), loadFont(19)
	textWidth := width - pad*2
	if strings.TrimSpace(text) == "" { text = mediaPlaceholder(msg.MediaType) }
	lines := wrapStyledSegments(styledSegments(text, msg.Entities), bodyFace, textWidth)
	if len(lines) == 0 { lines = []styledLine{{segments: []styledSegment{{text: mediaPlaceholder(msg.MediaType)}}}} }
	media, mediaKind := loadQuoteMedia(mediaPath, msg.Media)
	mediaHeight := 0
	if media != nil { mediaHeight = media.Bounds().Dy() + 34 } else if msg.Media != nil { mediaHeight = 112 }
	height := headerBottom + len(lines)*lineHeight + 72 + mediaHeight
	if height < 440 { height = 440 }
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: theme.background}, image.Point{}, draw.Src)
	drawRoundedRect(img, image.Rect(26, 26, width-26, height-26), 30, theme.panel)
	drawRoundedRect(img, image.Rect(26, 26, 36, height-26), 5, theme.accent)
	avatar := loadAvatar(avatarPath, 98, theme.accent)
	drawAvatar(img, avatar, image.Pt(105, 112), 49)
	nameX := 176
	d := &font.Drawer{Dst: img, Face: nameFace, Src: image.NewUniform(theme.primary), Dot: fixed.P(nameX, 101)}
	d.DrawString(truncateToWidth(name, nameFace, width-nameX-pad))
	meta := "Telegram"
	if !msg.Date.IsZero() { meta += "  •  " + msg.Date.Local().Format("02 Jan 2006, 15:04") }
	d.Face, d.Src, d.Dot = metaFace, image.NewUniform(theme.muted), fixed.P(nameX, 134)
	d.DrawString(meta)
	drawRoundedRect(img, image.Rect(pad, headerBottom, width-pad, headerBottom+3), 2, theme.accent)
	y := headerBottom + 42
	for _, line := range lines { drawStyledLine(img, line, pad, y, bodyFace, boldFace, italicFace, codeFace, theme); y += lineHeight }
	if media != nil { y += 18; drawRoundedImage(img, media, image.Pt((width-media.Bounds().Dx())/2, y), 20) } else if msg.Media != nil { y += 12; drawMediaCard(img, image.Rect(pad, y, width-pad, y+90), msg.Media, mediaKind, theme, metaFace) }
	f, err := os.Create(path); if err != nil { return err }; defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 94})
}

type styledSegment struct { text string; style entityStyle }
type styledLine struct{ segments []styledSegment }
type entityStyle uint8
const ( styleNormal entityStyle = iota; styleBold; styleItalic; styleCode; styleLink )

func styledSegments(text string, entities []tg.MessageEntityClass) []styledSegment {
	runes := []rune(text); if len(runes) == 0 { return nil }
	styles := make([]entityStyle, len(runes))
	for _, entity := range entities {
		start, end, ok := entityRuneRange(text, entity); if !ok { continue }
		style := styleNormal
		switch entity.(type) {
		case *tg.MessageEntityPre, *tg.MessageEntityCode: style = styleCode
		case *tg.MessageEntityBold: style = styleBold
		case *tg.MessageEntityItalic: style = styleItalic
		case *tg.MessageEntityTextURL, *tg.MessageEntityURL: style = styleLink
		default: continue
		}
		for i := start; i < end && i < len(styles); i++ { if style == styleCode || styles[i] == styleNormal || style == styleBold || style == styleItalic { styles[i] = style } }
	}
	var out []styledSegment; start := 0
	for i := 1; i <= len(runes); i++ { if i == len(runes) || styles[i] != styles[start] { out = append(out, styledSegment{text: string(runes[start:i]), style: styles[start]}); start = i } }
	return out
}

func entityRuneRange(text string, entity tg.MessageEntityClass) (int, int, bool) {
	var offset, length int
	switch e := entity.(type) {
	case *tg.MessageEntityBold: offset, length = e.Offset, e.Length
	case *tg.MessageEntityItalic: offset, length = e.Offset, e.Length
	case *tg.MessageEntityCode: offset, length = e.Offset, e.Length
	case *tg.MessageEntityPre: offset, length = e.Offset, e.Length
	case *tg.MessageEntityURL: offset, length = e.Offset, e.Length
	case *tg.MessageEntityTextURL: offset, length = e.Offset, e.Length
	default: return 0, 0, false
	}
	if offset < 0 || length <= 0 { return 0, 0, false }
	units, startByte, endByte := 0, -1, -1
	for i, r := range text {
		if units == offset { startByte = i }
		units += len(utf16.Encode([]rune{r}))
		if units == offset+length { endByte = i + len(string(r)); break }
	}
	if startByte < 0 { if units == offset { startByte = len(text) } else { return 0, 0, false } }
	if endByte < 0 { if units == offset+length { endByte = len(text) } else { return 0, 0, false } }
	return len([]rune(text[:startByte])), len([]rune(text[:endByte])), true
}

func wrapStyledSegments(segments []styledSegment, face font.Face, maxWidth int) []styledLine {
	var lines []styledLine; current := styledLine{}; width := 0
	flush := func() { if len(current.segments) > 0 { lines = append(lines, current) }; current = styledLine{}; width = 0 }
	for _, seg := range segments {
		parts := strings.Fields(seg.text)
		if len(parts) == 0 { if strings.Contains(seg.text, "\n") { flush() }; continue }
		for _, word := range parts {
			candidate := word; if len(current.segments) > 0 { candidate = " " + word }
			cw := font.MeasureString(face, candidate).Ceil()
			if width > 0 && width+cw > maxWidth { flush(); candidate = word; cw = font.MeasureString(face, candidate).Ceil() }
			current.segments = append(current.segments, styledSegment{text: candidate, style: seg.style}); width += cw
		}
	}
	flush(); return lines
}

func drawStyledLine(dst draw.Image, line styledLine, x, y int, normal, bold, italic, code font.Face, theme quoteTheme) {
	d := &font.Drawer{Dst: dst, Dot: fixed.P(x, y)}
	for _, seg := range line.segments {
		d.Face = normal; d.Src = image.NewUniform(theme.secondary)
		switch seg.style { case styleBold: d.Face = bold; d.Src = image.NewUniform(theme.primary); case styleItalic: d.Face = italic; d.Src = image.NewUniform(theme.primary); case styleCode: d.Face = code; d.Src = image.NewUniform(theme.accent); case styleLink: d.Src = image.NewUniform(theme.accent) }
		d.DrawString(seg.text)
	}
}

func loadAvatar(path string, size int, fallback color.Color) image.Image {
	if path != "" { if f, err := os.Open(path); err == nil { defer f.Close(); if img, _, err := image.Decode(f); err == nil { return resizeNearest(img, size, size) } } }
	img := image.NewRGBA(image.Rect(0, 0, size, size)); draw.Draw(img, img.Bounds(), &image.Uniform{C: fallback}, image.Point{}, draw.Src); return img
}
func drawAvatar(dst draw.Image, avatar image.Image, center image.Point, radius int) {
	for y := -radius; y <= radius; y++ { for x := -radius; x <= radius; x++ { if x*x+y*y > radius*radius { continue }; sx := (x+radius)*avatar.Bounds().Dx()/(radius*2+1); sy := (y+radius)*avatar.Bounds().Dy()/(radius*2+1); if sx >= avatar.Bounds().Dx() { sx = avatar.Bounds().Dx()-1 }; if sy >= avatar.Bounds().Dy() { sy = avatar.Bounds().Dy()-1 }; dst.Set(center.X+x, center.Y+y, avatar.At(avatar.Bounds().Min.X+sx, avatar.Bounds().Min.Y+sy)) } }
}
func loadQuoteMedia(path string, media *core.MediaInfo) (image.Image, string) {
	if path == "" { return nil, "" }; f, err := os.Open(path); if err != nil { return nil, "" }; defer f.Close(); img, _, err := image.Decode(f); if err != nil { return nil, "" }; b := img.Bounds(); if b.Dx() <= 0 || b.Dy() <= 0 { return nil, "" }; maxW := 820; if b.Dx() > maxW { img = resizeNearest(img, maxW, b.Dy()*maxW/b.Dx()) }; kind := "media"; if media != nil { kind = media.Type }; return img, kind
}
func drawMediaCard(dst draw.Image, r image.Rectangle, media *core.MediaInfo, kind string, theme quoteTheme, face font.Face) {
	drawRoundedRect(dst, r, 20, color.RGBA{38, 40, 48, 255}); d := &font.Drawer{Dst: dst, Face: loadFont(26), Src: image.NewUniform(theme.accent), Dot: fixed.P(r.Min.X+28, r.Min.Y+42)}; d.DrawString(mediaPlaceholder(kind)); meta := ""; if media != nil { if media.FileName != "" { meta = media.FileName }; if media.Size > 0 { if meta != "" { meta += "  •  " }; meta += formatBytes(media.Size) } }; if meta != "" { d.Face = face; d.Src = image.NewUniform(theme.muted); d.Dot = fixed.P(r.Min.X+28, r.Min.Y+72); d.DrawString(truncateToWidth(meta, face, r.Dx()-56)) }
}
func formatBytes(n int64) string { if n < 1024 { return fmt.Sprintf("%d B", n) }; v := float64(n); for _, u := range []string{"KB", "MB", "GB", "TB"} { v /= 1024; if v < 1024 { return fmt.Sprintf("%.1f %s", v, u) } }; return fmt.Sprintf("%.1f PB", v/1024) }
func drawRoundedImage(dst draw.Image, src image.Image, at image.Point, radius int) { r := src.Bounds(); for y := r.Min.Y; y < r.Max.Y; y++ { for x := r.Min.X; x < r.Max.X; x++ { lx, ly := x-r.Min.X, y-r.Min.Y; if insideRounded(lx, ly, r.Dx(), r.Dy(), radius) { dst.Set(at.X+lx, at.Y+ly, src.At(x, y)) } } } }
func insideRounded(x, y, w, h, radius int) bool { if (x >= radius && x < w-radius) || (y >= radius && y < h-radius) { return true }; cx, cy := radius, radius; if x >= w-radius { cx = w-radius-1 }; if y >= h-radius { cy = h-radius-1 }; dx, dy := x-cx, y-cy; return dx*dx+dy*dy <= radius*radius }
