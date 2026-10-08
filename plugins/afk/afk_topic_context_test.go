package afk

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
)

// contextualAFKMock simulates the optional contextual Telegram delivery
// capability while reusing the existing message service test double.
type contextualAFKMock struct {
	*mockService
	sends []core.MessageSendContext
	fail  error
}

func (s *contextualAFKMock) SendMessageContext(ctx context.Context, peer tg.InputPeerClass, text string, _ tg.ReplyMarkupClass, send core.MessageSendContext) (*tg.Message, error) {
	s.sends = append(s.sends, send)
	if s.fail != nil {
		return nil, s.fail
	}
	return s.mockService.SendMessage(ctx, peer, text)
}

func newActiveAFKTopicTestPlugin(svc TelegramService) *Plugin {
	p := NewWithService(nil, 1001, func() TelegramService { return svc })
	p.state.Store(&afkState{isAFK: true, reason: "busy", since: time.Now().Add(-time.Minute)})
	return p
}

func afkTopicTestEntities() tg.Entities {
	return tg.Entities{
		Users:    map[int64]*tg.User{2002: {ID: 2002, AccessHash: 111}},
		Channels: map[int64]*tg.Channel{500: {ID: 500, AccessHash: 222, Megagroup: true}},
	}
}

func TestAFKAutoReplyPreservesReplyAndTopic(t *testing.T) {
	cases := []struct {
		name         string
		message      *tg.Message
		reply        int
		topic        int
		ownerMessage bool
	}{
		{name: "private dialog", message: &tg.Message{ID: 201, PeerID: &tg.PeerUser{UserID: 2002}, FromID: &tg.PeerUser{UserID: 2002}, Message: "hello"}, reply: 201},
		{name: "group mention", message: &tg.Message{ID: 202, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: 2002}, Mentioned: true, Message: "@owner hello"}, reply: 202},
		{name: "forum mention", message: &tg.Message{ID: 203, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: 2002}, Mentioned: true, Message: "@owner hello", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 100, ReplyToTopID: 100}}, reply: 203, topic: 100},
		{name: "reply to owner inside forum", message: &tg.Message{ID: 204, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: 2002}, Message: "hello", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 150, ReplyToTopID: 100}}, reply: 204, topic: 100, ownerMessage: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			svc := &contextualAFKMock{mockService: &mockService{}}
			if tc.ownerMessage {
				svc.messages = map[int]*tg.Message{150: {ID: 150, Out: true}}
			}
			p := newActiveAFKTopicTestPlugin(svc)
			if err := handleMessageEvent(p, context.Background(), afkTopicTestEntities(), tc.message, false, ""); err != nil {
				t.Fatal(err)
			}
			if len(svc.sends) != 1 {
				t.Fatalf("contextual send calls=%d; want 1", len(svc.sends))
			}
			if got := svc.sends[0]; got.ReplyToID != tc.reply || got.TopicID != tc.topic {
				t.Fatalf("send context=%+v; want reply %d, topic %d", got, tc.reply, tc.topic)
			}
			if !strings.Contains(svc.sent, "currently AFK") {
				t.Fatalf("auto-reply missing: %q", svc.sent)
			}
		})
	}
}

func TestAFKAutoReplyNoForumTransportFailsClosedAndRollsBack(t *testing.T) {
	svc := &mockService{}
	p := newActiveAFKTopicTestPlugin(svc)
	msg := &tg.Message{ID: 250, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: 2002}, Mentioned: true, Message: "@owner", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 100, ReplyToTopID: 100}}
	for i := 0; i < 2; i++ {
		if err := handleMessageEvent(p, context.Background(), afkTopicTestEntities(), msg, false, ""); err != nil {
			t.Fatal(err)
		}
		if svc.sent != "" {
			t.Fatalf("forum reply escaped to unthreaded SendMessage: %q", svc.sent)
		}
		if p.isCooldownActive(500, 2002) {
			t.Fatal("failed delivery retained cooldown")
		}
	}
}

func TestAFKAutoReplyContextualErrorNoFallback(t *testing.T) {
	svc := &contextualAFKMock{mockService: &mockService{}, fail: errors.New("send rejected")}
	p := newActiveAFKTopicTestPlugin(svc)
	msg := &tg.Message{ID: 251, PeerID: &tg.PeerChannel{ChannelID: 500}, FromID: &tg.PeerUser{UserID: 2002}, Mentioned: true, Message: "@owner", ReplyTo: &tg.MessageReplyHeader{ForumTopic: true, ReplyToMsgID: 100, ReplyToTopID: 100}}
	if err := handleMessageEvent(p, context.Background(), afkTopicTestEntities(), msg, false, ""); err != nil {
		t.Fatal(err)
	}
	if len(svc.sends) != 1 || svc.sent != "" {
		t.Fatalf("contextual failure incorrectly fell back: calls=%d unthreaded=%q", len(svc.sends), svc.sent)
	}
	if p.isCooldownActive(500, 2002) {
		t.Fatal("failed contextual delivery retained cooldown")
	}
}

func TestAFKAutoReplyLegacyNonForumStillWorks(t *testing.T) {
	svc := &mockService{}
	p := newActiveAFKTopicTestPlugin(svc)
	msg := &tg.Message{ID: 260, PeerID: &tg.PeerUser{UserID: 2002}, FromID: &tg.PeerUser{UserID: 2002}, Message: "hello"}
	if err := handleMessageEvent(p, context.Background(), afkTopicTestEntities(), msg, false, ""); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(svc.sent, "currently AFK") {
		t.Fatalf("legacy private reply failed: %q", svc.sent)
	}
}

func TestAFKWelcomeRemainsStandaloneWithContextualService(t *testing.T) {
	svc := &contextualAFKMock{mockService: &mockService{}}
	p := newActiveAFKTopicTestPlugin(svc)
	peer := &tg.InputPeerUser{UserID: 2002, AccessHash: 111}
	if _, err := p.sendTemplate(context.Background(), svc, peer, afkWelcomeResponse, afkWelcomeTemplate, afkTemplateVars("", "1m")); err != nil {
		t.Fatal(err)
	}
	if len(svc.sends) != 0 || !strings.Contains(svc.sent, "Welcome back") {
		t.Fatalf("welcome must use existing standalone transport: contextual=%d message=%q", len(svc.sends), svc.sent)
	}
}
