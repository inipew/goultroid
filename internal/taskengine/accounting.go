package taskengine

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/inipew/goultroid/internal/tasks"
)

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
// Accepted WorkSpec.Input values are deliberately restricted to immutable
// scalar/string values and byte slices/arrays. Byte slices are copied before
// the admission decision is published. Mutable/opaque object graphs are
// rejected instead of receiving an unverifiable flat estimate. This makes the
// retained-memory charge match the payload the engine actually owns.

const (
	// taskOverheadBytes is the fixed retained charge per admitted record
	// (spec metadata, scope/occurrence copies, ticket, timestamps).
	taskOverheadBytes int64 = 512
	// jobRefBytes charges the fixed-size durable occurrence reference.
	jobRefBytes int64 = 128
	// resourceRequirementBytes conservatively charges the backing storage of
	// each copied resource requirement; variable-size names are charged below.
	resourceRequirementBytes int64 = 32

	// DefaultMaxRetainedBytes caps admitted-but-unevicted memory per engine.
	DefaultMaxRetainedBytes int64 = 256 << 20 // 256 MB
	// DefaultMaxOutputBytes caps a single TaskResult.Output payload.
	DefaultMaxOutputBytes int64 = 1 << 20 // 1 MB
	// DefaultMaxFailureBytes caps a single stored failure message/detail.
	DefaultMaxFailureBytes int = 4096
	// DefaultDeliveryConcurrency is the fixed completion-callback worker count.
	DefaultDeliveryConcurrency = 4
)

// workSpecMetadataBytes charges variable-size metadata and copied backing
// storage that remains reachable from an admitted WorkSpec. Input is accounted
// separately by payloadSize.
func workSpecMetadataBytes(spec tasks.WorkSpec) int64 {
	bytes := int64(
		len(spec.ID) +
			len(spec.Scope.Owner) +
			len(spec.QuotaOwner) +
			len(spec.Pool) +
			len(spec.Class) +
			len(spec.OrderingKey) +
			len(spec.HandlerRef),
	)
	if spec.Job != nil {
		bytes += jobRefBytes + int64(
			len(spec.Job.JobID)+
				len(spec.Job.OccurrenceID)+
				len(spec.Job.AttemptID),
		)
	}
	bytes += int64(len(spec.Resources)) * resourceRequirementBytes
	for _, requirement := range spec.Resources {
		bytes += int64(len(requirement.Name))
	}
	return bytes
}

// freezeWorkSpecMetadata gives the engine exact-size string/backing ownership
// after the retained-memory budget has accepted the measured charge.
func freezeWorkSpecMetadata(spec *tasks.WorkSpec) {
	if spec == nil {
		return
	}
	spec.ID = tasks.TaskID(strings.Clone(string(spec.ID)))
	spec.Scope.Owner = strings.Clone(spec.Scope.Owner)
	spec.QuotaOwner = tasks.OwnerID(strings.Clone(string(spec.QuotaOwner)))
	spec.Pool = tasks.PoolID(strings.Clone(string(spec.Pool)))
	spec.Class = tasks.PriorityClass(strings.Clone(string(spec.Class)))
	spec.OrderingKey = strings.Clone(spec.OrderingKey)
	spec.HandlerRef = strings.Clone(spec.HandlerRef)
	if spec.Job != nil {
		ref := *spec.Job
		ref.JobID = strings.Clone(ref.JobID)
		ref.OccurrenceID = tasks.OccurrenceID(strings.Clone(string(ref.OccurrenceID)))
		ref.AttemptID = tasks.AttemptID(strings.Clone(string(ref.AttemptID)))
		spec.Job = &ref
	}
	if len(spec.Resources) == 0 {
		spec.Resources = nil
		return
	}
	resources := make([]tasks.ResourceRequirement, len(spec.Resources))
	copy(resources, spec.Resources)
	for index := range resources {
		resources[index].Name = strings.Clone(resources[index].Name)
	}
	spec.Resources = resources
}

// payloadSize validates that Input has deterministic immutable/copy semantics
// and returns its variable-size retained charge without allocating a copy.
func payloadSize(input any) (int64, error) {
	if input == nil {
		return 0, nil
	}
	rv := reflect.ValueOf(input)
	rt := rv.Type()
	switch rv.Kind() {
	case reflect.String:
		return int64(rv.Len()), nil
	case reflect.Slice, reflect.Array:
		if rt.Elem().Kind() == reflect.Uint8 {
			return int64(rv.Len()), nil
		}
	case reflect.Bool,
		reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64, reflect.Uintptr,
		reflect.Float32, reflect.Float64,
		reflect.Complex64, reflect.Complex128:
		return int64(rt.Size()), nil
	}
	return 0, fmt.Errorf("%w: %T", tasks.ErrUnsupportedPayload, input)
}

// freezePayload returns an immutable engine-owned representation. It is called
// only after payload/admission budgets have accepted the measured size, so an
// oversized []byte cannot force a large defensive copy before rejection.
func freezePayload(input any) (any, error) {
	if _, err := payloadSize(input); err != nil {
		return nil, err
	}
	if input == nil {
		return nil, nil
	}
	rv := reflect.ValueOf(input)
	rt := rv.Type()
	if rv.Kind() == reflect.String {
		cloned := strings.Clone(rv.String())
		if rt == reflect.TypeOf("") {
			return cloned, nil
		}
		copyValue := reflect.New(rt).Elem()
		copyValue.SetString(cloned)
		return copyValue.Interface(), nil
	}
	if rv.Kind() == reflect.Slice && rt.Elem().Kind() == reflect.Uint8 {
		copyValue := reflect.MakeSlice(rt, rv.Len(), rv.Len())
		reflect.Copy(copyValue, rv)
		return copyValue.Interface(), nil
	}
	// Strings and scalar/array values are immutable or copied-by-value in safe
	// Go, so retaining the interface value cannot be mutated through the caller.
	return input, nil
}

// capOutput enforces maxBytes on a result output: []byte/string are copied or
// truncated; other values are dropped to nil. It returns the stored value and
// its accountable size.
func capOutput(output any, maxBytes int64) (any, int64) {
	if output == nil {
		return nil, 0
	}
	switch v := output.(type) {
	case []byte:
		limit := int64(len(v))
		if maxBytes > 0 && limit > maxBytes {
			limit = maxBytes
		}
		stored := append([]byte(nil), v[:int(limit)]...)
		return stored, limit
	case string:
		if maxBytes > 0 && int64(len(v)) > maxBytes {
			return strings.Clone(v[:maxBytes]), maxBytes
		}
		return strings.Clone(v), int64(len(v))
	default:
		// Opaque output has no immutable/accountable ownership contract. Drop it
		// rather than retaining a caller-owned mutable object graph.
		return nil, 0
	}
}

// truncateField caps a failure text field, marking truncation explicitly so
// operators can distinguish a clipped message from a complete one.
func truncateField(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return strings.Clone(s)
	}
	if max <= len("...(truncated)") {
		return strings.Clone(s[:max])
	}
	return strings.Clone(s[:max-len("...(truncated)")]) + "...(truncated)"
}
