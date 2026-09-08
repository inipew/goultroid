package quote

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/gotd/td/tg"

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

	author := p.resolveAuthorInfo(ctx, reply)
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
	var avatarPath string
	if reply.SenderID != 0 {
		avatarPath = p.downloadAvatar(ctx, reply.SenderID)
		if avatarPath != "" {
			defer os.Remove(avatarPath)
		}
	}

	// Resolve reply-to message if the quoted message itself replied to another message
	var replyPreview *ReplyPreview
	if reply.ReplyToID != 0 {
		replyPreview = p.resolveReplyPreview(ctx, reply.ReplyToID)
	}

	// Allow user to supply custom badge override via args (e.g. .qbot Bot Mirror)
	badge := strings.TrimSpace(ctx.RawArgs)
	if badge == "" {
		badge = author.Badge
	}

	path := filepath.Join(p.dataDir, fmt.Sprintf("quote-%d.png", time.Now().UnixNano()))
	if err := renderV3WithOpts(RenderOptions{
		Path:         path,
		Name:         author.Name,
		Badge:        badge,
		Text:         text,
		Message:      reply,
		MediaPath:    mediaPath,
		AvatarPath:   avatarPath,
		ReplyPreview: replyPreview,
		SenderID:     reply.SenderID,
	}); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Quote rendering failed: %v", err))
	}
	defer os.Remove(path)
	if _, err := ctx.SendMedia("photo", path, ""); err != nil {
		return ctx.EditOrReply(fmt.Sprintf("❌ Failed to send quote image: %v", err))
	}
	return nil
}

type profilePhotoDownloader interface {
	DownloadUserProfilePhoto(context.Context, tg.InputUserClass, string) error
}

func (p *Plugin) downloadAvatar(ctx *core.Context, userID int64) string {
	if ctx == nil || ctx.Svc == nil || ctx.Resolver == nil {
		return ""
	}
	service, ok := ctx.Svc.(profilePhotoDownloader)
	if !ok {
		return ""
	}
	peer, _, err := ctx.Resolver.ResolveUser(ctx.Ctx, strconv.FormatInt(userID, 10))
	if err != nil {
		return ""
	}
	inputPeer, ok := peer.(*tg.InputPeerUser)
	if !ok || inputPeer == nil || inputPeer.AccessHash == 0 {
		return ""
	}
	path := filepath.Join(p.dataDir, fmt.Sprintf("avatar-%d-%d.jpg", userID, time.Now().UnixNano()))
	inputUser := &tg.InputUser{UserID: inputPeer.UserID, AccessHash: inputPeer.AccessHash}
	if err := service.DownloadUserProfilePhoto(ctx.Ctx, inputUser, path); err != nil {
		_ = os.Remove(path)
		return ""
	}
	return path
}

type authorInfo struct {
	Name       string
	Badge      string
	ColorIndex int
	IsBot      bool
}

func (p *Plugin) resolveAuthorInfo(ctx *core.Context, reply *core.Message) authorInfo {
	info := authorInfo{
		Name:       "Unknown",
		ColorIndex: 4, // Default Cyan
	}
	if reply == nil {
		return info
	}
	info.ColorIndex = int(absInt64(reply.SenderID) % 7)

	if ctx != nil && ctx.Sender != nil && ctx.Sender.ID == reply.SenderID {
		if name := displayUserName(ctx.Sender.FirstName, ctx.Sender.LastName, ctx.Sender.Username); name != "" {
			info.Name = name
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
								info.Name = name
							}
							if u.Bot {
								info.IsBot = true
								info.Badge = "bot"
							}
						}
					}
				}
			}
		}
	}
	if info.Name == "Unknown" && reply.SenderID != 0 {
		info.Name = fmt.Sprintf("User %d", reply.SenderID)
	}
	return info
}

func (p *Plugin) resolveAuthor(ctx *core.Context, reply *core.Message) string {
	return p.resolveAuthorInfo(ctx, reply).Name
}

func (p *Plugin) resolveReplyPreview(ctx *core.Context, replyToID int) *ReplyPreview {
	if ctx == nil || ctx.Svc == nil || replyToID == 0 {
		return &ReplyPreview{Text: "Deleted message"}
	}
	msg, err := ctx.Svc.GetMessage(ctx.Ctx, ctx.PeerID, replyToID)
	if err != nil || msg == nil {
		return &ReplyPreview{Text: "Deleted message"}
	}

	author := ""
	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			author = p.resolveUserName(ctx, u.UserID)
		} else if ch, ok := msg.FromID.(*tg.PeerChannel); ok {
			author = fmt.Sprintf("Channel %d", ch.ChannelID)
		} else if ch, ok := msg.FromID.(*tg.PeerChat); ok {
			author = fmt.Sprintf("Chat %d", ch.ChatID)
		}
	}
	text := strings.TrimSpace(msg.Message)
	if text == "" && msg.Media != nil {
		text = mediaPlaceholder("")
	}
	if text == "" {
		text = "Message"
	}
	if len([]rune(text)) > 60 {
		text = string([]rune(text)[:57]) + "…"
	}
	return &ReplyPreview{
		Author: author,
		Text:   text,
	}
}

func (p *Plugin) resolveUserName(ctx *core.Context, userID int64) string {
	if ctx != nil && ctx.Sender != nil && ctx.Sender.ID == userID {
		return displayUserName(ctx.Sender.FirstName, ctx.Sender.LastName, ctx.Sender.Username)
	}
	if ctx != nil && ctx.Resolver != nil {
		peer, _, err := ctx.Resolver.ResolveUser(ctx.Ctx, strconv.FormatInt(userID, 10))
		if err == nil {
			if inputUser, ok := peerToInputUser(peer); ok && ctx.Svc != nil {
				full, err := ctx.Svc.GetFullUser(ctx.Ctx, inputUser)
				if err == nil && full != nil {
					for _, item := range full.Users {
						if u, ok := item.(*tg.User); ok && u.ID == userID {
							return displayUserName(u.FirstName, u.LastName, u.Username)
						}
					}
				}
			}
		}
	}
	if userID != 0 {
		return fmt.Sprintf("User %d", userID)
	}
	return "User"
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

func render(path, name, text string, msg *core.Message, mediaPath string) error {
	return renderV3(path, name, text, msg, mediaPath, "")
}
