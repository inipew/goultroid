package core

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/gotd/td/tg"
)

// TelegramServicer defines message, media, and chat actions on Telegram.
// Mockable for unit testing without a live MTProto connection.
type TelegramServicer interface {
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error)
	EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error
	DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error
	React(ctx context.Context, peer tg.InputPeerClass, msgID int, emoji string) error
	GetMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) (*tg.Message, error)
	PinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, silent bool) error
	UnpinMessage(ctx context.Context, peer tg.InputPeerClass, msgID int) error
	ForwardMessages(ctx context.Context, fromPeer, toPeer tg.InputPeerClass, msgIDs []int) error
	DownloadFile(ctx context.Context, location tg.InputFileLocationClass, dstPath string) error

	// Moderation & Admin actions
	BanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	UnbanUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	KickUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	MuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, untilDate int) error
	UnmuteUser(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	PurgeMessages(ctx context.Context, peer tg.InputPeerClass, topicID int, fromID, toID int) (int, error)
}

// MediaInfo stores metadata and download location for message attachments.
type MediaInfo struct {
	Type     string // "photo", "video", "document", "audio", "voice", "sticker"
	FileName string
	MimeType string
	Size     int64
	Location tg.InputFileLocationClass
}

// Message represents a high-level Telegram message.
type Message struct {
	ID        int
	SenderID  int64
	TopicID   int // Root ID of the forum topic / thread, if sent in a topic
	Text      string
	Date      time.Time
	ReplyToID int
	MediaType string
	Media     *MediaInfo
}

// HasMedia returns true if the message has an attached downloadable media.
func (m *Message) HasMedia() bool {
	return m != nil && m.Media != nil && m.Media.Location != nil
}

// Chat represents the chat in which an event occurred.
type Chat struct {
	ID       int64
	Title    string
	Username string
	Type     string // "private", "group", "supergroup", "channel"
}

// User represents the user who sent the message.
type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
}

// Context is passed to each command handler, providing clean abstractions.
type Context struct {
	Ctx context.Context

	Command string
	Args    []string
	RawArgs string

	Message *Message
	Chat    *Chat
	Sender  *User
	Perms   *Permissions

	Svc    TelegramServicer
	PeerID tg.InputPeerClass
}

// SenderID returns the ID of the sender if present.
func (c *Context) SenderID() int64 {
	if c != nil && c.Sender != nil {
		return c.Sender.ID
	}
	return 0
}

// Reply sends a response message to the same chat.
func (c *Context) Reply(text string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	sent, err := c.Svc.SendMessage(c.Ctx, c.PeerID, text)
	if err != nil {
		return fmt.Errorf("reply failed: %w", err)
	}
	if sent != nil && c.Message != nil {
		c.Message.ID = sent.ID
	}
	return nil
}

// Edit edits the command message (if sent by self) or a previously sent response.
func (c *Context) Edit(text string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to edit")
	}
	return c.Svc.EditMessage(c.Ctx, c.PeerID, c.Message.ID, text)
}

// Delete deletes the current message.
func (c *Context) Delete() error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to delete")
	}
	return c.Svc.DeleteMessage(c.Ctx, c.PeerID, []int{c.Message.ID})
}

// React sends an emoji reaction to the message.
func (c *Context) React(emoji string) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ID == 0 {
		return errors.New("no message to react to")
	}
	return c.Svc.React(c.Ctx, c.PeerID, c.Message.ID, emoji)
}

// targetMsgID returns the replied-to message ID if present, otherwise current message ID.
func (c *Context) targetMsgID() int {
	if c.Message != nil {
		if c.Message.ReplyToID != 0 {
			return c.Message.ReplyToID
		}
		return c.Message.ID
	}
	return 0
}

// Pin pins the current message or the replied-to message.
func (c *Context) Pin(silent bool) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to pin")
	}
	return c.Svc.PinMessage(c.Ctx, c.PeerID, targetID, silent)
}

// Unpin unpins the current message or the replied-to message.
func (c *Context) Unpin() error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to unpin")
	}
	return c.Svc.UnpinMessage(c.Ctx, c.PeerID, targetID)
}

