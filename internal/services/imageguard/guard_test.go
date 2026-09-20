package imageguard

import (
	"errors"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestInspectAndDecode(t *testing.T) {
	path := writePNG(t, 10, 5)
	info, err := Inspect(path, Policy{})
	if err != nil {
		t.Fatalf("Inspect failed: %v", err)
	}
	if info.Format != "png" || info.Width != 10 || info.Height != 5 {
		t.Fatalf("unexpected info: %+v", info)
	}
	if info.Pixels != 50 || info.EstimatedDecodedBytes != 200 {
		t.Fatalf("unexpected budgets: %+v", info)
	}

	img, decoded, err := Decode(path, Policy{})
	if err != nil {
		t.Fatalf("Decode failed: %v", err)
	}
	if img.Bounds().Dx() != 10 || img.Bounds().Dy() != 5 {
		t.Fatalf("unexpected decoded bounds: %v", img.Bounds())
	}
	if decoded != info {
		t.Fatalf("decoded info mismatch: got %+v want %+v", decoded, info)
	}
}

func TestInspectRejectsSafetyBudgets(t *testing.T) {
	path := writePNG(t, 10, 10)
	tests := []struct {
		name   string
		policy Policy
		want   error
	}{
		{name: "input bytes", policy: Policy{MaxInputBytes: 1}, want: ErrInputTooLarge},
		{name: "dimensions", policy: Policy{MaxWidth: 9, MaxHeight: 20}, want: ErrDimensionsExceeded},
		{name: "pixels", policy: Policy{MaxWidth: 20, MaxHeight: 20, MaxPixels: 99}, want: ErrPixelBudgetExceeded},
		{name: "decoded bytes", policy: Policy{MaxWidth: 20, MaxHeight: 20, MaxPixels: 1000, MaxDecodedBytes: 399}, want: ErrDecodedBudgetExceeded},
		{name: "format", policy: Policy{AllowedFormats: []string{"jpeg"}}, want: ErrUnsupportedFormat},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Inspect(path, tt.policy)
			if !errors.Is(err, tt.want) {
				t.Fatalf("got %v, want %v", err, tt.want)
			}
		})
	}
}

func TestValidateKnownRejectsBeforeIO(t *testing.T) {
	policy := Policy{MaxInputBytes: 1024, MaxWidth: 100, MaxHeight: 100, MaxPixels: 5_000, MaxDecodedBytes: 20_000}
	if err := ValidateKnown(2048, 10, 10, policy); !errors.Is(err, ErrInputTooLarge) {
		t.Fatalf("expected input size rejection, got %v", err)
	}
	if err := ValidateKnown(512, 101, 10, policy); !errors.Is(err, ErrDimensionsExceeded) {
		t.Fatalf("expected dimension rejection, got %v", err)
	}
	if err := ValidateKnown(0, 0, 0, policy); err != nil {
		t.Fatalf("unknown metadata should defer to file inspection: %v", err)
	}
}

func writePNG(t *testing.T, width, height int) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "image.png")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, image.NewRGBA(image.Rect(0, 0, width, height))); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	return path
}
