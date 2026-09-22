package interaction_test

import (
	"context"
	"errors"
	"os"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

type mockTelegramAPI struct {
	answerReq     *tg.MessagesSetBotCallbackAnswerRequest
	editReq       *tg.MessagesEditMessageRequest
	deleteMsgsReq *tg.MessagesDeleteMessagesRequest
	deleteChanReq *tg.ChannelsDeleteMessagesRequest
	getMsgsIDs     []tg.InputMessageClass
	getMsgsResult  tg.MessagesMessagesClass
	getChanReq     *tg.ChannelsGetMessagesRequest
	sendMsgReq     *tg.MessagesSendMessageRequest
	sendMsgResult  tg.UpdatesClass
	sendMediaReq   *tg.MessagesSendMediaRequest
	sendMediaResult tg.UpdatesClass
	forwardMsgsReq *tg.MessagesForwardMessagesRequest
	forwardResult  tg.UpdatesClass
	editInlineReq  *tg.MessagesEditInlineBotMessageRequest

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
	if m.getMsgsResult != nil {
		return m.getMsgsResult, nil
	}
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
	if m.sendMsgResult != nil {
		return m.sendMsgResult, nil
	}
	return &tg.UpdateShortSentMessage{ID: 100}, nil
}

func (m *mockTelegramAPI) MessagesSendMedia(ctx context.Context, req *tg.MessagesSendMediaRequest) (tg.UpdatesClass, error) {
	m.sendMediaReq = req
	if m.sendMediaResult != nil {
		return m.sendMediaResult, nil
	}
	return &tg.UpdateShortSentMessage{ID: 102}, nil
}

func (m *mockTelegramAPI) MessagesForwardMessages(ctx context.Context, req *tg.MessagesForwardMessagesRequest) (tg.UpdatesClass, error) {
	m.forwardMsgsReq = req
	if m.forwardResult != nil {
		return m.forwardResult, nil
	}
	return &tg.UpdateShortSentMessage{ID: 101}, nil
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

type fakePeerReResolver struct {
	invalidatedPeer tg.InputPeerClass
	reResolveCount  int
	newPeer         tg.InputPeerClass
	reResolveErr    error
}

func (f *fakePeerReResolver) InvalidatePeer(peer tg.InputPeerClass) {
	f.invalidatedPeer = peer
}

func (f *fakePeerReResolver) ReResolve(ctx context.Context, inputPeer tg.InputPeerClass) (tg.InputPeerClass, error) {
	f.reResolveCount++
	if f.reResolveErr != nil {
		return nil, f.reResolveErr
	}
	if f.newPeer != nil {
		return f.newPeer, nil
	}
	return inputPeer, nil
}

func TestClientInteraction_StalePeerRecovery(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		editErr: errors.New("rpc error code 400: ACCESS_HASH_INVALID"),
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	userPeer := &tg.InputPeerUser{UserID: 12345, AccessHash: 999}
	reResolver := &fakePeerReResolver{
		newPeer: &tg.InputPeerUser{UserID: 12345, AccessHash: 1000},
	}
	ci.SetPeerReResolver(reResolver)

	ctx := context.Background()
	target := interaction.NewMessageTarget(userPeer, 55, 100, 200)

	// Since mockAPI still returns ACCESS_HASH_INVALID, retry should happen once and then fail
	err := ci.Edit(ctx, target, "Hello", nil)
	if err == nil {
		t.Fatalf("expected error from ACCESS_HASH_INVALID")
	}
	if !errors.Is(err, interaction.ErrAccessHashStale) {
		t.Fatalf("expected ErrAccessHashStale, got %v", err)
	}
	if reResolver.invalidatedPeer != userPeer {
		t.Fatalf("expected invalidator to be called with userPeer, got %+v", reResolver.invalidatedPeer)
	}
	if reResolver.reResolveCount != 1 {
		t.Fatalf("expected exactly 1 ReResolve attempt (MaxPeerRecoveryAttempts=1), got %d", reResolver.reResolveCount)
	}
}

func TestParseHTML_SanitizeEntities(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	target := interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 100}, 55, 100, 200)

	// HTML with nested <pre><code> which normally produces duplicate entities in gotd
	text := "<pre><code>hello world</code></pre>"
	if err := ci.Edit(ctx, target, text, nil); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if mockAPI.editReq == nil {
		t.Fatal("expected editReq to be sent")
	}

	// Verify that entities only contains MessageEntityPre and NOT duplicate MessageEntityCode
	ents := mockAPI.editReq.Entities
	if len(ents) != 1 {
		t.Fatalf("expected exactly 1 entity after sanitization, got %d: %+v", len(ents), ents)
	}
	if _, ok := ents[0].(*tg.MessageEntityPre); !ok {
		t.Fatalf("expected MessageEntityPre, got %T", ents[0])
	}
}

