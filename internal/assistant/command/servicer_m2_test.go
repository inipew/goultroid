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

func TestM2AssistantUnsupportedCapabilitiesFailClosed(t *testing.T) {
	svc := &assistantServicerAdapter{}

	checks := []struct {
		name string
		call func() error
	}{
		{
			name: "react",
			call: func() error {
				return svc.React(context.Background(), &tg.InputPeerSelf{}, 1, "👍")
			},
		},
		{
			name: "forward",
			call: func() error {
				return svc.ForwardMessages(context.Background(), &tg.InputPeerSelf{}, &tg.InputPeerSelf{}, []int{1})
			},
		},
		{
			name: "download",
			call: func() error {
				return svc.DownloadFile(context.Background(), nil, "ignored")
			},
		},
		{
			name: "block",
			call: func() error {
				return svc.BlockUser(context.Background(), &tg.InputPeerSelf{})
			},
		},
		{
			name: "profile",
			call: func() error {
				return svc.UpdateProfile(context.Background(), nil, nil, nil)
			},
		},
	}

	for _, tc := range checks {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.call(); !errors.Is(err, core.ErrUnsupported) {
				t.Fatalf("error = %v, want ErrUnsupported", err)
			}
		})
	}

	if _, err := svc.GetFullUser(context.Background(), nil); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("GetFullUser error = %v, want ErrUnsupported", err)
	}
	if _, err := svc.GetDialogs(context.Background(), 1); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("GetDialogs error = %v, want ErrUnsupported", err)
	}
	if _, err := svc.GetContacts(context.Background()); !errors.Is(err, core.ErrUnsupported) {
		t.Fatalf("GetContacts error = %v, want ErrUnsupported", err)
	}
}
