package zaplog

import (
	"errors"
	"io"
	"testing"
	"time"

	"github.com/inipew/goultroid/internal/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

func BenchmarkAdapter_DisabledDebug(b *testing.B) {
	core := zapcore.NewNopCore()
	adapter := Wrap(zap.New(core))
	b.ReportAllocs()
	for b.Loop() {
		adapter.Debug("hot path debug", logging.Component("taskengine"), logging.Int("count", 42))
	}
}

func BenchmarkAdapter_Info(b *testing.B) {
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.InfoLevel)
	adapter := Wrap(zap.New(core))
	b.ReportAllocs()
	for b.Loop() {
		adapter.Info("job completed",
			logging.Component("jobs"),
			logging.Operation("commit"),
			logging.Duration("duration", 12*time.Millisecond),
		)
	}
}

func BenchmarkAdapter_Error(b *testing.B) {
	core := zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(io.Discard), zapcore.InfoLevel)
	adapter := Wrap(zap.New(core))
	err := errors.New("network timeout")
	b.ReportAllocs()
	for b.Loop() {
		adapter.Error("rpc failed", logging.Component("rpc"), logging.Error(err))
	}
}
