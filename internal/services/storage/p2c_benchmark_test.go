package storage

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"
)

func BenchmarkP2C_FileStorage(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{10 * 1024, 1024 * 1024} {
		payload := bytes.Repeat([]byte("a"), size)
		b.Run(fmt.Sprintf("put_open_delete_%dB", size), func(b *testing.B) {
			fs, err := NewFileStorage(b.TempDir(), 5*1024*1024*1024)
			if err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				asset, err := fs.Put(ctx, bytes.NewReader(payload), Metadata{Name: "bench.bin", MIME: "application/octet-stream"})
				if err != nil {
					b.Fatal(err)
				}
				rc, err := fs.Open(ctx, asset.ID)
				if err != nil {
					b.Fatal(err)
				}
				if _, err := io.Copy(io.Discard, rc); err != nil {
					_ = rc.Close()
					b.Fatal(err)
				}
				_ = rc.Close()
				if err := fs.Delete(ctx, asset.ID); err != nil {
					b.Fatal(err)
				}
			}
		})
	}

	b.Run("diagnostics_snapshot", func(b *testing.B) {
		fs, err := NewFileStorage(b.TempDir(), 5*1024*1024*1024)
		if err != nil {
			b.Fatal(err)
		}
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			_ = fs.Diagnostics()
		}
	})
}
