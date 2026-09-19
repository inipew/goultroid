package execution

import "context"

// Metadata carries cross-cutting execution capabilities without coupling
// infrastructure such as RPCExecutor to TaskEngine or JobManager types.
type Metadata struct {
	CanDurablyYield bool
}

type metadataKey struct{}

// WithMetadata attaches execution capabilities to a context.
func WithMetadata(ctx context.Context, metadata Metadata) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, metadataKey{}, metadata)
}

// MetadataFromContext returns execution capabilities previously attached to ctx.
func MetadataFromContext(ctx context.Context) (Metadata, bool) {
	if ctx == nil {
		return Metadata{}, false
	}
	metadata, ok := ctx.Value(metadataKey{}).(Metadata)
	return metadata, ok
}

// CanDurablyYield reports whether the current physical execution has a durable
// continuation owner capable of persisting a defer-and-redrive decision.
func CanDurablyYield(ctx context.Context) bool {
	metadata, ok := MetadataFromContext(ctx)
	return ok && metadata.CanDurablyYield
}