func TestClientInteraction_CopyTextMessageWithRandomIDSendsBotAuthoredCopy(t *testing.T) {
	entities := []tg.MessageEntityClass{
		&tg.MessageEntityBold{Offset: 0, Length: 5},
	}
	mockAPI := &mockTelegramAPI{
		getMsgsResult: &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{ID: 77, Message: "hello visitor", Entities: entities},
			},
		},
		sendMsgResult: &tg.UpdateShortSentMessage{ID: 303},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	sourcePeer := &tg.InputPeerUser{UserID: 7, AccessHash: 70}
	targetPeer := &tg.InputPeerUser{UserID: 42, AccessHash: 420}

	msg, err := ci.CopyTextMessageWithRandomID(
		context.Background(),
		interaction.NewMessageTarget(sourcePeer, 77, 7, 0),
		targetPeer,
		999,
	)
	if err != nil {
		t.Fatalf("CopyTextMessageWithRandomID() error = %v", err)
	}
	if msg == nil || msg.ID != 303 {
		t.Fatalf("copied message = %+v", msg)
	}
	if mockAPI.forwardMsgsReq != nil {
		t.Fatalf("owner reply leaked through forwardMessages: %+v", mockAPI.forwardMsgsReq)
	}
	req := mockAPI.sendMsgReq
	if req == nil || req.Peer != targetPeer || req.Message != "hello visitor" || req.RandomID != 999 {
		t.Fatalf("durable send request = %+v", req)
	}
	if len(req.Entities) != 1 {
		t.Fatalf("copied entities=%+v, want one entity", req.Entities)
	}
	if len(mockAPI.getMsgsIDs) != 1 {
		t.Fatalf("source lookup ids=%+v", mockAPI.getMsgsIDs)
	}
	if id, ok := mockAPI.getMsgsIDs[0].(*tg.InputMessageID); !ok || id.ID != 77 {
		t.Fatalf("source lookup=%+v", mockAPI.getMsgsIDs)
	}
}

func TestClientInteraction_CopyTextMessageRejectsMedia(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		getMsgsResult: &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{
					ID:      77,
					Message: "caption",
					Media:   &tg.MessageMediaPhoto{},
				},
			},
		},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	_, err := ci.CopyTextMessageWithRandomID(
		context.Background(),
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7, AccessHash: 70}, 77, 7, 0),
		&tg.InputPeerUser{UserID: 42, AccessHash: 420},
		999,
	)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("CopyTextMessageWithRandomID(media) error=%v, want %v", err, core.ErrUnsupported)
	}
	if mockAPI.sendMsgReq != nil || mockAPI.forwardMsgsReq != nil {
		t.Fatalf("media copy produced transport side effect: send=%+v forward=%+v", mockAPI.sendMsgReq, mockAPI.forwardMsgsReq)
	}
}

func TestClientInteraction_CopyMessageWithRandomIDCopiesPhotoReference(t *testing.T) {
	photoMedia := &tg.MessageMediaPhoto{}
	photoMedia.SetPhoto(&tg.Photo{ID: 901, AccessHash: 902, FileReference: []byte{1, 2, 3}})
	mockAPI := &mockTelegramAPI{
		getMsgsResult: &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{ID: 77, Message: "photo caption", Media: photoMedia},
			},
		},
		sendMediaResult: &tg.UpdateShortSentMessage{ID: 304},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	msg, err := ci.CopyMessageWithRandomID(
		context.Background(),
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7, AccessHash: 70}, 77, 7, 0),
		&tg.InputPeerUser{UserID: 42, AccessHash: 420},
		1001,
	)
	if err != nil {
		t.Fatalf("CopyMessageWithRandomID(photo) error=%v", err)
	}
	if msg == nil || msg.ID != 304 {
		t.Fatalf("photo copy message=%+v", msg)
	}
	if mockAPI.sendMsgReq != nil || mockAPI.forwardMsgsReq != nil {
		t.Fatalf("photo copy used wrong transport: send=%+v forward=%+v", mockAPI.sendMsgReq, mockAPI.forwardMsgsReq)
	}
	req := mockAPI.sendMediaReq
	if req == nil || req.RandomID != 1001 || req.Message != "photo caption" {
		t.Fatalf("photo sendMedia request=%+v", req)
	}
	input, ok := req.Media.(*tg.InputMediaPhoto)
	if !ok || input == nil {
		t.Fatalf("photo media type=%T", req.Media)
	}
	photo, ok := input.ID.(*tg.InputPhoto)
	if !ok || photo.ID != 901 || photo.AccessHash != 902 ||
		string(photo.FileReference) != string([]byte{1, 2, 3}) {
		t.Fatalf("photo input=%+v", input.ID)
	}
}

