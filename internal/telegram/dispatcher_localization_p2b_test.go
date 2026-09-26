package telegram

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/gotd/td/tg"
	"github.com/inipew/goultroid/internal/core"
	"github.com/inipew/goultroid/internal/services/localization"
	"go.uber.org/zap"
)

func TestP2BDispatcherBindsInvocationLocalizer(t *testing.T) {
	router := core.NewRouter(".")
	perms := core.NewPermissions(1001, nil)

	var wg sync.WaitGroup
	wg.Add(1)
	var got string
	if err := router.Register(core.Command{
		Name: "locale",
		Handler: func(ctx *core.Context) error {
			defer wg.Done()
			got = ctx.T("common.cancelled")
			return nil
		},
	}); err != nil {
		t.Fatal(err)
	}

	d := NewDispatcher(router, perms, nil, zap.NewNop())
	configureDispatcherTasks(t, d)
	d.SetSelfID(1001)
	base := localization.New(localization.LocaleEnglish)
	d.SetLocalizer(base)
	d.SetLocalizerResolver(func(context.Context, int64, int64) core.Localizer {
		return localization.Bind(base, localization.LocaleIndonesian)
	})

	entities := tg.Entities{
		Users: map[int64]*tg.User{1001: {ID: 1001, FirstName: "Alice"}},
	}
	if err := d.OnNewMessage(context.Background(), entities, &tg.UpdateNewMessage{
		Message: &tg.Message{
			ID: 1, Out: true, Message: ".locale",
			PeerID: &tg.PeerUser{UserID: 1001},
			FromID: &tg.PeerUser{UserID: 1001},
		},
	}); err != nil {
		t.Fatal(err)
	}

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for localized command")
	}
	if got != "Operasi dibatalkan." {
		t.Fatalf("localized result=%q", got)
	}
	if base.GetLocale() != localization.LocaleEnglish {
		t.Fatalf("base locale mutated=%q", base.GetLocale())
	}
}
