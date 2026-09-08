package core_test

import (
	"sync"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/core"
)

func TestEventBusConcurrentPublishAndClose(t *testing.T) {
	bus := core.NewEventBus()
	var wg sync.WaitGroup
	bus.Subscribe(core.EventTypeMessageEdited, func(core.Event) {})
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 500; j++ {
				bus.Publish(&core.MessageEditedEvent{At: time.Now()})
			}
		}()
	}
	wg.Add(1)
	go func() { defer wg.Done(); time.Sleep(time.Millisecond); _ = bus.Close() }()
	wg.Wait()
}
