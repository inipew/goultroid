package jobs

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestPersistencePumpCallbackAckPath(t *testing.T) {
	p := NewPersistencePump(1, 4)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	wantErr := errors.New("commit failed")
	ack := make(chan error, 1)
	if err := p.EnqueueSizedAck(context.Background(), 1024, func(context.Context) error {
		return wantErr
	}, func(err error) {
		ack <- err
	}); err != nil {
		t.Fatal(err)
	}

	select {
	case err := <-ack:
		if !errors.Is(err, wantErr) {
			t.Fatalf("ack error=%v, want %v", err, wantErr)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for callback acknowledgement")
	}

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) && p.Stats().RetainedBytes != 0 {
		time.Sleep(time.Millisecond)
	}
	if got := p.Stats().RetainedBytes; got != 0 {
		t.Fatalf("retained bytes after callback acknowledgement=%d", got)
	}
}

func TestPersistencePumpCallbackAckRejectsNilCallback(t *testing.T) {
	p := NewPersistencePump(1, 1)
	if err := p.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer p.Stop(context.Background())

	if err := p.EnqueueSizedAck(context.Background(), 512, func(context.Context) error { return nil }, nil); err == nil {
		t.Fatal("expected nil callback rejection")
	}
}
