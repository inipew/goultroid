package interaction_test

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"go.uber.org/zap"
)

type mockTelegramAPI struct {
	answerReq       *tg.MessagesSetBotCallbackAnswerRequest
	editReq         *tg.MessagesEditMessageRequest
	deleteMsgsReq   *tg.MessagesDeleteMessagesRequest
	deleteChanReq   *tg.ChannelsDeleteMessagesRequest
	getMsgsIDs      []tg.InputMessageClass
	getChanReq      *tg.ChannelsGetMessagesRequest
	sendMsgReq      *tg.MessagesSendMessageRequest
	editInlineReq   *tg.MessagesEditInlineBotMessageRequest

	// Injected errors
	deleteErr     error
	editErr       error
	answerErr     error
	editInlineErr error
}

func (m *mockTelegramAPI) MessagesSetBotCallbackAnswer(ctx context.Context, req *tg.MessagesSetBotCallbackAnswerRequest) (bool, error) {
	m.answerReq = req
	return true, m.answerErr
}

func (m *mockTelegramAPI) MessagesEditMessage(ctx context.Context, req *tg.MessagesEditMessageRequest) (tg.UpdatesClass, error) {
	m.editReq = req
	return &tg.Updates{}, m.editErr
}

func (m *mockTelegramAPI) MessagesDeleteMessages(ctx context.Context, req *tg.MessagesDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	m.deleteMsgsReq = req
	return &tg.MessagesAffectedMessages{Pts: 1, PtsCount: 1}, m.deleteErr
}

func (m *mockTelegramAPI) ChannelsDeleteMessages(ctx context.Context, req *tg.ChannelsDeleteMessagesRequest) (*tg.MessagesAffectedMessages, error) {
	m.deleteChanReq = req
	return &tg.MessagesAffectedMessages{Pts: 1, PtsCount: 1}, m.deleteErr
}

func (m *mockTelegramAPI) MessagesGetMessages(ctx context.Context, id []tg.InputMessageClass) (tg.MessagesMessagesClass, error) {
	m.getMsgsIDs = id
	return &tg.MessagesMessages{
		Messages: []tg.MessageClass{
			&tg.Message{ID: 10, Message: "test user msg"},
		},
	}, nil
}

func (m *mockTelegramAPI) ChannelsGetMessages(ctx context.Context, req *tg.ChannelsGetMessagesRequest) (tg.MessagesMessagesClass, error) {
	m.getChanReq = req
	return &tg.MessagesChannelMessages{
		Messages: []tg.MessageClass{
			&tg.Message{ID: 20, Message: "test channel msg"},
		},
	}, nil
}

func (m *mockTelegramAPI) MessagesSendMessage(ctx context.Context, req *tg.MessagesSendMessageRequest) (tg.UpdatesClass, error) {
	m.sendMsgReq = req
	return &tg.UpdateShortSentMessage{ID: 100}, nil
}

func (m *mockTelegramAPI) MessagesEditInlineBotMessage(ctx context.Context, req *tg.MessagesEditInlineBotMessageRequest) (bool, error) {
	m.editInlineReq = req
	return true, m.editInlineErr
}

func TestClientInteraction_Answer(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	if err := ci.Answer(ctx, 12345, "Hello alert", true); err != nil {
		t.Fatalf("unexpected error on Answer: %v", err)
	}
	if mockAPI.answerReq == nil || mockAPI.answerReq.QueryID != 12345 || mockAPI.answerReq.Message != "Hello alert" || !mockAPI.answerReq.Alert {
		t.Fatalf("unexpected answer request: %+v", mockAPI.answerReq)
	}
}

func TestClientInteraction_Delete_UserChat(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 55, 100, 200)
	if err := ci.Delete(ctx, target); err != nil {
		t.Fatalf("unexpected error on Delete: %v", err)
	}
	if mockAPI.deleteMsgsReq == nil || len(mockAPI.deleteMsgsReq.ID) != 1 || mockAPI.deleteMsgsReq.ID[0] != 55 || !mockAPI.deleteMsgsReq.Revoke {
		t.Fatalf("unexpected delete request for user message: %+v", mockAPI.deleteMsgsReq)
	}
}

