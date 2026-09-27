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
	if dispatcher.MessageService() == nil {
		t.Fatal("runtime message capability is nil")
	}
	if dispatcher.AdminService() == nil {
		t.Fatal("runtime admin capability is nil")
	}
	if dispatcher.MediaService() == nil {
		t.Fatal("runtime media capability is nil")
	}
	if dispatcher.OriginTracker() == nil {
		t.Fatal("runtime origin tracker is nil")
	}
	if dispatcher.SelfInlineTransport() == nil {
		t.Fatal("runtime self-inline transport is nil")
	}

	client := &Client{dispatcher: dispatcher}
	if client.SelfInlineTransport() == nil {
		t.Fatal("client self-inline transport is nil")
	}
}
