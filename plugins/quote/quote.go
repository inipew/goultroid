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
	if text == "" {
		text = mediaPlaceholder(reply.MediaType)
	}
	if text == "" {
		text = "[message]"
	}
	if len([]rune(text)) > 900 {
		text = string([]rune(text)[:900]) + "…"
	}

	path := filepath.Join(p.dataDir, fmt.Sprintf("quote-%d.jpg", time.Now().UnixNano()))
	if err := render(path, name, text, reply.Date); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Quote rendering failed: %v", err))
	}
	defer os.Remove(path)

	// MediaFacade.SendMedia takes (mediaType, filePath, caption).
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

	// The command sender is not necessarily the author of the replied message.
	// Resolve the replied sender through the central peer resolver instead.
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
	labels := map[string]string{
		"photo": "photo",
		"video": "video",
		"sticker": "sticker",
		"audio": "audio",
		"voice": "voice message",
		"document": "document",
	}
	if label := labels[strings.ToLower(strings.TrimSpace(mediaType))]; label != "" {
		return "[" + label + "]"
	}
	return "[media]"
}

func render(path, name, text string, messageDate time.Time) error {
	const (
		width      = 900
		pad        = 48
		textWidth  = width - pad*2
		lineHeight = 30
	)

	face := loadFont(24)
	metaFace := loadFont(15)
	lines := wrapToWidth(text, face, textWidth)
	if len(lines) == 0 {
		lines = []string{""}
	}

	height := 118 + len(lines)*lineHeight + 28
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 28, G: 29, B: 33, A: 255}}, image.Point{}, draw.Src)
	draw.Draw(img, image.Rect(0, 0, 12, height), &image.Uniform{C: color.RGBA{R: 90, G: 150, B: 240, A: 255}}, image.Point{}, draw.Src)

	name = strings.TrimSpace(name)
	if name == "" {
		name = "Unknown"
	}
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(pad, 54)}
	d.DrawString(name)

	meta := "Telegram"
	if !messageDate.IsZero() {
		meta += " • " + messageDate.Local().Format("2006-01-02 15:04")
	}
	d.Face = metaFace
	d.Src = image.NewUniform(color.RGBA{R: 150, G: 154, B: 164, A: 255})
	d.Dot = fixed.P(pad, 82)
	d.DrawString(meta)

	d.Face = face
	d.Src = image.NewUniform(color.RGBA{R: 232, G: 234, B: 238, A: 255})
	y := 124
	for _, line := range lines {
		d.Dot = fixed.P(pad, y)
		d.DrawString(line)
		y += lineHeight
	}

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 92})
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
