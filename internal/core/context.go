package core

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gotd/td/tg"
)

// TelegramServicer defines message, media, and chat actions on Telegram.
// Mockable for unit testing without a live MTProto connection.
type TelegramServicer interface {
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error)
	SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error)
	EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error
	EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error
	EditMessageMarkupOnly(ctx context.Context, peer tg.InputPeerClass, msgID int, markup tg.ReplyMarkupClass) error
	EditInlineBotMessage(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, text string, markup tg.ReplyMarkupClass) error
	EditInlineBotMessageMarkup(ctx context.Context, inlineID tg.InputBotInlineMessageIDClass, markup tg.ReplyMarkupClass) error
	DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error
	AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error
	AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error
	AnswerInlineQueryOptions(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, opts InlineAnswerOptions) error
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
	PromoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass, title string) error
	DemoteAdmin(ctx context.Context, peer tg.InputPeerClass, user tg.InputPeerClass) error
	EditChatDefaultBannedRights(ctx context.Context, peer tg.InputPeerClass, rights tg.ChatBannedRights) error

	// Media upload actions
	SendMedia(ctx context.Context, peer tg.InputPeerClass, mediaType string, filePath string, caption string) (*tg.Message, error)

	// Info & Query actions
	GetFullUser(ctx context.Context, user tg.InputUserClass) (*tg.UsersUserFull, error)
	ResolveUsername(ctx context.Context, username string) (*tg.ContactsResolvedPeer, error)
	GetFullChat(ctx context.Context, peer tg.InputPeerClass) (*tg.MessagesChatFull, error)

	// User & Profile actions
	UpdateProfile(ctx context.Context, firstName, lastName, about *string) error
	BlockUser(ctx context.Context, peer tg.InputPeerClass) error
	UnblockUser(ctx context.Context, peer tg.InputPeerClass) error
	UploadProfilePhoto(ctx context.Context, filePath string) error
	DeleteProfilePhotos(ctx context.Context, limit int) (int, error)
	GetDialogs(ctx context.Context, limit int) ([]*Chat, error)
	GetContacts(ctx context.Context) ([]*User, error)
}

// InlineAnswerOptions carries Telegram's inline response policy (gallery/private/switch_pm).
type InlineAnswerOptions struct {
	Results    []tg.InputBotInlineResultClass
	NextOffset string
	CacheTime  int
	Gallery    bool
	Private    bool
	SwitchPM   *tg.InlineBotSwitchPM
	SwitchWebView *tg.InlineBotWebView
}

// MediaInfo stores metadata and download location for message attachments.
type MediaInfo struct {
	Type     string // "photo", "video", "document", "audio", "voice", "sticker"
	FileName string
	MimeType string
	Size     int64
	Width    int
	Height   int
	Duration int
	Location tg.InputFileLocationClass
}

// Message represents a high-level Telegram message.
type Message struct {
	ID         int
	SenderID   int64
	TopicID    int // Root ID of the forum topic / thread, if sent in a topic
	Text       string
	Date       time.Time
	ReplyToID  int
	MediaType  string
	Media      *MediaInfo
	IsOutgoing bool  // true when the message was sent by the bot owner (userbot)
	GroupedID  int64 // non-zero when this message belongs to an album (grouped media)
	Entities   []tg.MessageEntityClass
}

// HasMedia returns true if the message has an attached downloadable media.
func (m *Message) HasMedia() bool {
	return m != nil && m.Media != nil && m.Media.Location != nil
}

// IsAlbum returns true when the message is part of a grouped media album.
func (m *Message) IsAlbum() bool {
	return m != nil && m.GroupedID != 0
}

