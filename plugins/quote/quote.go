package quote

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/tg"
	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
	"golang.org/x/image/font/opentype"
	"golang.org/x/image/math/fixed"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/execution"
)

type Plugin struct{ dataDir string }

func New() *Plugin                    { return &Plugin{dataDir: filepath.Join("data", "quote")} }
func (p *Plugin) Name() string        { return "quote" }
func (p *Plugin) Description() string { return "Render a replied message as a shareable quote image" }
func (p *Plugin) Init() error         { return os.MkdirAll(p.dataDir, 0o700) }
func (p *Plugin) Shutdown() error     { return nil }
func (p *Plugin) Capabilities() []execution.Capability {
	return []execution.Capability{{ID: "quote", Name: "Quote", Description: "Generate quote images from Telegram messages", Category: "Media", Surfaces: execution.SurfaceUserbot}}
}
func (p *Plugin) Commands() []core.Command {
	return []core.Command{{Name: "qbot", Aliases: []string{"quote", "q"}, Description: "Create a quote image from a replied message", Usage: ".qbot (reply to a message)", Category: "Media", Permission: core.PermissionSudo, Surfaces: execution.SurfaceUserbot, Timeout: 2 * time.Minute, Handler: p.handle}}
}

func (p *Plugin) handle(ctx *core.Context) error {
	if ctx.Message == nil || ctx.Message.ReplyToID == 0 {
		return ctx.EditOrReply("⚠️ Reply to a message with .qbot")
	}
	reply, err := ctx.GetReply()
	if err != nil || reply == nil {
		return ctx.EditOrReply("❌ Unable to load the replied message.")
	}
	_ = ctx.EditOrReply("⏳ Generating quote...")

	name := p.resolveAuthor(ctx, reply)
	text := strings.TrimSpace(reply.Text)
	if len([]rune(text)) > 1200 {
		text = string([]rune(text)[:1200]) + "…"
	}

	var mediaPath string
	if reply.HasMedia() {
		mediaPath, _ = ctx.Media().DownloadMedia(p.dataDir)
		if mediaPath != "" {
			defer os.Remove(mediaPath)
		}
	}

	path := filepath.Join(p.dataDir, fmt.Sprintf("quote-%d.jpg", time.Now().UnixNano()))
	if err := render(path, name, text, reply, mediaPath); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Quote rendering failed: %v", err))
	}
	defer os.Remove(path)

	if _, err := ctx.SendMedia("photo", path, ""); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send quote image: %v", err))
	}
	return nil
}

func (p *Plugin) resolveAuthor(ctx *core.Context, reply *core.Message) string {
	if reply == nil {
		return "Unknown"
	}
	if ctx != nil && ctx.Sender != nil && ctx.Sender.ID == reply.SenderID {
		if name := displayUserName(ctx.Sender.FirstName, ctx.Sender.LastName, ctx.Sender.Username); name != "" {
			return name
		}
	}
	if ctx != nil && reply.SenderID != 0 && ctx.Resolver != nil {
		peer, _, err := ctx.Resolver.ResolveUser(ctx.Ctx, strconv.FormatInt(reply.SenderID, 10))
		if err == nil {
			if inputUser, ok := peerToInputUser(peer); ok && ctx.Svc != nil {
				full, fullErr := ctx.Svc.GetFullUser(ctx.Ctx, inputUser)
				if fullErr == nil && full != nil {
					for _, item := range full.Users {
						if u, ok := item.(*tg.User); ok && u.ID == reply.SenderID {
							if name := displayUserName(u.FirstName, u.LastName, u.Username); name != "" {
								return name
							}
						}
					}
				}
			}
		}
	}
	if reply.SenderID != 0 {
		return fmt.Sprintf("User %d", reply.SenderID)
	}
	return "Unknown"
}

func peerToInputUser(peer tg.InputPeerClass) (tg.InputUserClass, bool) {
	u, ok := peer.(*tg.InputPeerUser)
	if !ok || u == nil || u.UserID == 0 || u.AccessHash == 0 {
		return nil, false
	}
	return &tg.InputUser{UserID: u.UserID, AccessHash: u.AccessHash}, true
}

func displayUserName(first, last, username string) string {
	name := strings.TrimSpace(strings.TrimSpace(first + " " + last))
	if name != "" {
		return name
	}
	if username != "" {
		return "@" + strings.TrimPrefix(username, "@")
	}
	return ""
}

