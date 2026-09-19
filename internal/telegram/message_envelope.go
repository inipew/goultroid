package telegram

import (
	"strings"
	"time"
	"unicode/utf16"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// NormalizeMessageEnvelope builds the canonical lightweight message envelope used by dispatcher hooks.
func NormalizeMessageEnvelope(e tg.Entities, msg *tg.Message, isCommand bool, commandName string, selfID int64) *core.MessageEnvelope {
	if msg == nil {
		return nil
	}

	envelope := &core.MessageEnvelope{
		ID:          msg.ID,
		Text:        msg.Message,
		Date:        time.Unix(int64(msg.Date), 0).UTC(),
		Outgoing:    msg.Out,
		IsCommand:   isCommand,
		CommandName: commandName,
		GroupedID:   msg.GroupedID,
	}

	switch peer := msg.PeerID.(type) {
	case *tg.PeerUser:
		envelope.ChatID = peer.UserID
		envelope.Chat = core.Chat{ID: peer.UserID, Type: "private"}
		envelope.Peer = core.PeerRef{Kind: core.PeerKindUser, ID: peer.UserID}
		if user := e.Users[peer.UserID]; user != nil {
			envelope.Peer.AccessHash = user.AccessHash
			envelope.Chat.Title = strings.TrimSpace(user.FirstName + " " + user.LastName)
			envelope.Chat.Username = user.Username
			envelope.Chat.AccessHash = user.AccessHash
		}
	case *tg.PeerChat:
		envelope.ChatID = peer.ChatID
		envelope.Chat = core.Chat{ID: peer.ChatID, Type: "group"}
		envelope.Peer = core.PeerRef{Kind: core.PeerKindChat, ID: peer.ChatID}
		if chat := e.Chats[peer.ChatID]; chat != nil {
			envelope.Chat.Title = chat.Title
		}
	case *tg.PeerChannel:
		envelope.ChatID = peer.ChannelID
		envelope.Chat = core.Chat{ID: peer.ChannelID, Type: "channel"}
		envelope.Peer = core.PeerRef{Kind: core.PeerKindChannel, ID: peer.ChannelID}
		if channel := e.Channels[peer.ChannelID]; channel != nil {
			envelope.Peer.AccessHash = channel.AccessHash
			envelope.Chat.Title = channel.Title
			envelope.Chat.Username = channel.Username
			envelope.Chat.AccessHash = channel.AccessHash
			if channel.Megagroup {
				envelope.Chat.Type = "supergroup"
			}
		}
	}

	if msg.ReplyTo != nil {
		if header, ok := msg.ReplyTo.(*tg.MessageReplyHeader); ok && header != nil {
			envelope.ReplyToID = header.ReplyToMsgID
			if header.ForumTopic || header.ReplyToTopID != 0 {
				if header.ReplyToTopID != 0 {
					envelope.TopicID = header.ReplyToTopID
				} else {
					envelope.TopicID = header.ReplyToMsgID
				}
			}
			envelope.ReplyIsTopicRoot = header.ForumTopic &&
				(header.ReplyToTopID == 0 || header.ReplyToMsgID == header.ReplyToTopID)
		}
	}

	if selfID != 0 {
		envelope.Self.ID = selfID
		if self := e.Users[selfID]; self != nil {
			envelope.Self = normalizeEnvelopeUser(self)
		}
	}

	senderID := int64(0)
	if msg.Out {
		senderID = selfID
		envelope.SenderPeer = core.PeerRef{Kind: core.PeerKindUser, ID: selfID}
		if self := e.Users[selfID]; self != nil {
			envelope.SenderPeer.AccessHash = self.AccessHash
		}
	} else {
		switch from := msg.FromID.(type) {
		case *tg.PeerUser:
			if from != nil {
				senderID = from.UserID
				envelope.SenderPeer = core.PeerRef{Kind: core.PeerKindUser, ID: from.UserID}
				if sender := e.Users[from.UserID]; sender != nil {
					envelope.SenderPeer.AccessHash = sender.AccessHash
				}
			}
		case *tg.PeerChat:
			if from != nil {
				envelope.SenderPeer = core.PeerRef{Kind: core.PeerKindChat, ID: from.ChatID}
			}
		case *tg.PeerChannel:
			if from != nil {
				envelope.SenderPeer = core.PeerRef{Kind: core.PeerKindChannel, ID: from.ChannelID}
				if sender := e.Channels[from.ChannelID]; sender != nil {
					envelope.SenderPeer.AccessHash = sender.AccessHash
				}
			}
		}
		if senderID == 0 {
			if peer, ok := msg.PeerID.(*tg.PeerUser); ok && peer != nil {
				senderID = peer.UserID
				envelope.SenderPeer = core.PeerRef{Kind: core.PeerKindUser, ID: peer.UserID}
				if sender := e.Users[peer.UserID]; sender != nil {
					envelope.SenderPeer.AccessHash = sender.AccessHash
				}
			}
		}
	}
	if senderID != 0 {
		envelope.Sender.ID = senderID
		if sender := e.Users[senderID]; sender != nil {
			envelope.Sender = normalizeEnvelopeUser(sender)
			envelope.SenderVerified = sender.Verified
			envelope.SenderSelf = sender.Self
		}
	}

	envelope.Mentioned = msg.Mentioned
	if len(msg.Entities) > 0 {
		envelope.Mentions = make([]core.MessageMention, 0, len(msg.Entities))
		for _, entity := range msg.Entities {
			switch mention := entity.(type) {
			case *tg.MessageEntityMentionName:
				if mention.UserID != 0 {
					envelope.Mentions = append(envelope.Mentions, core.MessageMention{UserID: mention.UserID})
				}
			case *tg.MessageEntityMention:
				if text, ok := telegramUTF16Slice(msg.Message, mention.Offset, mention.Length); ok {
					username := strings.TrimPrefix(strings.TrimSpace(text), "@")
					if username != "" {
						envelope.Mentions = append(envelope.Mentions, core.MessageMention{Username: username})
					}
				}
			}
		}
		if len(envelope.Mentions) == 0 {
			envelope.Mentions = nil
		}
	}

	return envelope
}

func normalizeEnvelopeUser(user *tg.User) core.User {
	if user == nil {
		return core.User{}
	}
	return core.User{
		ID:         user.ID,
		FirstName:  user.FirstName,
		LastName:   user.LastName,
		Username:   user.Username,
		IsBot:      user.Bot,
	}
}

func telegramUTF16Slice(text string, offset, length int) (string, bool) {
	if offset < 0 || length < 0 {
		return "", false
	}
	units := utf16.Encode([]rune(text))
	if offset > len(units) || length > len(units)-offset {
		return "", false
	}
	end := offset + length
	if utf16SplitSurrogate(units, offset) || utf16SplitSurrogate(units, end) {
		return "", false
	}
	return string(utf16.Decode(units[offset:end])), true
}

func utf16SplitSurrogate(units []uint16, index int) bool {
	if index <= 0 || index >= len(units) {
		return false
	}
	prev := units[index-1]
	next := units[index]
	return prev >= 0xD800 && prev <= 0xDBFF && next >= 0xDC00 && next <= 0xDFFF
}
