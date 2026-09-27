package command

import (
	"context"
	"errors"
	"testing"

	"github.com/gotd/td/tg"
	assistantinteraction "github.com/inipew/goultroid/internal/assistant/interaction"
	"github.com/inipew/goultroid/internal/core"
)

type m2FailingMessageInteraction struct {
	deleteErr error
}

func (*m2FailingMessageInteraction) Answer(context.Context, int64, string, bool) error {
	return nil
}

func (*m2FailingMessageInteraction) Edit(context.Context, assistantinteraction.MessageTarget, string, tg.ReplyMarkupClass) error {
	return nil
}

func (*m2FailingMessageInteraction) EditMarkup(context.Context, assistantinteraction.MessageTarget, tg.ReplyMarkupClass) error {
	return nil
}

func (f *m2FailingMessageInteraction) Delete(context.Context, assistantinteraction.MessageTarget) error {
	return f.deleteErr
}

func (*m2FailingMessageInteraction) GetMessage(context.Context, assistantinteraction.MessageTarget) (*tg.Message, error) {
	return nil, nil
}

func (*m2FailingMessageInteraction) SendMessage(context.Context, tg.InputPeerClass, string, tg.ReplyMarkupClass) (*tg.Message, error) {
	return nil, nil
}

func (*m2FailingMessageInteraction) SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error) {
	return nil, nil
}

func TestM2AssistantDeleteMessagePropagatesTransportError(t *testing.T) {
	sentinel := errors.New("delete failed")
	svc := &assistantServicerAdapter{
		inter: &m2FailingMessageInteraction{deleteErr: sentinel},
	}

	err := svc.DeleteMessage(
		context.Background(),
		&tg.InputPeerUser{UserID: 1, AccessHash: 2},
		[]int{10},
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("DeleteMessage error = %v, want %v", err, sentinel)
	}
}

func TestM2AssistantCapabilitiesReflectActualSupport(t *testing.T) {
	svc := &assistantServicerAdapter{}
	caps := assistantTelegramCapabilities(svc)

	if caps.MessageActions == nil {
		t.Fatal("assistant message actions capability is nil")
	}
	if caps.Admin == nil {
		t.Fatal("assistant admin capability is nil")
	}
	if caps.MediaSend == nil {
		t.Fatal("assistant media-send capability is nil")
	}
	if caps.FullChat == nil {
		t.Fatal("assistant full-chat capability is nil")
	}
	if caps.ContextualMessages == nil || caps.ContextualMedia == nil {
		t.Fatal("assistant contextual delivery capabilities are nil")
	}

	if caps.Messages != nil {
		t.Fatal("assistant unexpectedly advertises full MessageServicer")
	}
	if caps.Reactions != nil {
		t.Fatal("assistant unexpectedly advertises reaction capability")
	}
	if caps.Forwarding != nil {
		t.Fatal("assistant unexpectedly advertises forwarding capability")
	}
	if caps.Media != nil || caps.MediaDownload != nil {
		t.Fatal("assistant unexpectedly advertises media-download capability")
	}
	if caps.Peers != nil || caps.Profile != nil {
		t.Fatal("assistant unexpectedly advertises broad peer/profile capability")
	}

	if _, ok := any(svc).(core.CommandTelegramServicer); ok {
		t.Fatal("assistant adapter unexpectedly satisfies CommandTelegramServicer")
	}
	if _, ok := any(svc).(core.MessageServicer); ok {
		t.Fatal("assistant adapter unexpectedly satisfies full MessageServicer")
	}
	if _, ok := any(svc).(core.MediaServicer); ok {
		t.Fatal("assistant adapter unexpectedly satisfies full MediaServicer")
	}
	if _, ok := any(svc).(core.PeerServicer); ok {
		t.Fatal("assistant adapter unexpectedly satisfies full PeerServicer")
	}
}
