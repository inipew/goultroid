package zaplog

import (
	"errors"
	"syscall"

	"github.com/inipew/goultroid/internal/logging"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Adapter bridges logging.Logger to an underlying *zap.Logger at the
// infrastructure boundary, enforcing pre-sink redaction on every field.
type Adapter struct {
	z *zap.Logger
}

// Wrap constructs an Adapter around z. If z is nil, a no-op logger is used.
func Wrap(z *zap.Logger) *Adapter {
	if z == nil {
		z = zap.NewNop()
	}
	return &Adapter{z: z}
}

func (a *Adapter) Debug(msg string, fields ...logging.Field) {
	a.log(zapcore.DebugLevel, msg, fields)
}

func (a *Adapter) Info(msg string, fields ...logging.Field) {
	a.log(zapcore.InfoLevel, msg, fields)
}

func (a *Adapter) Warn(msg string, fields ...logging.Field) {
	a.log(zapcore.WarnLevel, msg, fields)
}

func (a *Adapter) Error(msg string, fields ...logging.Field) {
	a.log(zapcore.ErrorLevel, msg, fields)
}

func (a *Adapter) With(fields ...logging.Field) (res logging.Logger) {
	if a == nil || a.z == nil {
		return logging.Nop()
	}
	defer func() {
		if recover() != nil {
			res = a
		}
	}()
	return &Adapter{z: a.z.With(toZapFields(fields)...)}
}

func (a *Adapter) Named(name string) (res logging.Logger) {
	if a == nil || a.z == nil {
		return logging.Nop()
	}
	if name == "" {
		return a
	}
	defer func() {
		if recover() != nil {
			res = a
		}
	}()
	return &Adapter{z: a.z.Named(name)}
}

// Sync flushes any buffered log entries. Intended solely for the application
// lifecycle owner during shutdown.
func (a *Adapter) Sync() error {
	if a == nil || a.z == nil {
		return nil
	}
	err := a.z.Sync()
	if err != nil && (errors.Is(err, syscall.EINVAL) || errors.Is(err, syscall.ENOTTY)) {
		return nil
	}
	return err
}

func (a *Adapter) log(level zapcore.Level, msg string, fields []logging.Field) {
	if a == nil || a.z == nil {
		return
	}
	// Logging failures must never crash or fail the caller.
	defer func() { _ = recover() }()

	ce := a.z.Check(level, logging.RedactString(msg))
	if ce == nil {
		return
	}
	ce.Write(toZapFields(fields)...)
}

func toZapFields(fields []logging.Field) []zap.Field {
	if len(fields) == 0 {
		return nil
	}
	out := make([]zap.Field, 0, len(fields))
	for _, raw := range fields {
		f := logging.RedactField(raw)
		if f.Key == "" {
			continue
		}
		switch f.Kind {
		case logging.KindString:
			out = append(out, zap.String(f.Key, f.Str))
		case logging.KindInt64:
			out = append(out, zap.Int64(f.Key, f.Int64))
		case logging.KindUint64:
			out = append(out, zap.Uint64(f.Key, f.Uint64))
		case logging.KindFloat64:
			out = append(out, zap.Float64(f.Key, f.Float))
		case logging.KindBool:
			out = append(out, zap.Bool(f.Key, f.Int64 != 0))
		case logging.KindDuration:
			out = append(out, zap.Duration(f.Key, logging.DurationFromInt64(f.Int64)))
		case logging.KindTime:
			out = append(out, zap.Time(f.Key, f.Time))
		case logging.KindPanic:
			if rp, ok := f.Any.(logging.RedactedPanic); ok {
				out = append(out,
					zap.String("panic_type", rp.Type),
					zap.String("panic_summary", rp.Summary),
					zap.String("stack", rp.Stack),
				)
			}
		case logging.KindMetadata:
			if meta, ok := f.Any.(map[string]string); ok && len(meta) > 0 {
				out = append(out, zap.Dict(f.Key, metadataFields(meta)...))
			}
		default:
			out = append(out, zap.String(f.Key, logging.UnknownKindPlaceholder))
		}
	}
	return out
}

func metadataFields(meta map[string]string) []zap.Field {
	fields := make([]zap.Field, 0, len(meta))
	for k, v := range meta {
		fields = append(fields, zap.String(k, v))
	}
	return fields
}
