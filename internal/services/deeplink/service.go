package deeplink

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inipew/goultroid/internal/execution"
	"github.com/inipew/goultroid/internal/presentation"
	"github.com/inipew/goultroid/internal/runtime"
	"go.uber.org/zap"
)

// Service coordinates issuing, validating, and consuming deep link tokens.
type Service struct {
	repo               Repository
	logger             *zap.Logger
	botUsername        string
	prunePeriod        time.Duration
	activeGenerationFn func(owner string) (uint64, bool)

	mu     sync.RWMutex
	cancel context.CancelFunc
	done   chan struct{}
}

var _ runtime.Component = (*Service)(nil)
var _ presentation.DeepLinkIssuer = (*Service)(nil)

// NewService creates an initialized deep link Service.
func NewService(repo Repository, botUsername string, logger *zap.Logger) *Service {
	if logger == nil {
		logger = zap.NewNop()
	}
	return &Service{
		repo:        repo,
		logger:      logger.Named("deeplink"),
		botUsername: botUsername,
		prunePeriod: DefaultPrunePeriod,
	}
}

// SetAssistantUsername updates the bot username used in issued URLs.
func (s *Service) SetAssistantUsername(u string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.botUsername = u
}

// SetActiveGenerationResolver attaches a callback to verify whether a plugin generation is still current.
func (s *Service) SetActiveGenerationResolver(fn func(owner string) (uint64, bool)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activeGenerationFn = fn
}

func (s *Service) Name() string {
	return "deeplink"
}

func (s *Service) Dependencies() []string {
	return []string{"database"}
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		return nil
	}

	loopCtx, cancel := context.WithCancel(context.Background())
	s.cancel = cancel
	s.done = make(chan struct{})

	go s.pruneLoop(loopCtx)
	return nil
}

func (s *Service) Stop(ctx context.Context) error {
	s.mu.Lock()
	cancel := s.cancel
	done := s.done
	s.cancel = nil
	s.done = nil
	s.mu.Unlock()

	if cancel == nil {
		return nil
	}

	cancel()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return nil
}

func (s *Service) Health(ctx context.Context) runtime.ComponentHealth {
	return runtime.ComponentHealth{Status: runtime.HealthHealthy}
}

func (s *Service) pruneLoop(ctx context.Context) {
	defer close(s.done)
	ticker := time.NewTicker(s.prunePeriod)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			olderThan := time.Now().Add(-24 * time.Hour)
			if pruned, err := s.repo.Prune(ctx, olderThan, 500); err != nil {
				s.logger.Warn("periodic deep link prune error", zap.Error(err))
			} else if pruned > 0 {
				s.logger.Debug("pruned expired/consumed deep links", zap.Int64("count", pruned))
			}
		}
	}
}

// Issue generates a new secure deep link token and persists its SHA-256 hash.
func (s *Service) Issue(ctx context.Context, req IssueRequest) (string, error) {
	if req.Purpose != "" && req.Purpose != PurposeOpenScreen && req.Purpose != PurposeResumeFlow && req.Purpose != PurposeStoredFile {
		return "", ErrInvalidPurpose
	}
	if req.Purpose == "" {
		req.Purpose = PurposeOpenScreen
	}
	if req.Purpose == PurposeOpenScreen && (req.Screen.Namespace == "" || req.Screen.Name == "") {
		return "", ErrInvalidScreenKey
	}
	if len(req.Payload) > MaxPayloadSize {
		return "", ErrPayloadTooLarge
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	} else if ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}

	rawToken, tokenHash, err := GenerateToken()
	if err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}

	now := time.Now()
	rec := Record{
		ID:             uuid.NewString(),
		TokenHash:      tokenHash,
		Purpose:        req.Purpose,
		Owner:          req.Owner,
		Generation:     req.Generation,
		UserID:         req.UserID,
		SourceChatID:   req.SourceChatID,
		Screen:         req.Screen,
		PayloadType:    req.PayloadType,
		PayloadVersion: req.PayloadVersion,
		Payload:        req.Payload,
		SingleUse:      req.SingleUse,
		IssuedAt:       now,
		ExpiresAt:      now.Add(ttl),
	}

	if err := s.repo.Create(ctx, rec); err != nil {
		return "", fmt.Errorf("persist token: %w", err)
	}

	return rawToken, nil
}

