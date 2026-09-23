package app

import (
	"context"
	"errors"
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/plugin"
	"github.com/inipew/goultroid/internal/presentation/selfinline"
)

type p8hSelfInlinePlugin struct{}

func (*p8hSelfInlinePlugin) Name() string             { return "p8h-self-inline" }
func (*p8hSelfInlinePlugin) Init() error              { return nil }
func (*p8hSelfInlinePlugin) Commands() []core.Command { return nil }

type p8hSelfInlineRendererFunc func(context.Context, selfinline.Request) (selfinline.Result, error)

func (f p8hSelfInlineRendererFunc) Render(ctx context.Context, request selfinline.Request) (selfinline.Result, error) {
	return f(ctx, request)
}

func TestP8HSelfInlineAuthorizationTracksPluginEnableState(t *testing.T) {
	manager := plugin.NewManager(core.NewRouter("."))
	feature := &p8hSelfInlinePlugin{}
	if err := manager.RegisterWithContext(context.Background(), feature); err != nil {
		t.Fatalf("RegisterWithContext() error=%v", err)
	}
	t.Cleanup(func() {
		if manager.IsEnabled(feature.Name()) {
			_ = manager.ShutdownWithContext(context.Background())
		}
	})

	gate := plugin.NewCapabilityGate()
	if err := gate.RegisterManifest(plugin.Manifest{
		ID:      feature.Name(),
		Name:    feature.Name(),
		Version: "1",
		Capabilities: []string{
			plugin.CapTelegramRead,
			plugin.CapTelegramSendMessage,
		},
	}); err != nil {
		t.Fatalf("RegisterManifest() error=%v", err)
	}

	calls := 0
	renderer := selfinline.Authorized(
		p8hSelfInlineRendererFunc(func(context.Context, selfinline.Request) (selfinline.Result, error) {
			calls++
			return selfinline.Result{QueryID: int64(calls)}, nil
		}),
		selfInlineFeatureAuthorizer(manager, gate, feature.Name()),
	)

	first, err := renderer.Render(context.Background(), selfinline.Request{})
	if err != nil || first.QueryID != 1 {
		t.Fatalf("Render(enabled)=%+v err=%v", first, err)
	}

	if err := manager.Disable(context.Background(), feature.Name()); err != nil {
		t.Fatalf("Disable() error=%v", err)
	}
	if _, err := renderer.Render(context.Background(), selfinline.Request{}); !errors.Is(err, selfinline.ErrUnavailable) {
		t.Fatalf("Render(disabled) error=%v, want %v", err, selfinline.ErrUnavailable)
	}
	if calls != 1 {
		t.Fatalf("disabled renderer reached transport delegate, calls=%d", calls)
	}

	if err := manager.Enable(context.Background(), feature.Name()); err != nil {
		t.Fatalf("Enable() error=%v", err)
	}
	second, err := renderer.Render(context.Background(), selfinline.Request{})
	if err != nil || second.QueryID != 2 {
		t.Fatalf("Render(re-enabled)=%+v err=%v", second, err)
	}
}
