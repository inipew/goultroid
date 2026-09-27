package myxl

import (
	"errors"
	"testing"
)

func TestPurchaseOptionEchoValidation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		echoed    string
		wantError bool
	}{
		{name: "omitted", echoed: ""},
		{name: "same", echoed: "OPT-A"},
		{name: "same case insensitive", echoed: "opt-a"},
		{name: "mismatch", echoed: "OPT-B", wantError: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validatePurchaseOptionEcho("OPT-A", tc.echoed)
			if tc.wantError {
				if !errors.Is(err, ErrPurchaseIntentInvalid) {
					t.Fatalf("error = %v, want ErrPurchaseIntentInvalid", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("validatePurchaseOptionEcho() error = %v", err)
			}
		})
	}
}
