package idempotency

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var (
	ErrClaimNotOwned             = errors.New("idempotency claim is no longer owned")
	ErrClaimLifecycleUnsupported = errors.New("idempotency repository does not support claim lifecycle")
)

type claimStatus uint8

const (
	claimStatusAccepted claimStatus = iota
	claimStatusProcessing
	claimStatusReserved
)

// ClaimLifecycleRepository extends durable idempotency with a two-phase claim.
// A processing claim blocks concurrent duplicates, but can be released if the
// caller fails before its work is accepted by the execution engine.
type ClaimLifecycleRepository interface {
	BeginClaim(context.Context, string, string, time.Time, time.Time) (bool, error)
	AcceptClaim(context.Context, string, string, time.Time) (bool, error)
	ReleaseClaim(context.Context, string, string) (bool, error)
}

// ClaimReservationRepository persists an execution fence before submitting
// work. A reserved claim may be released only by its generation owner when
// admission is known to have failed.
type ClaimReservationRepository interface {
	ReserveClaim(context.Context, string, string, time.Time) (bool, error)
}

// ExecutionClaim represents ownership of one processing idempotency claim.
// Accept commits it as processed; Release makes the key immediately retryable.
type ExecutionClaim struct {
	manager *Manager
	key     string
	token   string

	mu   sync.Mutex
	done bool
}

// Begin creates a processing claim. Duplicate processing and accepted claims
// are both rejected until they expire or the processing owner releases them.
func (m *Manager) Begin(ctx context.Context, key string, ttl time.Duration) (*ExecutionClaim, bool, error) {
	if m == nil {
		return nil, false, errors.New("idempotency manager is nil")
	}
	cleanKey := strings.TrimSpace(key)
	if cleanKey == "" {
		return nil, false, errors.New("idempotency key cannot be empty")
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	token, err := newClaimToken()
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	expiresAt := now.Add(ttl)

	if m.repo != nil {
		repo, ok := m.repo.(ClaimLifecycleRepository)
		if !ok {
			return nil, false, ErrClaimLifecycleUnsupported
		}
		claimed, err := repo.BeginClaim(ctx, cleanKey, token, now, expiresAt)
		if err != nil || !claimed {
			return nil, claimed, err
		}
		m.signalCleanup()
		return &ExecutionClaim{manager: m, key: cleanKey, token: token}, true, nil
	}

	m.mu.Lock()
	if current, exists := m.entries[cleanKey]; exists && now.Before(current.expiresAt) {
		m.mu.Unlock()
		return nil, false, nil
	}
	m.entries[cleanKey] = entry{
		key:       cleanKey,
		token:     token,
		status:    claimStatusProcessing,
		createdAt: now,
		expiresAt: expiresAt,
	}
	m.mu.Unlock()
	m.signalCleanup()
	return &ExecutionClaim{manager: m, key: cleanKey, token: token}, true, nil
}

// Accept marks a processing claim as successfully admitted and extends its
// duplicate-suppression window from the acceptance point.
func (c *ExecutionClaim) Accept(ctx context.Context, ttl time.Duration) error {
	if c == nil || c.manager == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}

	m := c.manager
	expiresAt := time.Now().UTC().Add(ttl)
	if m.repo != nil {
		repo, ok := m.repo.(ClaimLifecycleRepository)
		if !ok {
			return ErrClaimLifecycleUnsupported
		}
		accepted, err := repo.AcceptClaim(ctx, c.key, c.token, expiresAt)
		if err != nil {
			return err
		}
		if !accepted {
			return ErrClaimNotOwned
		}
		c.done = true
		m.signalCleanup()
		return nil
	}

	m.mu.Lock()
	current, exists := m.entries[c.key]
	if !exists || current.status != claimStatusProcessing || current.token != c.token {
		m.mu.Unlock()
		return ErrClaimNotOwned
	}
	current.status = claimStatusAccepted
	current.token = ""
	current.expiresAt = expiresAt
	m.entries[c.key] = current
	m.mu.Unlock()
	c.done = true
	m.signalCleanup()
	return nil
}

// Reserve persists the duplicate-suppression fence before execution admission.
// A failed reservation must never be followed by task submission.
func (c *ExecutionClaim) Reserve(ctx context.Context, ttl time.Duration) error {
	if c == nil || c.manager == nil {
		return nil
	}
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return ErrClaimNotOwned
	}
	m := c.manager
	expiresAt := time.Now().UTC().Add(ttl)
	if m.repo != nil {
		repo, ok := m.repo.(ClaimReservationRepository)
		if !ok {
			return ErrClaimLifecycleUnsupported
		}
		reserved, err := repo.ReserveClaim(ctx, c.key, c.token, expiresAt)
		if err != nil {
			return err
		}
		if !reserved {
			return ErrClaimNotOwned
		}
		m.signalCleanup()
		return nil
	}
	m.mu.Lock()
	current, exists := m.entries[c.key]
	if !exists || current.status != claimStatusProcessing || current.token != c.token {
		m.mu.Unlock()
		return ErrClaimNotOwned
	}
	current.status = claimStatusReserved
	current.expiresAt = expiresAt
	m.entries[c.key] = current
	m.mu.Unlock()
	m.signalCleanup()
	return nil
}

// Release removes a processing claim only while this handle still owns it.
// Accepted claims are never removed by Release.
func (c *ExecutionClaim) Release(ctx context.Context) error {
	if c == nil || c.manager == nil {
		return nil
	}

	c.mu.Lock()
	defer c.mu.Unlock()
	if c.done {
		return nil
	}

	m := c.manager
	if m.repo != nil {
		repo, ok := m.repo.(ClaimLifecycleRepository)
		if !ok {
			return ErrClaimLifecycleUnsupported
		}
		released, err := repo.ReleaseClaim(ctx, c.key, c.token)
		if err != nil {
			return err
		}
		if !released {
			return ErrClaimNotOwned
		}
		c.done = true
		m.signalCleanup()
		return nil
	}

	m.mu.Lock()
	current, exists := m.entries[c.key]
	if !exists || (current.status != claimStatusProcessing && current.status != claimStatusReserved) || current.token != c.token {
		m.mu.Unlock()
		return ErrClaimNotOwned
	}
	delete(m.entries, c.key)
	m.mu.Unlock()
	c.done = true
	m.signalCleanup()
	return nil
}

func newClaimToken() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", fmt.Errorf("generate idempotency claim token: %w", err)
	}
	return hex.EncodeToString(raw[:]), nil
}
