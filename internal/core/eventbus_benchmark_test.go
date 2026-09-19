package core

import (
	"context"
	"fmt"
	"sync/atomic"
	"testing"
	"time"
)

// BenchmarkEventBusActivePublish measures the pointer-backed pooled queue path,
// not the no-subscriber fast path. The event object is reused so reported
// allocations focus on EventBus fanout/envelope overhead.
func BenchmarkEventBusActivePublish(b *testing.B) {
	for _, subscribers := range []int{1, 4, 8} {
		b.Run(fmt.Sprintf("subscribers_%d", subscribers), func(b *testing.B) {
			bus := NewEventBus()
			bus.workerIdleTimeout = time.Minute
			if err := bus.Start(context.Background()); err != nil {
				b.Fatal(err)
			}
			defer bus.Close()

			done := make(chan struct{}, subscribers)
			var calls atomic.Int64
			for i := 0; i < subscribers; i++ {
				bus.Subscribe(EventTypeMessageCreated, func(Event) {
					calls.Add(1)
					done <- struct{}{}
				})
			}
			event := &MessageCreatedEvent{At: time.Unix(1, 0), ChatID: 42}

			// Warm workers and sync.Pool before allocation measurements.
			bus.Publish(event)
			for i := 0; i < subscribers; i++ {
				<-done
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				bus.Publish(event)
				for j := 0; j < subscribers; j++ {
					<-done
				}
			}
			b.StopTimer()
			want := int64(b.N+1) * int64(subscribers)
			if got := calls.Load(); got != want {
				b.Fatalf("handler calls=%d, want %d", got, want)
			}
		})
	}
}
