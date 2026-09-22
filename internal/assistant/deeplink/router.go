package deeplink

import (
	"context"
	"crypto/rand"
	"errors"
	"encoding/base64"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/inipew/goultroid/internal/tasks"
)

const (
	tokenRandomBytes = 16
	pruneBatch       = 64
)

type providerEntry struct {
	provider Provider
	token    uint64
}

// Registration owns one provider generation.
type Registration struct {
	router *Router
	kind   string
	token  uint64
	once   sync.Once
}

// Prepared freezes token/provider admission state without consuming single-use
// authority. ExecutePrepared performs the atomic claim after TaskEngine admits.
type Prepared struct {
	record        Token
	target        PreparedTarget
	provider      Provider
	providerToken uint64
}

func (p Prepared) Scope() tasks.ScopeIdentity {
	return p.target.Scope
}

func (p Prepared) Resources() []tasks.ResourceRequirement {
	return append([]tasks.ResourceRequirement(nil), p.target.Resources...)
}

func (p Prepared) Kind() string { return p.record.Kind }

// Router is the canonical typed /start payload runtime.
type Router struct {
	repo Repository
	now  func() time.Time

	mu          sync.RWMutex
	issueMu     sync.Mutex
	providers   map[string]providerEntry
	next        uint64
	maxRetained int
}

func NewRouter(repo Repository) *Router {
	return &Router{
		repo: repo, now: time.Now, providers: make(map[string]providerEntry),
		maxRetained: MaxRetainedTokens,
	}
}

func normalizeKind(kind string) string {
	return strings.ToLower(strings.TrimSpace(kind))
}

func validKind(kind string) bool {
	if kind == "" || len(kind) > MaxKindBytes {
		return false
	}
	for _, r := range kind {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') || r == '_' || r == '-' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func LooksLikeToken(raw string) bool {
	return strings.HasPrefix(strings.TrimSpace(raw), TokenVersion+"_")
}

func normalizeToken(raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if !LooksLikeToken(raw) {
		return "", ErrInvalidToken
	}
	encoded := strings.TrimPrefix(raw, TokenVersion+"_")
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(decoded) != tokenRandomBytes {
		return "", ErrInvalidToken
	}
	return raw, nil
}

func newTokenID() (string, error) {
	var raw [tokenRandomBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate deep-link token: %w", err)
	}
	return TokenVersion + "_" + base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func newClaimID() (string, error) {
	var raw [tokenRandomBytes]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate deep-link claim: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw[:]), nil
}

func claimCleanupContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	return context.WithTimeout(context.WithoutCancel(parent), 2*time.Second)
}