// Forward forwards the message (or replied message) to another peer.
func (c *Context) Forward(toPeer tg.InputPeerClass) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	if toPeer == nil {
		return errors.New("destination peer is nil")
	}
	targetID := c.targetMsgID()
	if targetID == 0 {
		return errors.New("no message to forward")
	}
	return c.Svc.ForwardMessages(c.Ctx, c.PeerID, toPeer, []int{targetID})
}

// ForwardToSelf forwards the message (or replied message) to Saved Messages.
func (c *Context) ForwardToSelf() error {
	return c.Forward(&tg.InputPeerSelf{})
}

// DownloadMedia downloads the media attached to the message or the replied message.
func (c *Context) DownloadMedia(destDir string) (string, error) {
	if c.Svc == nil {
		return "", errors.New("telegram service not initialized")
	}

	var media *MediaInfo
	if c.Message != nil && c.Message.Media != nil {
		media = c.Message.Media
	}

	if media == nil || media.Location == nil {
		replied, err := c.GetReply()
		if err == nil && replied != nil && replied.Media != nil {
			media = replied.Media
		}
	}

	if media == nil || media.Location == nil {
		return "", errors.New("no media found in message or reply")
	}

	if err := os.MkdirAll(destDir, 0755); err != nil {
		return "", fmt.Errorf("failed to create destination directory: %w", err)
	}

	fileName := media.FileName
	if fileName == "" {
		ext := ".bin"
		switch media.Type {
		case "photo":
			ext = ".jpg"
		case "video":
			ext = ".mp4"
		case "audio":
			ext = ".mp3"
		case "voice":
			ext = ".ogg"
		case "sticker":
			ext = ".webp"
		}
		fileName = fmt.Sprintf("media_%d%s", time.Now().UnixNano(), ext)
	}

	filePath := filepath.Join(destDir, fileName)
	if err := c.Svc.DownloadFile(c.Ctx, media.Location, filePath); err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}

	return filePath, nil
}

// GetReply retrieves the message that was replied to, if any.
func (c *Context) GetReply() (*Message, error) {
	if c.Svc == nil {
		return nil, errors.New("telegram service not initialized")
	}
	if c.Message == nil || c.Message.ReplyToID == 0 {
		return nil, nil
	}
	msg, err := c.Svc.GetMessage(c.Ctx, c.PeerID, c.Message.ReplyToID)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch reply message: %w", err)
	}
	if msg == nil {
		return nil, nil
	}

	res := &Message{
		ID:   msg.ID,
		Text: msg.Message,
		Date: time.Unix(int64(msg.Date), 0),
	}

	if msg.FromID != nil {
		if u, ok := msg.FromID.(*tg.PeerUser); ok {
			res.SenderID = u.UserID
		}
	}

	if msg.ReplyTo != nil {
		if h, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok {
			res.ReplyToID = h.ReplyToMsgID
			if h.ForumTopic || h.ReplyToTopID != 0 {
				if h.ReplyToTopID != 0 {
					res.TopicID = h.ReplyToTopID
				} else {
					res.TopicID = h.ReplyToMsgID
				}
			}
		}
	}

	// Extract media if present in reply
	if msg.Media != nil {
		res.Media = ExtractMediaFromTG(msg.Media)
		if res.Media != nil {
			res.MediaType = res.Media.Type
		}
	}

	return res, nil
}

// TopicID returns the forum topic ID of the command or replied message, if any.
func (c *Context) TopicID() int {
	if c.Message != nil && c.Message.TopicID != 0 {
		return c.Message.TopicID
	}
	return 0
}

// ResolveTargetUser extracts the target user's InputPeer and UserID from args (if ID numeric) or from replied message.
func (c *Context) ResolveTargetUser() (tg.InputPeerClass, int64, error) {
	if len(c.Args) > 0 {
		if uid, err := strconv.ParseInt(c.Args[0], 10, 64); err == nil && uid != 0 {
			return &tg.InputPeerUser{UserID: uid}, uid, nil
		}
	}

	reply, err := c.GetReply()
	if err == nil && reply != nil && reply.SenderID != 0 {
		return &tg.InputPeerUser{UserID: reply.SenderID}, reply.SenderID, nil
	}

	return nil, 0, errors.New("please provide a valid user ID or reply to a user's message")
}

// Ban bans a user from the chat.
func (c *Context) Ban(user tg.InputPeerClass, untilDate int) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.BanUser(c.Ctx, c.PeerID, user, untilDate)
}

