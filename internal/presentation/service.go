package presentation

import (
	"context"
	"fmt"
	"time"
)

const (
	DefaultBuildTimeout = 2 * time.Second
)

// Service orchestrates authorization, screen building, and output validation.
type Service struct {
	registry     *Registry
	evaluator    *Evaluator
	buildTimeout time.Duration
}

// NewService creates an initialized presentation Service.
func NewService(registry *Registry, evaluator *Evaluator) *Service {
	if registry == nil {
		registry = NewRegistry()
	}
	if evaluator == nil {
		evaluator = NewEvaluator(0, nil)
	}
	return &Service{
		registry:     registry,
		evaluator:    evaluator,
		buildTimeout: DefaultBuildTimeout,
	}
}

// SetBuildTimeout configures the maximum time permitted for a Screen build.
func (s *Service) SetBuildTimeout(d time.Duration) {
	if d > 0 {
		s.buildTimeout = d
	}
}

// WithBuildTimeout configures the build timeout fluently.
func (s *Service) WithBuildTimeout(d time.Duration) *Service {
	s.SetBuildTimeout(d)
	return s
}

// Registry returns the underlying presentation Registry.
func (s *Service) Registry() *Registry {
	return s.registry
}

// Evaluator returns the policy Evaluator.
func (s *Service) Evaluator() *Evaluator {
	return s.evaluator
}

// Registration returns the registration entry for a screen key if registered.
func (s *Service) Registration(key ScreenKey) (Registration, bool) {
	return s.registry.Resolve(key)
}

// EvaluatePolicy verifies access constraints for a screen without executing its builder.
func (s *Service) EvaluatePolicy(ctx context.Context, key ScreenKey, req PolicyRequest) (Decision, error) {
	reg, ok := s.registry.Resolve(key)
	if !ok {
		return Decision{}, fmt.Errorf("%w: %s", ErrScreenNotFound, key)
	}
	decision := s.evaluator.Evaluate(ctx, reg.Policy, req)
	return decision, nil
}

// Build executes the full presentation pipeline: resolve -> authorize -> build -> validate.
func (s *Service) Build(ctx context.Context, req BuildRequest) (res BuildResult, retErr error) {
	if req.Key.IsZero() {
		return BuildResult{}, fmt.Errorf("%w: missing screen key", ErrInvalidPresentation)
	}

	reg, ok := s.registry.Resolve(req.Key)
	if !ok {
		return BuildResult{}, fmt.Errorf("%w: %s", ErrScreenNotFound, req.Key)
	}

	// 1. Authorize via policy evaluator
	policyReq := PolicyRequest{
		Actor:     req.Actor,
		Source:    req.Source,
		ChatType:  req.ChatType,
		Screen:    req.Key,
		MenuOwner: req.MenuOwner,
	}
	decision := s.evaluator.Evaluate(ctx, reg.Policy, policyReq)
	if !decision.Allowed {
		if decision.Code == DecisionDenyPrivate {
			return BuildResult{}, fmt.Errorf("%w: %s", ErrPrivateRequired, decision.SafeMessage)
		}
		return BuildResult{}, fmt.Errorf("%w: %s", ErrAccessDenied, decision.SafeMessage)
	}

	// 2. Set build deadline
	timeout := s.buildTimeout
	if timeout <= 0 {
		timeout = DefaultBuildTimeout
	}
	buildCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	// 3. Execute builder with panic boundary
	defer func() {
		if r := recover(); r != nil {
			retErr = fmt.Errorf("%w: panic during screen build: %v", ErrInvalidPresentation, r)
		}
	}()

	type buildOutcome struct {
		result BuildResult
		err    error
	}

	done := make(chan buildOutcome, 1)
	go func() {
		defer func() {
			if r := recover(); r != nil {
				done <- buildOutcome{err: fmt.Errorf("panic in builder: %v", r)}
			}
		}()
		result, err := reg.Builder.Build(buildCtx, req)
		done <- buildOutcome{result: result, err: err}
	}()

	select {
	case outcome := <-done:
		if outcome.err != nil {
			return BuildResult{}, outcome.err
		}
		res = outcome.result
	case <-buildCtx.Done():
		if ctx.Err() != nil {
			return BuildResult{}, ctx.Err()
		}
		return BuildResult{}, ErrBuildTimeout
	}

	// 4. Sync sensitivity with policy if declared
	if reg.Policy.Sensitive {
		res.Sensitivity = SensitivitySensitive
	}

	// 5. Validate output
	if err := ValidateScreen(res.Screen, res); err != nil {
		return BuildResult{}, err
	}

	return res, nil
}
