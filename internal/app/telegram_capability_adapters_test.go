package app

import (
	"context"
	"testing"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/telegram"
	"go.uber.org/zap"
)

type moduleTelegramRecorder struct {
	core.MockTelegramServicer
	sends       int
	bans        int
	mediaSends  int
	originCalls int
}

func (r *moduleTelegramRecorder) SendMessage(context.Context, tg.InputPeerClass, string) (*tg.Message, error) {
	r.sends++
	return &tg.Message{ID: 1}, nil
}

func (r *moduleTelegramRecorder) BanUser(context.Context, tg.InputPeerClass, tg.InputPeerClass, int) error {
	r.bans++
	return nil
}

func (r *moduleTelegramRecorder) SendMedia(context.Context, tg.InputPeerClass, string, string, string) (*tg.Message, error) {
	r.mediaSends++
	return &tg.Message{ID: 2}, nil
}

func (r *moduleTelegramRecorder) IsBotSent(int) bool {
	r.originCalls++
	return true
}

type moduleTelegramProviderStub struct {
	recorder *moduleTelegramRecorder
}

func (p moduleTelegramProviderStub) MessageService() core.MessageServicer {
	return p.recorder
}

func (p moduleTelegramProviderStub) AdminService() core.AdminServicer {
	return p.recorder
}

func (p moduleTelegramProviderStub) MediaService() core.MediaServicer {
	return p.recorder
}

func (moduleTelegramProviderStub) ContextualMessageService() core.ContextualMessageServicer {
	return nil
}

func (moduleTelegramProviderStub) ContextualMediaService() core.ContextualMediaServicer {
	return nil
}

func (p moduleTelegramProviderStub) OriginTracker() telegram.OriginTracker {
	return p.recorder
}

func (moduleTelegramProviderStub) Resolver() core.PeerResolver {
	return nil
}

func TestM2ModuleTelegramRuntimeForwardsThroughNarrowProviders(t *testing.T) {
	recorder := &moduleTelegramRecorder{}
	runtime := newModuleTelegramRuntime(moduleTelegramProviderStub{recorder: recorder})

	messageSvc := runtime.MessageService()
	if messageSvc == nil {
		t.Fatal("module message provider returned nil")
	}
	if _, err := messageSvc.SendMessage(context.Background(), &tg.InputPeerSelf{}, "hello"); err != nil {
		t.Fatalf("SendMessage() error = %v", err)
	}

	adminSvc := runtime.AdminService()
	if adminSvc == nil {
		t.Fatal("module admin provider returned nil")
	}
	if err := adminSvc.BanUser(context.Background(), &tg.InputPeerChat{ChatID: 1}, &tg.InputPeerUser{UserID: 2}, 0); err != nil {
		t.Fatalf("BanUser() error = %v", err)
	}

	mediaSvc := runtime.MediaService()
	if mediaSvc == nil {
		t.Fatal("module media provider returned nil")
	}
	if _, err := mediaSvc.SendMedia(context.Background(), &tg.InputPeerSelf{}, "file", "x", "caption"); err != nil {
		t.Fatalf("SendMedia() error = %v", err)
	}

	origin := runtime.OriginTracker()
	if origin == nil || !origin.IsBotSent(10) {
		t.Fatal("module origin provider did not forward classification")
	}

	if recorder.sends != 1 || recorder.bans != 1 || recorder.mediaSends != 1 || recorder.originCalls != 1 {
		t.Fatalf(
			"forwarded calls send/ban/media/origin = %d/%d/%d/%d, want 1/1/1/1",
			recorder.sends,
			recorder.bans,
			recorder.mediaSends,
			recorder.originCalls,
		)
	}
}

func TestM2ModuleTelegramRuntimeBindsConcreteDispatcherCapabilities(t *testing.T) {
	service := &telegram.Service{}
	dispatcher := telegram.NewDispatcher(
		core.NewRouter("."),
		core.NewPermissions(1, nil),
		service,
		zap.NewNop(),
	)
	runtime := newModuleTelegramRuntime(dispatcher)

	if got := runtime.MessageService(); got != service {
		t.Fatalf("message provider = %T %p, want concrete service %p", got, got, service)
	}
	if got := runtime.AdminService(); got != service {
		t.Fatalf("admin provider = %T %p, want concrete service %p", got, got, service)
	}
	if got := runtime.MediaService(); got != service {
		t.Fatalf("media provider = %T %p, want concrete service %p", got, got, service)
	}
	if got := runtime.ContextualMessageService(); got != service {
		t.Fatalf("contextual message provider = %T %p, want concrete service %p", got, got, service)
	}
	if got := runtime.ContextualMediaService(); got != service {
		t.Fatalf("contextual media provider = %T %p, want concrete service %p", got, got, service)
	}
	if got := runtime.OriginTracker(); got != service {
		t.Fatalf("origin provider = %T %p, want concrete service %p", got, got, service)
	}
}
