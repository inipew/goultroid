package core

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

type p7jTrackingResolver struct {
	lastUserRef string
}

func (r *p7jTrackingResolver) Resolve(context.Context, string) (tg.InputPeerClass, error) {
	return nil, nil
}

func (r *p7jTrackingResolver) ResolveUser(_ context.Context, ref string) (tg.InputPeerClass, int64, error) {
	r.lastUserRef = ref
	var id int64
	switch ref {
	case "555":
		id = 555
	case "@explicit":
		id = 777
	case "payload":
		id = 888
	default:
		id = 999
	}
	return &tg.InputPeerUser{UserID: id, AccessHash: id + 1000}, id, nil
}

func (r *p7jTrackingResolver) ResolveChat(context.Context, string) (tg.InputPeerClass, error) {
	return nil, nil
}

func TestP7JResolveTargetReplyPayloadDoesNotBecomeUsername(t *testing.T) {
	resolver := &p7jTrackingResolver{}
	svc := &p7jReplyServicer{message: &tg.Message{
		ID:     800,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 555},
	}}
	ctx := &Context{
		Ctx:      context.Background(),
		Svc:      svc,
		Resolver: resolver,
		PeerID:   &tg.InputPeerChat{ChatID: 77},
		Message:  &Message{ID: 900, ReplyToID: 800},
		Args:     []string{"payload"},
	}
	_, id, err := ctx.ResolveTargetUser()
	if err != nil {
		t.Fatalf("resolve reply target: %v", err)
	}
	if id != 555 || resolver.lastUserRef != "555" {
		t.Fatalf("resolved id/ref=%d/%q want 555/555", id, resolver.lastUserRef)
	}
}

func TestP7JResolveTargetExplicitMentionOverridesReply(t *testing.T) {
	resolver := &p7jTrackingResolver{}
	svc := &p7jReplyServicer{message: &tg.Message{
		ID:     800,
		PeerID: &tg.PeerChat{ChatID: 77},
		FromID: &tg.PeerUser{UserID: 555},
	}}
	ctx := &Context{
		Ctx:      context.Background(),
		Svc:      svc,
		Resolver: resolver,
		PeerID:   &tg.InputPeerChat{ChatID: 77},
		Message:  &Message{ID: 900, ReplyToID: 800},
		Args:     []string{"@explicit"},
	}
	_, id, err := ctx.ResolveTargetUser()
	if err != nil {
		t.Fatalf("resolve explicit target: %v", err)
	}
	if id != 777 || resolver.lastUserRef != "@explicit" || svc.calls != 0 {
		t.Fatalf("explicit target id/ref/calls=%d/%q/%d", id, resolver.lastUserRef, svc.calls)
	}
}

type p7jContextualSendServicer struct {
	MockTelegramServicer
	lastSend MessageSendContext
}

func (s *p7jContextualSendServicer) SendMessageContext(
	_ context.Context,
	_ tg.InputPeerClass,
	text string,
	_ tg.ReplyMarkupClass,
	send MessageSendContext,
) (*tg.Message, error) {
	s.lastSend = send
	return &tg.Message{ID: 901, Message: text}, nil
}

func (s *p7jContextualSendServicer) SendMediaContext(
	context.Context,
	tg.InputPeerClass,
	string,
	string,
	string,
	MessageSendContext,
) (*tg.Message, error) {
	return nil, nil
}

func TestP7JCoreReplyCarriesSourceMessageAndTopic(t *testing.T) {
	svc := &p7jContextualSendServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100},
	}
	if _, err := ctx.Messages().Reply("same topic"); err != nil {
		t.Fatalf("reply: %v", err)
	}
	if svc.lastSend.ReplyToID != 900 || svc.lastSend.TopicID != 100 {
		t.Fatalf("send context=%+v want reply=900 topic=100", svc.lastSend)
	}
}


type p7jPlainServicer struct {
	MockTelegramServicer
	sendCalls  int
	mediaCalls int
}

func (s *p7jPlainServicer) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	s.sendCalls++
	return &tg.Message{ID: 1}, nil
}

func (s *p7jPlainServicer) SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error) {
	s.mediaCalls++
	return &tg.Message{ID: 1}, nil
}

func TestP7JAddressedToSelfPrefersMentionWithoutReplyRPC(t *testing.T) {
	ctx := &Context{
		Message: &Message{MentionedSelf: true, ReplyToID: 77},
		Self:    &User{ID: 999, IsBot: true},
	}
	ok, err := ctx.AddressedToSelf()
	if err != nil || !ok {
		t.Fatalf("AddressedToSelf=%v err=%v", ok, err)
	}
}

func TestP7JTopicReplyFailsClosedWithoutContextualTransport(t *testing.T) {
	svc := &p7jPlainServicer{}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100},
	}
	if err := ctx.Reply("topic response"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("topic Reply error=%v want ErrUnavailable", err)
	}
	if svc.sendCalls != 0 {
		t.Fatalf("topic reply escaped contextual transport with %d plain sends", svc.sendCalls)
	}
}

func TestP7JTopicMediaFailsClosedWithoutContextualTransport(t *testing.T) {
	svc := &p7jPlainServicer{}
	dir := t.TempDir()
	path := filepath.Join(dir, "payload.bin")
	if err := os.WriteFile(path, []byte("p7j"), 0o600); err != nil {
		t.Fatal(err)
	}
	ctx := &Context{
		Ctx:     context.Background(),
		Svc:     svc,
		PeerID:  &tg.InputPeerChannel{ChannelID: 77, AccessHash: 700},
		Message: &Message{ID: 900, TopicID: 100},
	}
	if _, err := ctx.Media().SendMedia("file", path, "topic media"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("topic SendMedia error=%v want ErrUnavailable", err)
	}
	if svc.mediaCalls != 0 {
		t.Fatalf("topic media escaped contextual transport with %d plain sends", svc.mediaCalls)
	}
}