func TestClientInteraction_Delete_Channel(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	target := interaction.NewMessageTarget(&tg.InputPeerChannel{ChannelID: 777, AccessHash: 888}, 99, 777, 200)
	if err := ci.Delete(ctx, target); err != nil {
		t.Fatalf("unexpected error on Delete channel: %v", err)
	}
	if mockAPI.deleteChanReq == nil || mockAPI.deleteChanReq.Channel == nil {
		t.Fatalf("unexpected channel delete request: %+v", mockAPI.deleteChanReq)
	}
	inpChan, ok := mockAPI.deleteChanReq.Channel.(*tg.InputChannel)
	if !ok || inpChan.ChannelID != 777 {
		t.Fatalf("unexpected input channel: %+v", mockAPI.deleteChanReq.Channel)
	}
}

func TestClientInteraction_Delete_Idempotent(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		deleteErr: errors.New("rpc error code 400: MESSAGE_ID_INVALID"),
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 55, 100, 200)
	// Even though deleteErr returns MESSAGE_ID_INVALID, Delete must return nil (idempotent!)
	if err := ci.Delete(ctx, target); err != nil {
		t.Fatalf("expected nil on MESSAGE_ID_INVALID (idempotent deletion), got error: %v", err)
	}
}

func TestClientInteraction_GetMessage(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	userTarget := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 10, 100, 200)
	msg, err := ci.GetMessage(ctx, userTarget)
	if err != nil || msg == nil || msg.ID != 10 {
		t.Fatalf("unexpected result getting user message: msg=%+v err=%v", msg, err)
	}

	chanTarget := interaction.NewMessageTarget(&tg.InputPeerChannel{ChannelID: 777, AccessHash: 888}, 20, 777, 200)
	msgChan, err := ci.GetMessage(ctx, chanTarget)
	if err != nil || msgChan == nil || msgChan.ID != 20 {
		t.Fatalf("unexpected result getting channel message: msg=%+v err=%v", msgChan, err)
	}
}

func TestClientInteraction_Edit(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 55, 100, 200)
	if err := ci.Edit(ctx, target, "<b>Updated text</b>", nil); err != nil {
		t.Fatalf("unexpected error on Edit: %v", err)
	}
	if mockAPI.editReq == nil || mockAPI.editReq.ID != 55 || mockAPI.editReq.Message != "Updated text" {
		t.Fatalf("unexpected edit request: %+v", mockAPI.editReq)
	}
}

func TestInlineInteraction(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	inlineInter := ci.AsInline()
	ctx := context.Background()

	inlineID := &tg.InputBotInlineMessageID{DCID: 1, ID: 12345}
	target := interaction.NewInlineTarget(999, inlineID, 888)

	if err := inlineInter.Edit(ctx, target, "Updated inline", nil); err != nil {
		t.Fatalf("unexpected error editing inline message: %v", err)
	}
	if mockAPI.editInlineReq == nil || mockAPI.editInlineReq.ID != inlineID || mockAPI.editInlineReq.Message != "Updated inline" {
		t.Fatalf("unexpected inline edit request: %+v", mockAPI.editInlineReq)
	}
}

type fakePeerInvalidator struct {
	invalidatedPeer tg.InputPeerClass
}

func (f *fakePeerInvalidator) InvalidatePeer(peer tg.InputPeerClass) {
	f.invalidatedPeer = peer
}

func TestClientInteraction_StalePeerInvalidator(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		editErr: errors.New("rpc error code 400: ACCESS_HASH_INVALID"),
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	invalidator := &fakePeerInvalidator{}
	ci.SetPeerInvalidator(invalidator)

	ctx := context.Background()
	userPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 999}
	target := interaction.NewMessageTarget(userPeer, 55, 100, 200)

	err := ci.Edit(ctx, target, "Hello", nil)
	if err == nil {
		t.Fatalf("expected error from ACCESS_HASH_INVALID")
	}
	if !errors.Is(err, interaction.ErrAccessHashStale) {
		t.Fatalf("expected ErrAccessHashStale, got %v", err)
	}
	if invalidator.invalidatedPeer != userPeer {
		t.Fatalf("expected invalidator to be called with userPeer, got %+v", invalidator.invalidatedPeer)
	}
}
