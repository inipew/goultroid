package database

import (
	"context"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
)

// BenchmarkSQLiteIdlePoolBurst compares the cost of retaining one versus two
// idle SQLite handles across repeated short read bursts. Each iteration allows
// all burst connections to return to the idle pool, so a smaller idle limit
// pays any reconnect/setup cost on the next burst instead of hiding it behind a
// permanently saturated benchmark.
func BenchmarkSQLiteIdlePoolBurst(b *testing.B) {
	for _, maxIdle := range []int{1, 2} {
		b.Run(fmt.Sprintf("idle_%d", maxIdle), func(b *testing.B) {
			db, err := Open(filepath.Join(b.TempDir(), "pool-bench.db"))
			if err != nil {
				b.Fatal(err)
			}
			defer db.Close()

			db.SetMaxOpenConns(4)
			db.SetMaxIdleConns(maxIdle)
			db.SetConnMaxIdleTime(0)

			ctx := context.Background()
			if _, err := db.ExecContext(ctx, "CREATE TABLE IF NOT EXISTS benchmark_kv (id INTEGER PRIMARY KEY, value TEXT NOT NULL)"); err != nil {
				b.Fatal(err)
			}
			for i := 0; i < 256; i++ {
				if _, err := db.ExecContext(ctx, "INSERT OR REPLACE INTO benchmark_kv(id, value) VALUES(?, ?)", i, "value"); err != nil {
					b.Fatal(err)
				}
			}

			// Warm schema/page state before measuring pool retention policy.
			for i := 0; i < 4; i++ {
				var value string
				if err := db.QueryRowContext(ctx, "SELECT value FROM benchmark_kv WHERE id = ?", i).Scan(&value); err != nil {
					b.Fatal(err)
				}
			}

			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				var wg sync.WaitGroup
				wg.Add(4)
				for j := 0; j < 4; j++ {
					id := (i*4 + j) & 255
					go func() {
						defer wg.Done()
						var value string
						if err := db.QueryRowContext(ctx, "SELECT value FROM benchmark_kv WHERE id = ?", id).Scan(&value); err != nil {
							b.Error(err)
						}
					}()
				}
				wg.Wait()
			}
			b.StopTimer()
			stats := db.Stats()
			b.ReportMetric(float64(stats.OpenConnections), "open_conns")
			b.ReportMetric(float64(stats.Idle), "idle_conns")
		})
	}
}
