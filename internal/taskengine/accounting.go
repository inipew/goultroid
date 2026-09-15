package taskengine

// Bounded memory accounting for the execution coordinator (Phase B).
//
// The admission controller bounds queued-but-undispatched bytes per pool.
// This file bounds everything the admission view does NOT cover:
//
//   - in-flight payloads (released from the admission queue at dispatch but
//     still owned by the engine until the record is evicted),
//   - terminal results retained for Snapshot/Result,
//   - completion-callback delivery fan-out.
//
// The engine tracks retainedBytes (runLoop-owned, single writer): charged at
// admission (payload + fixed per-record overhead) and at completion (bounded
// output + truncated failure text), released only at eviction. Admission is
// rejected with tasks.ErrRetainedBudget once the cap would be exceeded, so a
// stalled persistence layer (Phase C CommitPending) surfaces as backpressure,
// not OOM.
//
// Measurement is exact for []byte/string payloads and outputs. Arbitrary `any`
// values that are neither are charged a flat estimate: closures and opaque
// references cannot be measured, and fully closing that hole requires the
// value-payload/ref redesign tracked as a follow-up (see boundResult).

const (
	// taskOverheadBytes is the fixed retained charge per admitted record
	// (spec metadata, scope/occurrence copies, ticket, timestamps).
	taskOverheadBytes int64 = 512
	// jobRefBytes charges the fixed-size durable occurrence reference.
	jobRefBytes int64 = 128
	// opaqueValueBytes is the flat charge for non-nil, non-measurable values.
	opaqueValueBytes int64 = 256

	// DefaultMaxRetainedBytes caps admitted-but-unevicted memory per engine.
	DefaultMaxRetainedBytes int64 = 256 << 20 // 256 MB
	// DefaultMaxOutputBytes caps a single TaskResult.Output payload.
	DefaultMaxOutputBytes int64 = 1 << 20 // 1 MB
	// DefaultMaxFailureBytes caps a single stored failure message/detail.
	DefaultMaxFailureBytes int = 4096
	// DefaultDeliveryConcurrency is the fixed completion-callback worker count.
	DefaultDeliveryConcurrency = 4
)

// measurePayload returns the accountable byte size of a WorkSpec.Input value.
func measurePayload(input any) int64 {
	switch v := input.(type) {
	case nil:
		return 0
	case []byte:
		return int64(len(v))
	case string:
		return int64(len(v))
	default:
		return opaqueValueBytes
	}
}

// measureOutput returns the accountable byte size of a TaskResult.Output value.
func measureOutput(output any) int64 {
	switch v := output.(type) {
	case nil:
		return 0
	case []byte:
		return int64(len(v))
	case string:
		return int64(len(v))
	default:
		return opaqueValueBytes
	}
}

// capOutput enforces maxBytes on a result output: []byte/string are truncated,
// other over-budget values are dropped to nil. It returns the stored value and
// its accountable size.
func capOutput(output any, maxBytes int64) (any, int64) {
	if maxBytes <= 0 || output == nil {
		return output, measureOutput(output)
	}
	switch v := output.(type) {
	case []byte:
		if int64(len(v)) <= maxBytes {
			return output, int64(len(v))
		}
		truncated := append([]byte(nil), v[:maxBytes]...)
		return truncated, maxBytes
	case string:
		if int64(len(v)) <= maxBytes {
			return output, int64(len(v))
		}
		return v[:maxBytes], maxBytes
	default:
		return nil, 0
	}
}

// truncateField caps a failure text field, marking truncation explicitly so
// operators can distinguish a clipped message from a complete one.
func truncateField(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	if max <= len("...(truncated)") {
		return s[:max]
	}
	return s[:max-len("...(truncated)")] + "...(truncated)"
}