func mediaPlaceholder(mediaType string) string {
	labels := map[string]string{"photo": "Photo", "video": "Video", "sticker": "Sticker", "audio": "Audio", "voice": "Voice message", "document": "Document"}
	if label := labels[strings.ToLower(strings.TrimSpace(mediaType))]; label != "" {
		return "[" + label + "]"
	}
	return "[Media]"
}

type quoteTheme struct {
	background color.RGBA
	panel      color.RGBA
	primary    color.RGBA
	secondary  color.RGBA
	muted      color.RGBA
	accent     color.RGBA
}

var quoteThemes = []quoteTheme{
	{color.RGBA{18, 19, 23, 255}, color.RGBA{28, 30, 36, 255}, color.RGBA{244, 246, 250, 255}, color.RGBA{211, 214, 221, 255}, color.RGBA{132, 137, 150, 255}, color.RGBA{91, 157, 255, 255}},
	{color.RGBA{19, 20, 24, 255}, color.RGBA{31, 30, 38, 255}, color.RGBA{245, 245, 250, 255}, color.RGBA{214, 211, 224, 255}, color.RGBA{139, 135, 153, 255}, color.RGBA{171, 103, 255, 255}},
	{color.RGBA{18, 22, 22, 255}, color.RGBA{28, 34, 34, 255}, color.RGBA{242, 247, 245, 255}, color.RGBA{207, 220, 215, 255}, color.RGBA{126, 145, 137, 255}, color.RGBA{57, 190, 150, 255}},
	{color.RGBA{22, 20, 18, 255}, color.RGBA{36, 31, 28, 255}, color.RGBA{249, 245, 239, 255}, color.RGBA{222, 212, 201, 255}, color.RGBA{151, 138, 125, 255}, color.RGBA{244, 157, 76, 255}},
}

func themeFor(id int64) quoteTheme {
	if id < 0 {
		id = -id
	}
	return quoteThemes[id%int64(len(quoteThemes))]
}

func render(path, name, text string, msg *core.Message, mediaPath string) error {
	const width = 1080
	const horizontalPad = 76
	const headerHeight = 170
	const lineHeight = 43
	const textWidth = width - horizontalPad*2
	const radius = 30

	if msg == nil {
		return fmt.Errorf("message is nil")
	}
	if strings.TrimSpace(text) == "" {
		text = mediaPlaceholder(msg.MediaType)
	}

	theme := themeFor(msg.SenderID)
	nameFace := loadFont(31)
	bodyFace := loadFont(28)
	metaFace := loadFont(20)

	lines := wrapToWidth(text, bodyFace, textWidth)
	if len(lines) == 0 {
		lines = []string{mediaPlaceholder(msg.MediaType)}
	}

	mediaImg, mediaOK := loadMediaPreview(mediaPath, 360)
	mediaHeight := 0
	if mediaOK {
		mediaHeight = mediaImg.Bounds().Dy() + 28
	}

	height := headerHeight + len(lines)*lineHeight + 72 + mediaHeight
	if height < 430 {
		height = 430
	}

	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: theme.background}, image.Point{}, draw.Src)
	drawRoundedRect(img, image.Rect(26, 26, width-26, height-26), radius, theme.panel)

	// Accent rail mirrors the strong visual identity of the established Python renderers.
	drawRoundedRect(img, image.Rect(26, 26, 35, height-26), 4, theme.accent)

	// Avatar is deliberately generated locally when Telegram profile media is unavailable.
	// This keeps rendering deterministic and avoids making quote generation depend on a second API call.
	avatarCenter := image.Pt(105, 112)
	drawCircle(img, avatarCenter, 49, theme.accent)
	initials := initialsFor(name)
	drawCenteredString(img, initials, avatarCenter.X, avatarCenter.Y+10, loadFont(27), color.White)

	nameX := 176
	d := &font.Drawer{Dst: img, Face: nameFace, Src: image.NewUniform(theme.primary), Dot: fixed.P(nameX, 101)}
	d.DrawString(truncateToWidth(name, nameFace, width-nameX-horizontalPad))

	meta := "Telegram"
	if !msg.Date.IsZero() {
		meta += "  •  " + msg.Date.Local().Format("02 Jan 2006, 15:04")
	}
	d.Face = metaFace
	d.Src = image.NewUniform(theme.muted)
	d.Dot = fixed.P(nameX, 133)
	d.DrawString(meta)

	// Thin accent separator.
	drawRoundedRect(img, image.Rect(horizontalPad, 181, width-horizontalPad, 184), 2, theme.accent)

	y := 224
	d.Face = bodyFace
	d.Src = image.NewUniform(theme.secondary)
	for _, line := range lines {
		d.Dot = fixed.P(horizontalPad, y)
		d.DrawString(line)
		y += lineHeight
	}

	if mediaOK {
		y += 14
		x := (width - mediaImg.Bounds().Dx()) / 2
		if mediaImg.Bounds().Dx() > textWidth {
			x = horizontalPad
		}
		drawRoundedImage(img, mediaImg, image.Pt(x, y), 18)
	} else if msg.MediaType != "" {
		y += 18
		d.Face = metaFace
		d.Src = image.NewUniform(theme.accent)
		d.Dot = fixed.P(horizontalPad, y+20)
		d.DrawString(mediaPlaceholder(msg.MediaType))
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 94})
}

