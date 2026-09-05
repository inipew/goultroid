package core

import (
	"errors"
	"fmt"
	"testing"
	"time"
)

func TestErrorTaxonomy_Sentinels(t *testing.T) {
	cases := []struct {
		err  error
		name string
	}{
		{ErrPermissionDenied, "ErrPermissionDenied"},
		{ErrGroupOnly, "ErrGroupOnly"},
		{ErrPrivateOnly, "ErrPrivateOnly"},
		{ErrReplyRequired, "ErrReplyRequired"},
		{ErrCooldownActive, "ErrCooldownActive"},
		{ErrInvalidArgs, "ErrInvalidArgs"},
		{ErrNotFound, "ErrNotFound"},
		{ErrUnsupported, "ErrUnsupported"},
		{ErrMedia, "ErrMedia"},
		{ErrStorage, "ErrStorage"},
		{ErrTimeout, "ErrTimeout"},
		{ErrInternal, "ErrInternal"},
		{ErrTelegram, "ErrTelegram"},
		{ErrRateLimited, "ErrRateLimited"},
	}

	for _, tc := range cases {
		if tc.err == nil {
			t.Errorf("sentinel error %s is nil", tc.name)
		}
		if tc.err.Error() == "" {
			t.Errorf("sentinel error %s has empty message", tc.name)
		}
	}
}

func TestRateLimitError_Behavior(t *testing.T) {
	origErr := errors.New("rpc error: FLOOD_WAIT_15")
	rle := NewRateLimitError(15*time.Second, origErr)

	// 1. Error message
	msg := rle.Error()
	if msg != "rate limited by telegram: wait 15s before retrying" {
		t.Errorf("unexpected error message: %q", msg)
	}

	// 2. Unwrap
	if !errors.Is(rle.Unwrap(), origErr) {
		t.Errorf("expected unwrapped error to match origErr")
	}

	// 3. errors.Is check
	if !errors.Is(rle, ErrRateLimited) {
		t.Errorf("expected errors.Is(rle, ErrRateLimited) to be true")
	}

	wrapped := fmt.Errorf("operation failed: %w", rle)
	if !errors.Is(wrapped, ErrRateLimited) {
		t.Errorf("expected errors.Is(wrapped, ErrRateLimited) to be true")
	}

	var extracted *RateLimitError
	if !errors.As(wrapped, &extracted) {
		t.Errorf("expected errors.As(wrapped, &extracted) to succeed")
	}
	if extracted.Wait != 15*time.Second {
		t.Errorf("expected wait duration 15s, got %v", extracted.Wait)
	}
}
