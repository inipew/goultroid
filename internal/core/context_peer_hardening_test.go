package core

import (
	"context"
	"testing"
)

func TestResolveTargetUserRequiresResolver(t *testing.T) {
	ctx := &Context{
		Ctx:  context.Background(),
		Args: []string{"12345"},
	}
	_, _, err := (&PeerFacade{ctx: ctx}).ResolveTargetUser()
	if err == nil {
		t.Fatal("expected target resolution to fail when no peer resolver is configured")
	}
}
