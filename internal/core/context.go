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

// TelegramServicer defines message, media, and chat actions implementing the
// high-level Telegram boundary used by plugins and services.
type TelegramServicer interface {
	SendMessage(ctx context.Context, peer tg.InputPeerClass, text string) (*tg.Message, error)
	SendMessageWithMarkup(ctx context.Context, peer tg.InputPeerClass, text string, markup tg.ReplyMarkupClass) (*tg.Message, error)
	EditMessage(ctx context.Context, peer tg.InputPeerClass, msgID int, text string) error
	EditMessageMarkup(ctx context.Context, peer tg.InputPeerClass, msgID int, text string, markup tg.ReplyMarkupClass) error
	DeleteMessage(ctx context.Context, peer tg.InputPeerClass, msgIDs []int) error
	AnswerCallbackQuery(ctx context.Context, queryID int64, text string, alert bool) error
	AnswerInlineQuery(ctx context.Context, queryID int64, results []tg.InputBotInlineResultClass, nextOffset string, cacheTime int) error
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

type MediaInfo struct {
	Type     string
	FileName string
	MimeType string
	Size     int64
	Width    int
	Height   int
	Duration int
	Location tg.InputFileLocationClass
}

type Message struct {
	ID         int
	SenderID   int64
	TopicID    int
	Text       string
	Date       time.Time
	ReplyToID  int
	MediaType  string
	Media      *MediaInfo
	IsOutgoing bool
	GroupedID  int64
	Entities   []tg.MessageEntityClass
}

func (m *Message) HasMedia() bool { return m != nil && m.Media != nil && m.Media.Location != nil }
func (m *Message) IsAlbum() bool  { return m != nil && m.GroupedID != 0 }

func (m *Message) Mentions() []string {
	if m == nil {
		return nil
	}
	var mentions []string
	for _, ent := range m.Entities {
		switch e := ent.(type) {
		case *tg.MessageEntityMentionName:
			mentions = append(mentions, fmt.Sprintf("user:%d", e.UserID))
		}
	}
	for _, word := range strings.Fields(m.Text) {
		if strings.HasPrefix(word, "@") && len(word) > 1 {
			mentions = append(mentions, word)
		}
	}
	return mentions
}

func (m *Message) URLs() []string {
	if m == nil {
		return nil
	}
	runes := []rune(m.Text)
	var urls []string
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

// Chat represents the chat in which an event occurred. AccessHash is retained
// whenever Telegram supplies one so downstream operations can construct a
// valid InputPeer without an unsafe hash-less lookup.
type Chat struct {
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Username   string `json:"username"`
	Type       string `json:"type"`
	AccessHash int64  `json:"access_hash,omitempty"`
}

type User struct {
	ID        int64
	FirstName string
	LastName  string
	Username  string
	IsBot     bool
}

type Localizer interface {
	T(key string, args ...any) string
}

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

	LastResponseID int

	Svc       TelegramServicer
	PeerID    tg.InputPeerClass
	Resolver  PeerResolver
	Localizer Localizer
}

var _ = errors.Is
var _ = filepath.Join
var _ = strconv.Itoa