func (r *Router) Register(kind string, provider Provider) (*Registration, error) {
	if r == nil || provider == nil {
		return nil, ErrProviderUnavailable
	}
	kind = normalizeKind(kind)
	if !validKind(kind) {
		return nil, ErrInvalidKind
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.providers[kind]; ok {
		return nil, fmt.Errorf("%w: %s", ErrProviderRegistered, kind)
	}
	r.next++
	r.providers[kind] = providerEntry{provider: provider, token: r.next}
	return &Registration{router: r, kind: kind, token: r.next}, nil
}

func (r *Registration) Close() {
	if r == nil || r.router == nil {
		return
	}
	r.once.Do(func() {
		r.router.mu.Lock()
		current, ok := r.router.providers[r.kind]
		if ok && current.token == r.token {
			delete(r.router.providers, r.kind)
		}
		r.router.mu.Unlock()
	})
}

func (r *Router) Issue(ctx context.Context, request IssueRequest) (Token, error) {
	if r == nil || r.repo == nil {
		return Token{}, ErrProviderUnavailable
	}
	request.Kind = normalizeKind(request.Kind)
	if !validKind(request.Kind) {
		return Token{}, ErrInvalidKind
	}
	if strings.TrimSpace(request.Payload) == "" || len(request.Payload) > MaxPayloadBytes {
		return Token{}, ErrInvalidPayload
	}
	ttl := request.TTL
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if ttl > MaxTTL {
		return Token{}, ErrInvalidToken
	}
	r.mu.RLock()
	_, registered := r.providers[request.Kind]
	r.mu.RUnlock()
	if !registered {
		return Token{}, ErrProviderUnavailable
	}
	r.issueMu.Lock()
	defer r.issueMu.Unlock()

	now := r.now().UTC()
	if _, err := r.repo.PruneExpired(ctx, now, pruneBatch); err != nil {
		return Token{}, err
	}
	retained, err := r.repo.CountRetained(ctx)
	if err != nil {
		return Token{}, err
	}
	maxRetained := r.maxRetained
	if maxRetained <= 0 {
		maxRetained = MaxRetainedTokens
	}
	if retained >= maxRetained {
		return Token{}, ErrCapacity
	}
	for attempt := 0; attempt < 4; attempt++ {
		id, err := newTokenID()
		if err != nil {
			return Token{}, err
		}
		record := Token{
			ID: id, Kind: request.Kind, Payload: request.Payload,
			ActorID: request.ActorID, SingleUse: request.SingleUse,
			ExpiresAt: now.Add(ttl), CreatedAt: now,
		}
		if err := r.repo.Create(ctx, record); err != nil {
			if err == ErrTokenExists {
				continue
			}
			return Token{}, err
		}
		return record, nil
	}
	return Token{}, ErrTokenExists
}

func (r *Router) Prepare(ctx context.Context, rawToken string, actorID int64) (Prepared, error) {
	if r == nil || r.repo == nil {
		return Prepared{}, ErrProviderUnavailable
	}
	id, err := normalizeToken(rawToken)
	if err != nil {
		return Prepared{}, err
	}
	record, err := r.repo.Get(ctx, id)
	if err != nil {
		return Prepared{}, err
	}
	now := r.now().UTC()
	if !record.ExpiresAt.After(now) {
		return Prepared{}, ErrTokenExpired
	}
	if record.ConsumedAt != nil {
		return Prepared{}, ErrTokenConsumed
	}
	if record.ActorID != 0 && record.ActorID != actorID {
		return Prepared{}, ErrTokenUnauthorized
	}
	r.mu.RLock()
	entry, ok := r.providers[record.Kind]
	r.mu.RUnlock()
	if !ok || entry.provider == nil {
		return Prepared{}, ErrProviderUnavailable
	}
	target, err := entry.provider.Prepare(ctx, record.Payload, actorID)
	if err != nil {
		return Prepared{}, err
	}
	r.mu.RLock()
	current, currentOK := r.providers[record.Kind]
	r.mu.RUnlock()
	if !currentOK || current.token != entry.token {
		return Prepared{}, ErrProviderStale
	}
	if target.Scope.IsZero() {
		return Prepared{}, ErrProviderUnavailable
	}
	return Prepared{
		record: record, target: target, provider: entry.provider, providerToken: entry.token,
	}, nil
}

func (r *Router) ExecutePrepared(ctx context.Context, prepared Prepared, delivery Delivery) error {
	if r == nil || r.repo == nil || prepared.provider == nil || prepared.record.ID == "" {
		return ErrProviderUnavailable
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.RLock()
	current, ok := r.providers[prepared.record.Kind]
	r.mu.RUnlock()
	if !ok || current.token != prepared.providerToken {
		return ErrProviderStale
	}

	now := r.now().UTC()
	claimID := ""
	claimExpiresAt := now
	if prepared.record.SingleUse {
		var err error
		claimID, err = newClaimID()
		if err != nil {
			return err
		}
		claimExpiresAt = now.Add(ClaimLeaseTTL)
	}
	claimed, err := r.repo.Claim(
		ctx,
		prepared.record.ID,
		delivery.ActorID,
		now,
		claimID,
		claimExpiresAt,
	)
	if err != nil {
		return err
	}
	releaseClaim := func(cause error) error {
		if !prepared.record.SingleUse || claimID == "" {
			return cause
		}
		cleanupCtx, cancel := claimCleanupContext(ctx)
		defer cancel()
		if releaseErr := r.repo.ReleaseClaim(cleanupCtx, prepared.record.ID, claimID); releaseErr != nil {
			return errors.Join(cause, releaseErr)
		}
		return cause
	}
	if claimed.Kind != prepared.record.Kind ||
		claimed.Payload != prepared.record.Payload ||
		claimed.ActorID != prepared.record.ActorID ||
		claimed.SingleUse != prepared.record.SingleUse ||
		!claimed.ExpiresAt.Equal(prepared.record.ExpiresAt) {
		return releaseClaim(ErrInvalidToken)
	}

	r.mu.RLock()
	current, ok = r.providers[prepared.record.Kind]
	r.mu.RUnlock()
	if !ok || current.token != prepared.providerToken {
		return releaseClaim(ErrProviderStale)
	}

	if err := prepared.provider.Execute(ctx, prepared.target, delivery); err != nil {
		return releaseClaim(err)
	}
	if prepared.record.SingleUse {
		commitCtx, cancel := claimCleanupContext(ctx)
		defer cancel()
		if err := r.repo.CommitClaim(commitCtx, prepared.record.ID, claimID, r.now().UTC()); err != nil {
			return err
		}
	}
	return nil
}