func loadMediaPreview(path string, maxWidth int) (image.Image, bool) {
	if path == "" {
		return nil, false
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, false
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	if err != nil {
		return nil, false
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, false
	}
	if b.Dx() <= maxWidth {
		return img, true
	}
	h := b.Dy() * maxWidth / b.Dx()
	return resizeNearest(img, maxWidth, h), true
}

func resizeNearest(src image.Image, width, height int) image.Image {
	dst := image.NewRGBA(image.Rect(0, 0, width, height))
	sb := src.Bounds()
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			sx := sb.Min.X + x*sb.Dx()/width
			sy := sb.Min.Y + y*sb.Dy()/height
			dst.Set(x, y, src.At(sx, sy))
		}
	}
	return dst
}

func loadFont(size float64) font.Face {
	candidates := []string{
		"/usr/share/fonts/truetype/noto/NotoSans-Regular.ttf",
		"/usr/share/fonts/opentype/noto/NotoSans-Regular.ttf",
		"/usr/share/fonts/truetype/dejavu/DejaVuSans.ttf",
		"/usr/share/fonts/truetype/liberation2/LiberationSans-Regular.ttf",
	}
	for _, path := range candidates {
		data, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		f, err := opentype.Parse(data)
		if err != nil {
			continue
		}
		face, err := opentype.NewFace(f, &opentype.FaceOptions{Size: size, DPI: 72, Hinting: font.HintingFull})
		if err == nil {
			return face
		}
	}
	return basicfont.Face7x13
}

func wrapToWidth(s string, face font.Face, maxWidth int) []string {
	paragraphs := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	var out []string
	for _, paragraph := range paragraphs {
		words := strings.Fields(paragraph)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			if line == "" {
				line = word
				continue
			}
			candidate := line + " " + word
			if font.MeasureString(face, candidate).Ceil() <= maxWidth {
				line = candidate
				continue
			}
			out = append(out, line)
			line = word
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
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

func initialsFor(name string) string {
	words := strings.Fields(name)
	if len(words) == 0 {
		return "?"
	}
	var out []rune
	for _, word := range words {
		r := []rune(word)
		if len(r) > 0 {
			out = append(out, unicode.ToUpper(r[0]))
		}
		if len(out) == 2 {
			break
		}
	}
	return string(out)
}

func drawCenteredString(dst draw.Image, s string, x, y int, face font.Face, c color.Color) {
	w := font.MeasureString(face, s).Ceil()
	d := &font.Drawer{Dst: dst, Src: image.NewUniform(c), Face: face, Dot: fixed.P(x-w/2, y)}
	d.DrawString(s)
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
	// Center strips.
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

func drawRoundedImage(dst draw.Image, src image.Image, at image.Point, radius int) {
	r := src.Bounds()
	mask := image.NewRGBA(r)
	drawRoundedRect(mask, r, radius, color.White)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		for x := r.Min.X; x < r.Max.X; x++ {
			if mask.RGBAAt(x, y).A > 0 {
				dst.Set(at.X+x-r.Min.X, at.Y+y-r.Min.Y, src.At(x, y))
			}
		}
	}
}