// IssueStartLink satisfies presentation.DeepLinkIssuer to generate full https://t.me/<bot>?start=<token> URLs.
func (s *Service) IssueStartLink(ctx context.Context, req presentation.DeepLinkRequest) (string, time.Time, error) {
	s.mu.RLock()
	username := s.botUsername
	s.mu.RUnlock()

	if username == "" {
		username = "GoUltroidBot"
	}

	ttl := req.TTL
	if ttl <= 0 {
		ttl = DefaultTokenTTL
	}
	if ttl > MaxTokenTTL {
		ttl = MaxTokenTTL
	}

	token, err := s.Issue(ctx, IssueRequest{
		Purpose:      PurposeOpenScreen,
		Owner:        req.Owner,
		Generation:   req.Generation,
		UserID:       req.UserID,
		SourceChatID: req.SourceChatID,
		Screen:       req.Screen,
		PayloadType:  req.PayloadType,
		Payload:      req.Payload,
		SingleUse:    true,
		TTL:          ttl,
	})
	if err != nil {
		return "", time.Time{}, err
	}

	expiresAt := time.Now().Add(ttl)
	link := fmt.Sprintf("https://t.me/%s?start=%s", username, token)
	return link, expiresAt, nil
}

// Peek inspects a token without redeeming it.
func (s *Service) Peek(ctx context.Context, token string, actor execution.Actor) (Claim, error) {
	if token == "" {
		return Claim{}, ErrTokenNotFound
	}
	hash := HashToken(token)
	rec, err := s.repo.FindByHash(ctx, hash)
	if err != nil {
		return Claim{}, err
	}

	if time.Now().After(rec.ExpiresAt) {
		return Claim{}, ErrTokenExpired
	}
	if rec.SingleUse && rec.ConsumedAt != nil {
		return Claim{}, ErrTokenConsumed
	}
	if rec.UserID != 0 {
		if actor.UserID == 0 || (rec.UserID != actor.UserID && !actor.IsOwner) {
			return Claim{}, ErrTokenScopeMismatch
		}
	}

	s.mu.RLock()
	fn := s.activeGenerationFn
	s.mu.RUnlock()
	if fn != nil && rec.Owner != "" {
		activeGen, ok := fn(rec.Owner)
		if !ok || (rec.Generation != 0 && activeGen != rec.Generation) {
			return Claim{}, ErrTokenGenerationStale
		}
	}

	return rec.ToClaim(), nil
}

// Consume atomically marks a single-use token as redeemed.
func (s *Service) Consume(ctx context.Context, token string, actor execution.Actor) (Claim, error) {
	if token == "" {
		return Claim{}, ErrTokenNotFound
	}
	hash := HashToken(token)
	now := time.Now()

	s.mu.RLock()
	fn := s.activeGenerationFn
	s.mu.RUnlock()

	validate := func(rec Record) error {
		if rec.UserID != 0 {
			if actor.UserID == 0 || (rec.UserID != actor.UserID && !actor.IsOwner) {
				return ErrTokenScopeMismatch
			}
		}
		if fn != nil && rec.Owner != "" {
			activeGen, ok := fn(rec.Owner)
			if !ok || (rec.Generation != 0 && activeGen != rec.Generation) {
				return ErrTokenGenerationStale
			}
		}
		return nil
	}

	rec, err := s.repo.ConsumeAtomic(ctx, hash, actor.UserID, now, validate)
	if err != nil {
		return Claim{}, err
	}

	return rec.ToClaim(), nil
}

// RevokeOwner invalidates all tokens associated with a given owner/generation.
func (s *Service) RevokeOwner(ctx context.Context, owner string, generation uint64) (int64, error) {
	return s.repo.RevokeOwner(ctx, owner, generation)
}
