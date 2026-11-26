package logging

import (
	"errors"
	"testing"
)

func BenchmarkRedactString_Plain(b *testing.B) {
	s := "scheduler started cleanly with 12 registered periodic jobs"
	b.ReportAllocs()
	for b.Loop() {
		_ = RedactString(s)
	}
}

func BenchmarkRedactString_WithURLAndSecrets(b *testing.B) {
	s := `request to https://user:pw@example.com/path?token=abc failed: Bearer xyz accessToken=123 password="my secret"`
	b.ReportAllocs()
	for b.Loop() {
		_ = RedactString(s)
	}
}

func BenchmarkRedactField_Primitives(b *testing.B) {
	f := String("component", "scheduler")
	b.ReportAllocs()
	for b.Loop() {
		_ = RedactField(f)
	}
}

func BenchmarkRedactField_Error(b *testing.B) {
	f := Error(errors.New("connection refused"))
	b.ReportAllocs()
	for b.Loop() {
		_ = RedactField(f)
	}
}
