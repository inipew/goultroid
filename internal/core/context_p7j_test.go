package core

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
)

type p7jReplyServicer struct {
	MockTelegramServicer
	message *tg.Message
	calls   int
}

func (s *p7jReplyServicer) GetMessage(context.Context, tg.InputPeerClass, int) (*tg.Message, error) {
	s.calls++
	return s.message, nil
}

func TestP7JGetReplyRejectsLinkedPeerBeforeRPC(t *testing.T) {
	svc := &p7jReplyServicer{}
	ctx := &Context{
		Ctx:    context.Background(),
		Svc:    svc,
		PeerID: &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{
			ID:        900,
			ReplyToID: 800,
			ReplyPeer: PeerRef{Kind: PeerKindChannel, ID: 88},
		},
	}
	if _, err := ctx.GetReply(); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("linked reply error=%v want ErrInvalidArgs", err)
	}
	if svc.calls != 0 {
		t.Fatalf("linked reply made %d Telegram lookups", svc.calls)
	}
}

func TestP7JGetReplyRejectsCrossTopicTarget(t *testing.T) {
	svc := &p7jReplyServicer{message: &tg.Message{
		ID:     800,
		PeerID: &tg.PeerChannel{ChannelID: 77},
		ReplyTo: &tg.MessageReplyHeader{
			ForumTopic:   true,
			ReplyToMsgID: 201,
			ReplyToTopID: 200,
		},
	}}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100, ReplyToID: 800},
	}
	if _, err := ctx.GetReply(); !errors.Is(err, ErrInvalidArgs) {
		t.Fatalf("cross-topic reply error=%v want ErrInvalidArgs", err)
	}
}

func TestP7JGetReplyAcceptsTopicRootAndNormalizesTopic(t *testing.T) {
	svc := &p7jReplyServicer{message: &tg.Message{ID: 100, PeerID: &tg.PeerChannel{ChannelID: 77}}}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100, ReplyToID: 100},
	}
	reply, err := ctx.GetReply()
	if err != nil {
		t.Fatalf("topic-root reply: %v", err)
	}
	if reply == nil || reply.TopicID != 100 {
		t.Fatalf("topic-root reply=%+v want TopicID=100", reply)
	}
}

func TestP7JRepliedToSelfUsesCanonicalReplyFence(t *testing.T) {
	svc := &p7jReplyServicer{message: &tg.Message{
		ID:     800,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 999},
	}}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChat{ChatID: 77},
		Message: &Message{ID: 900, ReplyToID: 800},
		Self:    &User{ID: 999, IsBot: true},
	}
	ok, err := ctx.RepliedToSelf()
	if err != nil || !ok {
		t.Fatalf("RepliedToSelf=%v err=%v", ok, err)
	}
}
