package telegram

import (
	"testing"

	"github.com/inipew/goultroid/internal/core"
	"go.uber.org/zap"
)

func TestM2RuntimeServiceKeepsNarrowCapabilitiesWithoutCompatAggregate(t *testing.T) {
	dispatcher := NewDispatcher(
		core.NewRouter("."),
		core.NewPermissions(1, nil),
		nil,
		zap.NewNop(),
	)
	service := &Service{}
	dispatcher.setRuntimeService(service)

	if dispatcher.Service() != nil {
		t.Fatal("runtime service unexpectedly populated compatibility Dispatcher.Service")
	}
	if got := dispatcher.MessageService(); got != service {
		t.Fatalf("runtime message capability = %T %p, want service %p", got, got, service)
	}
	if got := dispatcher.AdminService(); got != service {
		t.Fatalf("runtime admin capability = %T %p, want service %p", got, got, service)
	}
	if got := dispatcher.MediaService(); got != service {
		t.Fatalf("runtime media capability = %T %p, want service %p", got, got, service)
	}
	if got := dispatcher.OriginTracker(); got != service {
		t.Fatalf("runtime origin tracker = %T %p, want service %p", got, got, service)
	}
	if got := dispatcher.SelfInlineTransport(); got != service {
		t.Fatalf("runtime self-inline transport = %T %p, want service %p", got, got, service)
	}

	client := &Client{dispatcher: dispatcher}
	if got := client.SelfInlineTransport(); got != service {
		t.Fatalf("client self-inline transport = %T %p, want service %p", got, got, service)
	}
}
