package quote

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/image/font"
	"golang.org/x/image/font/basicfont"
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
	name := "Unknown"
	if ctx.Sender != nil && ctx.Sender.ID == reply.SenderID {
		name = strings.TrimSpace(strings.TrimSpace(ctx.Sender.FirstName + " " + ctx.Sender.LastName))
	}
	if name == "Unknown" && reply.SenderID != 0 {
		name = fmt.Sprintf("User %d", reply.SenderID)
	}
	text := strings.TrimSpace(reply.Text)
	if text == "" {
		text = "[" + strings.Title(reply.MediaType) + "]"
	}
	if len([]rune(text)) > 900 {
		text = string([]rune(text)[:900]) + "…"
	}
	path := filepath.Join(p.dataDir, fmt.Sprintf("quote-%d.jpg", time.Now().UnixNano()))
	if err := render(path, name, text); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Quote rendering failed: %v", err))
	}
	defer os.Remove(path)
	if _, err := ctx.SendMedia(path, "photo", ""); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send quote image: %v", err))
	}
	return nil
}

func render(path, name, text string) error {
	const pad = 36
	face := basicfont.Face7x13
	lines := wrap(text, 78)
	if len(lines) == 0 {
		lines = []string{""}
	}
	height := 100 + len(lines)*22
	width := 900
	img := image.NewRGBA(image.Rect(0, 0, width, height))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 28, G: 29, B: 33, A: 255}}, image.Point{}, draw.Src)
	// Accent bar and simple avatar substitute keep the renderer dependency-free.
	draw.Draw(img, image.Rect(0, 0, 12, height), &image.Uniform{C: color.RGBA{R: 90, G: 150, B: 240, A: 255}}, image.Point{}, draw.Src)
	d := &font.Drawer{Dst: img, Src: image.NewUniform(color.White), Face: face, Dot: fixed.P(pad, 48)}
	d.DrawString(name)
	d.Src = image.NewUniform(color.RGBA{R: 220, G: 222, B: 228, A: 255})
	y := 82
	for _, line := range lines {
		d.Dot = fixed.P(pad, y)
		d.DrawString(asciiSafe(line))
		y += 22
	}
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return jpeg.Encode(f, img, &jpeg.Options{Quality: 92})
}

func wrap(s string, max int) []string {
	words := strings.Fields(strings.ReplaceAll(s, "\r", ""))
	if len(words) == 0 {
		return nil
	}
	var out []string
	line := ""
	for _, word := range words {
		if len([]rune(word)) > max {
			for len([]rune(word)) > max {
				out = append(out, string([]rune(word)[:max]))
				word = string([]rune(word)[max:])
			}
		}
		if line == "" {
			line = word
		} else if len([]rune(line))+1+len([]rune(word)) <= max {
			line += " " + word
		} else {
			out = append(out, line)
			line = word
		}
	}
	if line != "" {
		out = append(out, line)
	}
	return out
}
func asciiSafe(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= 32 && r <= 126 {
			b.WriteRune(r)
		} else {
			b.WriteRune('?')
		}
	}
	return b.String()
}
