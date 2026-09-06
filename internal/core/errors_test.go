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
		{ErrResourceLimit, "ErrResourceLimit"},
		{ErrConflict, "ErrConflict"},
		{ErrUnavailable, "ErrUnavailable"},
		{ErrLeaseLost, "ErrLeaseLost"},
		{ErrCancelled, "ErrCancelled"},
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

func TestIsPermanentError(t *testing.T) {
	tests := []struct {
		err       error
		permanent bool
	}{
		{nil, false},
		{ErrRateLimited, false},
		{NewRateLimitError(10*time.Second, nil), false},
		{ErrTimeout, false},
		{errors.New("rpc error: FLOOD_WAIT_30"), false},
		{errors.New("network connection reset by peer"), false},
		{ErrConflict, false},
		{ErrUnavailable, false},
		{ErrLeaseLost, false},
		{ErrCancelled, false},
		{ErrPermissionDenied, true},
		{ErrNotFound, true},
		{ErrInvalidArgs, true},
		{ErrUnsupported, true},
		{ErrUnclosedQuote, true},
		{ErrTrailingEscape, true},
		{ErrResourceLimit, true},
		{errors.New("rpc error: CHAT_WRITE_FORBIDDEN"), true},
		{errors.New("rpc error: CHANNEL_PRIVATE"), true},
		{errors.New("rpc error: USER_BANNED_IN_CHANNEL"), true},
		{errors.New("rpc error: PEER_ID_INVALID"), true},
		{errors.New("scheduled command not found in router: foo"), true},
	}

	for _, tc := range tests {
		got := IsPermanentError(tc.err)
		if got != tc.permanent {
			t.Errorf("IsPermanentError(%v) = %v, want %v", tc.err, got, tc.permanent)
		}
	}
}

func TestClassifyError(t *testing.T) {
	tests := []struct {
		err      error
		expected ErrorCategory
	}{
		{nil, CategoryNone},
		{ErrRateLimited, CategoryRateLimited},
		{NewRateLimitError(5*time.Second, nil), CategoryRateLimited},
		{errors.New("rpc error: FLOOD_WAIT_10"), CategoryRateLimited},
		{ErrPermissionDenied, CategorySecurity},
		{ErrInvalidArgs, CategoryInvalidInput},
		{ErrUnclosedQuote, CategoryInvalidInput},
		{ErrTrailingEscape, CategoryInvalidInput},
		{ErrGroupOnly, CategoryInvalidInput},
		{ErrResourceLimit, CategoryResourceLimit},
		{ErrTimeout, CategoryTransient},
		{ErrUnavailable, CategoryTransient},
		{ErrConflict, CategoryTransient},
		{ErrLeaseLost, CategoryTransient},
		{errors.New("connection timeout"), CategoryTransient},
		{ErrNotFound, CategoryPermanent},
		{ErrUnsupported, CategoryPermanent},
		{errors.New("rpc error: USER_BANNED"), CategoryPermanent},
		{errors.New("some arbitrary unknown error"), CategoryInternal},
	}

	for _, tc := range tests {
		got := ClassifyError(tc.err)
		if got != tc.expected {
			t.Errorf("ClassifyError(%v) = %v, want %v", tc.err, got, tc.expected)
		}
	}
}
