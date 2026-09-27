package app

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

type profileErrorWriter struct{ err error }

func (w profileErrorWriter) Write([]byte) (int, error) { return 0, w.err }

func TestWriteGoroutineProfile(t *testing.T) {
	var out bytes.Buffer
	if err := (&App{}).WriteGoroutineProfile(&out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "goroutine ") {
		t.Fatalf("profile does not contain goroutine stacks: %q", out.String())
	}
}

func TestWriteGoroutineProfile_WriterError(t *testing.T) {
	want := errors.New("writer failed")
	if err := (&App{}).WriteGoroutineProfile(profileErrorWriter{want}); !errors.Is(err, want) {
		t.Fatalf("error = %v, want %v", err, want)
	}
	if err := (&App{}).WriteGoroutineProfile(nil); err == nil {
		t.Fatal("nil writer was accepted")
	}
}
