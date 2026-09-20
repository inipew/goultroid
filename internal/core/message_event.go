package core

import (
	"strconv"
	"strings"
	"time"
)

// MessageMention is a canonical mention extracted from Telegram entities.
// Exactly one of UserID or Username is normally populated.
type MessageMention struct {
	UserID   int64
	Username string
}

// MessageMediaSummary is the transport-neutral media metadata exposed to
// canonical message hooks. Download locations and raw MTProto objects are
// intentionally excluded.
type MessageMediaSummary struct {
	Type     string
	FileName string
	MIMEType string
	Size     int64
	Width    int
	Height   int
	Duration int
	WebURL   string
}

func SummarizeMedia(info *MediaInfo) *MessageMediaSummary {
	if info == nil {
		return nil
	}
	return &MessageMediaSummary{
		Type: info.Type, FileName: info.FileName, MIMEType: info.MimeType,
		Size: info.Size, Width: info.Width, Height: info.Height,
		Duration: info.Duration, WebURL: info.WebURL,
	}
}

// MessageEnvelope is the canonical, lightweight message view exposed to
// message-hook plugins. It intentionally contains no raw MTProto update or
// entity containers. Treat instances as immutable after dispatcher creation.
type MessageEnvelope struct {
	ID         int
	ChatID     int64
	Peer       PeerRef
	Chat       Chat
	Sender     User
	SenderPeer PeerRef
	Self       User
	Text       string
	Date       time.Time
	ReplyToID  int
	TopicID    int
	GroupedID  int64

	Outgoing         bool
	IsCommand        bool
	CommandName      string
	SenderVerified   bool
	SenderSelf       bool
	Mentioned        bool
	ReplyIsTopicRoot bool
	Mentions         []MessageMention
	Media            *MessageMediaSummary
}

// IsPrivate reports whether the message belongs to a private user dialog.
func (m *MessageEnvelope) IsPrivate() bool {
	return m != nil && m.Chat.Type == "private"
}

// IsGroup reports whether the message belongs to a basic group/supergroup.
func (m *MessageEnvelope) IsGroup() bool {
	if m == nil {
		return false
	}
	return m.Chat.Type == "group" || m.Chat.Type == "supergroup"
}

// IsChannel reports whether the message belongs to a broadcast channel.
func (m *MessageEnvelope) IsChannel() bool {
	return m != nil && m.Chat.Type == "channel"
}

// MentionsUser reports whether an explicit text-mention points at userID.
func (m *MessageEnvelope) MentionsUser(userID int64) bool {
	if m == nil || userID == 0 {
		return false
	}
	for _, mention := range m.Mentions {
		if mention.UserID == userID {
			return true
		}
	}
	return false
}

// MentionsUsername reports whether an @username entity matches username.
func (m *MessageEnvelope) MentionsUsername(username string) bool {
	if m == nil {
		return false
	}
	username = strings.TrimPrefix(strings.TrimSpace(username), "@")
	if username == "" {
		return false
	}
	for _, mention := range m.Mentions {
		if mention.Username != "" && strings.EqualFold(strings.TrimPrefix(mention.Username, "@"), username) {
			return true
		}
	}
	return false
}

// SenderName returns a stable human-readable sender label without requiring
// Telegram entity access in plugins.
func (m *MessageEnvelope) SenderName() string {
	if m == nil {
		return "Unknown User"
	}
	name := strings.TrimSpace(m.Sender.FirstName + " " + m.Sender.LastName)
	if name != "" {
		return name
	}
	if m.Sender.Username != "" {
		return m.Sender.Username
	}
	if m.Sender.ID != 0 {
		return "User " + strconv.FormatInt(m.Sender.ID, 10)
	}
	return "Unknown User"
}

func (m *MessageEnvelope) HasMedia() bool {
	return m != nil && m.Media != nil
}