// Unban removes ban restrictions on a user.
func (c *Context) Unban(user tg.InputPeerClass) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.UnbanUser(c.Ctx, c.PeerID, user)
}

// Kick kicks a user from the chat.
func (c *Context) Kick(user tg.InputPeerClass) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.KickUser(c.Ctx, c.PeerID, user)
}

// Mute mutes a user in the chat until the specified unix timestamp (0 for permanent).
func (c *Context) Mute(user tg.InputPeerClass, untilDate int) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.MuteUser(c.Ctx, c.PeerID, user, untilDate)
}

// Unmute unmutes a user in the chat.
func (c *Context) Unmute(user tg.InputPeerClass) error {
	if c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return errors.New("peer is nil")
	}
	return c.Svc.UnmuteUser(c.Ctx, c.PeerID, user)
}

// Purge safely purges messages from the replied message up to the current command message,
// taking into account forum topic scope so messages in other topics are never affected.
func (c *Context) Purge() (int, error) {
	if c.Svc == nil {
		return 0, errors.New("telegram service not initialized")
	}
	if c.PeerID == nil {
		return 0, errors.New("peer is nil")
	}
	if c.Message == nil || c.Message.ReplyToID == 0 {
		return 0, errors.New("purge must be a reply to a message")
	}

	topicID := c.TopicID()
	if topicID == 0 {
		reply, _ := c.GetReply()
		if reply != nil && reply.TopicID != 0 {
			topicID = reply.TopicID
		}
	}

	fromID := c.Message.ReplyToID
	toID := c.Message.ID
	return c.Svc.PurgeMessages(c.Ctx, c.PeerID, topicID, fromID, toID)
}

// ExtractMediaFromTG parses raw tg.MessageMediaClass into core.MediaInfo.
func ExtractMediaFromTG(media tg.MessageMediaClass) *MediaInfo {
	if media == nil {
		return nil
	}

	switch m := media.(type) {
	case *tg.MessageMediaPhoto:
		photo, ok := m.Photo.(*tg.Photo)
		if !ok || len(photo.Sizes) == 0 {
			return nil
		}

		var largestSize string
		var largestBytes int64
		for _, s := range photo.Sizes {
			switch sz := s.(type) {
			case *tg.PhotoSize:
				largestSize = sz.Type
				largestBytes = int64(sz.Size)
			case *tg.PhotoSizeProgressive:
				largestSize = sz.Type
				if len(sz.Sizes) > 0 {
					largestBytes = int64(sz.Sizes[len(sz.Sizes)-1])
				}
			}
		}

		return &MediaInfo{
			Type:     "photo",
			FileName: fmt.Sprintf("photo_%d.jpg", photo.ID),
			MimeType: "image/jpeg",
			Size:     largestBytes,
			Location: photo.AsInputPhotoFileLocation(largestSize),
		}

	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil
		}

		mediaType := "document"
		fileName := fmt.Sprintf("document_%d", doc.ID)

		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeFilename:
				fileName = a.FileName
			case *tg.DocumentAttributeVideo:
				mediaType = "video"
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					mediaType = "voice"
				} else {
					mediaType = "audio"
				}
			case *tg.DocumentAttributeSticker:
				mediaType = "sticker"
			}
		}

		return &MediaInfo{
			Type:     mediaType,
			FileName: fileName,
			MimeType: doc.MimeType,
			Size:     doc.Size,
			Location: doc.AsInputDocumentFileLocation(""),
		}
	}

	return nil
}

// IsPrivate returns true if the chat is a 1-on-1 private chat.
func (c *Context) IsPrivate() bool {
	return c.Chat != nil && c.Chat.Type == "private"
}

// IsGroup returns true if the chat is a group or supergroup.
func (c *Context) IsGroup() bool {
	return c.Chat != nil && (c.Chat.Type == "group" || c.Chat.Type == "supergroup")
}

// IsChannel returns true if the chat is a broadcast channel.
func (c *Context) IsChannel() bool {
	return c.Chat != nil && c.Chat.Type == "channel"
}

// IsOwner returns true if the sender is the Owner.
func (c *Context) IsOwner() bool {
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsOwner(c.Sender.ID)
}

// IsSudo returns true if the sender has Sudo or Owner privileges.
func (c *Context) IsSudo() bool {
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsSudo(c.Sender.ID)
}
