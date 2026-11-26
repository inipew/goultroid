package logging

import (
	"fmt"
	"time"
)

// Level represents the severity of an operational log event.
type Level uint8

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

// String returns the lowercase canonical name of the log level.
func (l Level) String() string {
	switch l {
	case LevelDebug:
		return "debug"
	case LevelInfo:
		return "info"
	case LevelWarn:
		return "warn"
	case LevelError:
		return "error"
	default:
		return "unknown"
	}
}

// Logger is the minimal operational logging contract for Goultroid.
// Implementations must never panic, terminate the process, or fail callers.
type Logger interface {
	Debug(msg string, fields ...Field)
	Info(msg string, fields ...Field)
	Warn(msg string, fields ...Field)
	Error(msg string, fields ...Field)
	With(fields ...Field) Logger
	Named(name string) Logger
}

// FieldKind identifies the concrete value type stored in a Field.
type FieldKind uint8

const (
	KindUnknown FieldKind = iota
	KindString
	KindInt64
	KindUint64
	KindFloat64
	KindBool
	KindDuration
	KindTime
	KindURL
	KindError
	KindPanic
	KindMetadata
	KindAny
)

// Field is a flat value-typed structured log attribute.
// Primitive constructors avoid heap boxing into interface{}.
type Field struct {
	Key    string
	Kind   FieldKind
	Int64  int64
	Uint64 uint64
	Float  float64
	Str    string
	Time   time.Time
	Any    any
}

// String constructs a string field.
func String(key, val string) Field {
	return Field{Key: key, Kind: KindString, Str: val}
}

// Int constructs an integer field.
func Int(key string, val int) Field {
	return Field{Key: key, Kind: KindInt64, Int64: int64(val)}
}

// Int64 constructs a 64-bit integer field.
func Int64(key string, val int64) Field {
	return Field{Key: key, Kind: KindInt64, Int64: val}
}

// Uint64 constructs an unsigned 64-bit integer field.
func Uint64(key string, val uint64) Field {
	return Field{Key: key, Kind: KindUint64, Uint64: val}
}

// Float64 constructs a 64-bit floating-point field.
func Float64(key string, val float64) Field {
	return Field{Key: key, Kind: KindFloat64, Float: val}
}

// Bool constructs a boolean field.
func Bool(key string, val bool) Field {
	var v int64
	if val {
		v = 1
	}
	return Field{Key: key, Kind: KindBool, Int64: v}
}

// Duration constructs a duration field.
func Duration(key string, val time.Duration) Field {
	return Field{Key: key, Kind: KindDuration, Int64: int64(val)}
}

// DurationFromInt64 converts a stored int64 nanosecond count back to time.Duration.
func DurationFromInt64(v int64) time.Duration {
	return time.Duration(v)
}

// Time constructs a timestamp field.
func Time(key string, val time.Time) Field {
	return Field{Key: key, Kind: KindTime, Time: val}
}

// URL constructs a conservatively redacted URL field (scheme and safe host only).
func URL(key, rawURL string) Field {
	return Field{Key: key, Kind: KindURL, Str: rawURL, Any: URLPolicy{}}
}

// URLWithPolicy constructs a URL field using an explicit URLPolicy.
func URLWithPolicy(key, rawURL string, policy URLPolicy) Field {
	return Field{Key: key, Kind: KindURL, Str: rawURL, Any: policy}
}

// Error constructs a redacted error field under the standard "error" key.
func Error(err error) Field {
	return NamedError("error", err)
}

// NamedError constructs a redacted error field under a custom key.
func NamedError(key string, err error) Field {
	return Field{Key: key, Kind: KindError, Any: err}
}

type panicPayload struct {
	recoverVal any
	stack      []byte
}

// Panic constructs a bounded, redacted panic field.
func Panic(recoverVal any, stack []byte) Field {
	return Field{
		Key:  "panic",
		Kind: KindPanic,
		Any:  panicPayload{recoverVal: recoverVal, stack: stack},
	}
}

type metadataPayload struct {
	values      map[string]string
	allowedKeys []string
}

// Metadata constructs an allowlisted string-map field.
func Metadata(key string, values map[string]string, allowedKeys ...string) Field {
	return Field{
		Key:  key,
		Kind: KindMetadata,
		Any:  metadataPayload{values: values, allowedKeys: allowedKeys},
	}
}

// Any constructs a field from an arbitrary value. The redactor never assumes
// unknown structs, maps, slices, or Stringers are safe.
func Any(key string, val any) Field {
	return Field{Key: key, Kind: KindAny, Str: fmt.Sprintf("%T", val), Any: val}
}

// Standard event schema helpers.

func Component(name string) Field              { return String("component", name) }
func Operation(name string) Field              { return String("operation", name) }
func Event(name string) Field                  { return String("event", name) }
func Correlation(id string) Field              { return String("correlation_id", id) }
func Causation(id string) Field                { return String("causation_id", id) }
func ErrorCategoryField(c ErrorCategory) Field { return String("error_category", string(c)) }
func Retryable(v bool) Field                   { return Bool("retryable", v) }
func Result(outcome string) Field              { return String("result", outcome) }

type nopLogger struct{}

// Nop returns a no-op Logger that performs no allocations or I/O.
func Nop() Logger { return nopLogger{} }

func (nopLogger) Debug(string, ...Field) {}
func (nopLogger) Info(string, ...Field)  {}
func (nopLogger) Warn(string, ...Field)  {}
func (nopLogger) Error(string, ...Field) {}
func (n nopLogger) With(...Field) Logger { return n }
func (n nopLogger) Named(string) Logger  { return n }
