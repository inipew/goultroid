package download

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"testing"
	"time"
)

func BenchmarkP2C_SafeDownloader(b *testing.B) {
	ctx := context.Background()
	for _, size := range []int{10 * 1024, 1024 * 1024} {
		payload := bytes.Repeat([]byte("b"), size)
		b.Run(fmt.Sprintf("download_%dB", size), func(b *testing.B) {
			dl := NewSafeDownloader(5*time.Second, 10*1024*1024)
			dl.client.Transport = roundTripperFunc(func(req *http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode:    http.StatusOK,
					Status:        "200 OK",
					Body:          io.NopCloser(bytes.NewReader(payload)),
					ContentLength: int64(size),
					Request:       req,
				}, nil
			})
			dst := filepath.Join(b.TempDir(), "bench.bin")
			b.ReportAllocs()
			b.SetBytes(int64(size))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := dl.Download(ctx, "https://example.com/bench.bin", dst, 10*1024*1024); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