// Mentions returns all usernames and text mentions parsed from message entities or plain text.
func (m *Message) Mentions() []string {
	if m == nil {
		return nil
	}
	var mentions []string
	runes := []rune(m.Text)
	for _, ent := range m.Entities {
		switch e := ent.(type) {
		case *tg.MessageEntityMention:
			if e.Offset >= 0 && e.Offset+e.Length <= len(runes) {
				mentions = append(mentions, string(runes[e.Offset:e.Offset+e.Length]))
			}
		case *tg.MessageEntityMentionName:
			mentions = append(mentions, strconv.FormatInt(e.UserID, 10))
		}
	}
	if len(mentions) > 0 {
		return mentions
	}
	for _, word := range strings.Fields(m.Text) {
		if strings.HasPrefix(word, "@") && len(word) > 1 {
			mentions = append(mentions, word)
		}
	}
	return mentions
}

// URLs returns all URLs parsed from message entities or plain text.
func (m *Message) URLs() []string {
	if m == nil {
		return nil
	}
	var urls []string
	runes := []rune(m.Text)
	for _, ent := range m.Entities {
		switch e := ent.(type) {
		case *tg.MessageEntityURL:
			if e.Offset >= 0 && e.Offset+e.Length <= len(runes) {
				urls = append(urls, string(runes[e.Offset:e.Offset+e.Length]))
			}
		case *tg.MessageEntityTextURL:
			urls = append(urls, e.URL)
		}
	}
	if len(urls) > 0 {
		return urls
	}
	for _, word := range strings.Fields(m.Text) {
		if strings.HasPrefix(word, "http://") || strings.HasPrefix(word, "https://") {
			urls = append(urls, word)
		}
	}
	return urls
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

// Localizer defines the interface for internationalization and translation lookup.
type Localizer interface {
	T(key string, args ...any) string
}

// Context is passed to each command handler, providing clean abstractions.
type Context struct {
	Ctx context.Context

	CorrelationID string
	Command       string
	Args          []string
	RawArgs       string

	Message   *Message
	Album     []*Message
	Chat      *Chat
	Sender    *User
	Perms     *Permissions
	Principal *Principal

	// LastResponseID tracks the ID of the bot's most recent reply in this context
	LastResponseID int

	Svc       TelegramServicer
	PeerID    tg.InputPeerClass
	Resolver  PeerResolver
	Localizer Localizer
	EventBus  *EventBus
}

// Correlation returns the CorrelationID or an empty string if unset.
func (c *Context) Correlation() string {
	if c != nil {
		return c.CorrelationID
	}
	return ""
}

// SenderID returns the ID of the sender if present.
func (c *Context) SenderID() int64 {
	if c != nil && c.Sender != nil {
		return c.Sender.ID
	}
	return 0
}

// ChatID returns the ID of the chat if present.
func (c *Context) ChatID() int64 {
	if c != nil && c.Chat != nil {
		return c.Chat.ID
	}
	return 0
}

// IsAlbum returns true when the triggering message is part of a grouped media album.
func (c *Context) IsAlbum() bool {
	return c != nil && c.Message != nil && c.Message.IsAlbum()
}

// Mentions returns all user mentions in the triggering message.
func (c *Context) Mentions() []string {
	if c != nil && c.Message != nil {
		return c.Message.Mentions()
	}
	return nil
}

// URLs returns all URLs present in the triggering message.
func (c *Context) URLs() []string {
	if c != nil && c.Message != nil {
		return c.Message.URLs()
	}
	return nil
}

// Messages returns the dedicated MessagesFacade for messaging operations.
func (c *Context) Messages() *MessagesFacade {
	return &MessagesFacade{ctx: c}
}

// Admin returns the dedicated AdminFacade for moderation and group administration.
func (c *Context) Admin() *AdminFacade {
	return &AdminFacade{ctx: c}
}

// Media returns the dedicated MediaFacade for media handling and uploads.
func (c *Context) Media() *MediaFacade {
	return &MediaFacade{ctx: c}
}

// Peer returns the dedicated PeerFacade for user/chat resolution.
func (c *Context) Peer() *PeerFacade {
	return &PeerFacade{ctx: c}
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

// TopicID returns the forum topic ID of the command or replied message, if any.
func (c *Context) TopicID() int {
	if c.Message != nil && c.Message.TopicID != 0 {
		return c.Message.TopicID
	}
	return 0
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
		if errors.Is(err, ErrNotFound) {
			return nil, nil
		}
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

	if msg.Media != nil {
		res.Media = ExtractMediaFromTG(msg.Media)
		if res.Media != nil {
			res.MediaType = res.Media.Type
		}
	}

	return res, nil
}

// --- Backward-Compatible Delegator Methods ---

// Reply sends a response message to the same chat and records LastResponseID.
func (c *Context) Reply(text string) error {
	return c.Messages().Reply(text)
}

// ReplyMarkup sends a response message to the same chat with reply markup attached.
func (c *Context) ReplyMarkup(text string, markup tg.ReplyMarkupClass) error {
	return c.Messages().ReplyMarkup(text, markup)
}

// Edit edits the previously sent response (if Reply was called) or the outgoing command message.
func (c *Context) Edit(text string) error {
	return c.Messages().Edit(text)
}

// EditOrReply tries to edit the trigger (or last response) message in-place.
// Falls back to Reply if editing fails. Canonical userbot UX helper.
func (c *Context) EditOrReply(text string) error {
	return c.Messages().EditOrReply(text)
}

// EditOrReplyWithDelay updates trigger/response and schedules deletion after delay.
func (c *Context) EditOrReplyWithDelay(text string, delay time.Duration) error {
	return c.Messages().EditOrReplyWithDelay(text, delay)
}

// EditMarkup edits the response or command message with new text and markup.
func (c *Context) EditMarkup(text string, markup tg.ReplyMarkupClass) error {
	return c.Messages().EditMarkup(text, markup)
}

// T translates a key with optional formatting arguments using the configured Localizer.
func (c *Context) T(key string, args ...any) string {
	if c != nil && c.Localizer != nil {
		return c.Localizer.T(key, args...)
	}
	if len(args) == 0 {
		return key
	}
	var sb strings.Builder
	sb.WriteString(key)
	sb.WriteString(" [")
	for i, a := range args {
		if i > 0 {
			sb.WriteString(", ")
		}
		sb.WriteString(fmt.Sprint(a))
	}
	sb.WriteString("]")
	return sb.String()
}

// Delete deletes the current command message.
func (c *Context) Delete() error {
	return c.Messages().Delete()
}

// DeleteResponse deletes the bot's previously sent response message, if any.
func (c *Context) DeleteResponse() error {
	return c.Messages().DeleteResponse()
}

// React sends an emoji reaction to the message.
func (c *Context) React(emoji string) error {
	return c.Messages().React(emoji)
}

// Pin pins the current message or the replied-to message.
func (c *Context) Pin(silent bool) error {
	return c.Messages().Pin(silent)
}

// Unpin unpins the current message or the replied-to message.
func (c *Context) Unpin() error {
	return c.Messages().Unpin()
}

// Forward forwards the message (or replied message) to another peer.
func (c *Context) Forward(toPeer tg.InputPeerClass) error {
	return c.Messages().Forward(toPeer)
}

// ForwardToSelf forwards the message (or replied message) to Saved Messages.
func (c *Context) ForwardToSelf() error {
	return c.Messages().ForwardToSelf()
}

// Purge safely purges messages from the replied message up to the current command message.
func (c *Context) Purge() (int, error) {
	return c.Messages().Purge()
}

// Ban restricts a user in the chat until untilDate.
func (c *Context) Ban(user tg.InputPeerClass, untilDate int) error {
	return c.Admin().Ban(user, untilDate)
}

// Unban removes restrictions from a user in the chat.
func (c *Context) Unban(user tg.InputPeerClass) error {
	return c.Admin().Unban(user)
}

// Kick kicks a user from the chat.
func (c *Context) Kick(user tg.InputPeerClass) error {
	return c.Admin().Kick(user)
}

// Mute mutes a user in the chat until untilDate.
func (c *Context) Mute(user tg.InputPeerClass, untilDate int) error {
	return c.Admin().Mute(user, untilDate)
}

// Unmute unmutes a user in the chat.
func (c *Context) Unmute(user tg.InputPeerClass) error {
	return c.Admin().Unmute(user)
}

// Promote promotes a user to administrator in the current chat.
func (c *Context) Promote(user tg.InputPeerClass, title string) error {
	return c.Admin().Promote(user, title)
}

// Demote demotes an administrator to a regular member in the current chat.
func (c *Context) Demote(user tg.InputPeerClass) error {
	return c.Admin().Demote(user)
}

// EditChatDefaultBannedRights updates default permissions / locks for all members in the chat.
func (c *Context) EditChatDefaultBannedRights(rights tg.ChatBannedRights) error {
	return c.Admin().SetChatPermissions(rights)
}

// DownloadMedia downloads the media attached to the message or the replied message.
func (c *Context) DownloadMedia(destDir string) (string, error) {
	return c.Media().DownloadMedia(destDir)
}

// SendMedia sends a media file to the chat.
func (c *Context) SendMedia(mediaType string, filePath string, caption string) (*Message, error) {
	return c.Media().SendMedia(mediaType, filePath, caption)
}

// SendFile uploads and sends a file/document to the chat.
func (c *Context) SendFile(filePath, caption string) error {
	return c.Media().SendFile(filePath, caption)
}

// SendPhoto uploads and sends a photo to the chat.
func (c *Context) SendPhoto(filePath, caption string) error {
	return c.Media().SendPhoto(filePath, caption)
}

// SendSticker uploads and sends a sticker to the chat.
func (c *Context) SendSticker(filePath string) error {
	return c.Media().SendSticker(filePath)
}

// SendAudio uploads and sends an audio file to the chat.
func (c *Context) SendAudio(filePath, caption string) error {
	return c.Media().SendAudio(filePath, caption)
}

// ResolveUser resolves a user reference using the injected PeerResolver.
func (c *Context) ResolveUser(ref string) (tg.InputPeerClass, int64, error) {
	return c.Peer().ResolveUser(ref)
}

// ResolveChat resolves a chat reference using the injected PeerResolver.
func (c *Context) ResolveChat(ref string) (tg.InputPeerClass, error) {
	return c.Peer().ResolveChat(ref)
}

// ResolveTargetUser extracts the target user's InputPeer and UserID from args or reply.
func (c *Context) ResolveTargetUser() (tg.InputPeerClass, int64, error) {
	return c.Peer().ResolveTargetUser()
}

// GetFullUser fetches detailed user information.
func (c *Context) GetFullUser(user tg.InputUserClass) (*tg.UsersUserFull, error) {
	return c.Peer().GetFullUser(user)
}

// ResolveUsername resolves a public username to user/chat entities.
func (c *Context) ResolveUsername(username string) (*tg.ContactsResolvedPeer, error) {
	return c.Peer().ResolveUsername(username)
}

// GetFullChat fetches detailed chat/channel information for the current chat.
func (c *Context) GetFullChat() (*tg.MessagesChatFull, error) {
	return c.Peer().GetFullChat()
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
		var width, height int
		for _, s := range photo.Sizes {
			switch sz := s.(type) {
			case *tg.PhotoSize:
				largestSize = sz.Type
				largestBytes = int64(sz.Size)
				width = sz.W
				height = sz.H
			case *tg.PhotoSizeProgressive:
				largestSize = sz.Type
				if len(sz.Sizes) > 0 {
					largestBytes = int64(sz.Sizes[len(sz.Sizes)-1])
				}
				width = sz.W
				height = sz.H
			}
		}

		return &MediaInfo{
			Type:     "photo",
			FileName: fmt.Sprintf("photo_%d.jpg", photo.ID),
			MimeType: "image/jpeg",
			Size:     largestBytes,
			Width:    width,
			Height:   height,
			Location: photo.AsInputPhotoFileLocation(largestSize),
		}

	case *tg.MessageMediaDocument:
		doc, ok := m.Document.(*tg.Document)
		if !ok {
			return nil
		}

		mediaType := "document"
		fileName := fmt.Sprintf("document_%d", doc.ID)
		var width, height, duration int

		for _, attr := range doc.Attributes {
			switch a := attr.(type) {
			case *tg.DocumentAttributeFilename:
				base := filepath.Base(filepath.Clean(a.FileName))
				if base != "." && base != ".." && base != "/" && base != "" {
					fileName = base
				}
			case *tg.DocumentAttributeVideo:
				mediaType = "video"
				width = a.W
				height = a.H
				duration = int(a.Duration)
			case *tg.DocumentAttributeAudio:
				if a.Voice {
					mediaType = "voice"
				} else {
					mediaType = "audio"
				}
				duration = a.Duration
			case *tg.DocumentAttributeImageSize:
				width = a.W
				height = a.H
			case *tg.DocumentAttributeSticker:
				mediaType = "sticker"
			}
		}

		return &MediaInfo{
			Type:     mediaType,
			FileName: fileName,
			MimeType: doc.MimeType,
			Size:     doc.Size,
			Width:    width,
			Height:   height,
			Duration: duration,
			Location: doc.AsInputDocumentFileLocation(""),
		}
	}

	return nil
}

// IsPrivate returns true if the chat is a 1-on-1 private chat.
func (c *Context) IsPrivate() bool {
	return c.Chat != nil && c.Chat.Type == "private"
}

// IsGroup returns true if the chat is a group, supergroup, or channel.
// Channels are included because Telegram supergroups are represented as PeerChannel
// and may appear as "channel" type when entity metadata is absent from the cache.
func (c *Context) IsGroup() bool {
	if c.Chat == nil {
		return false
	}
	t := c.Chat.Type
	return t == "group" || t == "supergroup" || t == "channel"
}

// IsChannel returns true if the chat is a broadcast channel.
func (c *Context) IsChannel() bool {
	return c.Chat != nil && c.Chat.Type == "channel"
}

// GetPrincipal returns the dynamically resolved authorization identity of the caller.
func (c *Context) GetPrincipal() *Principal {
	if c == nil {
		return nil
	}
	if c.Principal != nil && (c.Sender == nil || c.Principal.UserID == c.Sender.ID) {
		return c.Principal
	}
	if c.Perms != nil && c.Sender != nil {
		p, _ := c.Perms.Resolve(c.Ctx, c.Sender.ID)
		c.Principal = p
		return p
	}
	return nil
}

// IsOwner returns true if the sender is the Owner.
func (c *Context) IsOwner() bool {
	if p := c.GetPrincipal(); p != nil {
		return p.IsOwner
	}
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsOwner(c.Sender.ID)
}

// IsSudo returns true if the sender has Sudo or Owner privileges.
func (c *Context) IsSudo() bool {
	if p := c.GetPrincipal(); p != nil {
		return p.IsSudo
	}
	if c.Sender == nil || c.Perms == nil {
		return false
	}
	return c.Perms.IsSudo(c.Sender.ID)
}

// UpdateProfile updates the account's first name, last name, and/or bio.
func (c *Context) UpdateProfile(firstName, lastName, about *string) error {
	if c == nil || c.Svc == nil {
		return errors.New("telegram service not initialized")
	}
	return c.Svc.UpdateProfile(c.Ctx, firstName, lastName, about)
}

// BlockUser blocks the specified user.
func (c *Context) BlockUser(peer tg.InputPeerClass) error {
	return c.Peer().BlockUser(peer)
}

// UnblockUser unblocks the specified user.
func (c *Context) UnblockUser(peer tg.InputPeerClass) error {
	return c.Peer().UnblockUser(peer)
}