func TestClientInteraction_CopyMessageWithRandomIDCopiesDocumentReference(t *testing.T) {
	documentMedia := &tg.MessageMediaDocument{}
	documentMedia.SetDocument(&tg.Document{
		ID: 801, AccessHash: 802, FileReference: []byte{4, 5, 6},
		MimeType: "video/mp4",
	})
	mockAPI := &mockTelegramAPI{
		getMsgsResult: &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{ID: 78, Message: "video caption", Media: documentMedia},
			},
		},
		sendMediaResult: &tg.UpdateShortSentMessage{ID: 305},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	msg, err := ci.CopyMessageWithRandomID(
		context.Background(),
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7, AccessHash: 70}, 78, 7, 0),
		&tg.InputPeerUser{UserID: 42, AccessHash: 420},
		1002,
	)
	if err != nil {
		t.Fatalf("CopyMessageWithRandomID(document) error=%v", err)
	}
	if msg == nil || msg.ID != 305 {
		t.Fatalf("document copy message=%+v", msg)
	}
	req := mockAPI.sendMediaReq
	if req == nil || req.RandomID != 1002 || req.Message != "video caption" {
		t.Fatalf("document sendMedia request=%+v", req)
	}
	input, ok := req.Media.(*tg.InputMediaDocument)
	if !ok || input == nil {
		t.Fatalf("document media type=%T", req.Media)
	}
	document, ok := input.ID.(*tg.InputDocument)
	if !ok || document.ID != 801 || document.AccessHash != 802 ||
		string(document.FileReference) != string([]byte{4, 5, 6}) {
		t.Fatalf("document input=%+v", input.ID)
	}
	if mockAPI.forwardMsgsReq != nil {
		t.Fatalf("document copy leaked through forwardMessages: %+v", mockAPI.forwardMsgsReq)
	}
}

func TestClientInteraction_CopyMessageRejectsUnsupportedMediaWithoutSideEffect(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		getMsgsResult: &tg.MessagesMessages{
			Messages: []tg.MessageClass{
				&tg.Message{ID: 79, Media: &tg.MessageMediaUnsupported{}},
			},
		},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	_, err := ci.CopyMessageWithRandomID(
		context.Background(),
		interaction.NewMessageTarget(&tg.InputPeerUser{UserID: 7, AccessHash: 70}, 79, 7, 0),
		&tg.InputPeerUser{UserID: 42, AccessHash: 420},
		1003,
	)
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("CopyMessageWithRandomID(unsupported) error=%v, want %v", err, core.ErrUnsupported)
	}
	if mockAPI.sendMsgReq != nil || mockAPI.sendMediaReq != nil || mockAPI.forwardMsgsReq != nil {
		t.Fatalf("unsupported media produced side effect: send=%+v media=%+v forward=%+v",
			mockAPI.sendMsgReq, mockAPI.sendMediaReq, mockAPI.forwardMsgsReq)
	}
}

func TestClientInteraction_ForwardMessageWithRandomID(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	from := &tg.InputPeerUser{UserID: 42, AccessHash: 420}
	to := &tg.InputPeerUser{UserID: 7, AccessHash: 70}

	msg, err := ci.ForwardMessageWithRandomID(context.Background(), from, to, 11, 999)
	if err != nil {
		t.Fatalf("ForwardMessageWithRandomID() error = %v", err)
	}
	if msg == nil || msg.ID != 101 {
		t.Fatalf("forwarded message = %+v", msg)
	}
	req := mockAPI.forwardMsgsReq
	if req == nil || req.FromPeer != from || req.ToPeer != to ||
		len(req.ID) != 1 || req.ID[0] != 11 ||
		len(req.RandomID) != 1 || req.RandomID[0] != 999 {
		t.Fatalf("forward request = %+v", req)
	}
}

func TestClientInteraction_ForwardExtractsMessageFromUpdatesCombined(t *testing.T) {
	mockAPI := &mockTelegramAPI{
		forwardResult: &tg.UpdatesCombined{
			Updates: []tg.UpdateClass{
				&tg.UpdateNewMessage{Message: &tg.Message{ID: 202, Message: "forwarded"}},
			},
		},
	}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	msg, err := ci.ForwardMessageWithRandomID(
		context.Background(),
		&tg.InputPeerUser{UserID: 42, AccessHash: 420},
		&tg.InputPeerUser{UserID: 7, AccessHash: 70},
		11,
		999,
	)
	if err != nil {
		t.Fatalf("ForwardMessageWithRandomID() error = %v", err)
	}
	if msg == nil || msg.ID != 202 {
		t.Fatalf("combined forward message = %+v", msg)
	}
}

func TestClientInteraction_SendMedia(t *testing.T) {
	mockAPI := &mockTelegramAPI{}
	ci := interaction.NewClientInteraction(mockAPI, zap.NewNop())
	ctx := context.Background()

	// 1. Without sender/uploader configured -> core.ErrUnsupported
	_, err := ci.SendMedia(ctx, &tg.InputPeerUser{UserID: 100}, "photo", "nonexistent.png", "caption")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported when sender/uploader nil, got %v", err)
	}

	// 2. Mock uploader & sender via dummy struct
	tmpDir := t.TempDir()
	testFile := tmpDir + "/test.png"
	if err := os.WriteFile(testFile, []byte("fake png content"), 0600); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	// Nil peer with fake sender
	ci.SetMediaSender(nil, &fakeUploader{})
	// Still returns ErrUnsupported since sender is nil
	_, err = ci.SendMedia(ctx, nil, "photo", testFile, "")
	if !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("expected ErrUnsupported when sender is nil, got %v", err)
	}
}

type fakeUploader struct{}

func (f *fakeUploader) FromPath(ctx context.Context, path string) (tg.InputFileClass, error) {
	return &tg.InputFile{ID: 1, Parts: 1, Name: "test.png"}, nil
}
