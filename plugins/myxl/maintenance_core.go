package myxl

import (
	"context"
	"errors"
	"sort"
	"strings"
	"time"
)

type tokenEvalDecision int

const (
	evalSkipInvalid tokenEvalDecision = iota
	evalMissingRefreshToken
	evalAlreadyFresh
	evalNeedsRefreshUnknownExpiry
	evalNeedsRefreshWithinWindow
	evalNeedsRefreshExpired
)

func isTokenExpiryKnown(expiresAt time.Time) bool {
	return !expiresAt.IsZero() && expiresAt.After(time.Unix(0, 0))
}

// evaluateAccountToken decides whether an account requires refresh using the
// same expiry semantics as Client.EnsureFreshTokenWithin.
func evaluateAccountToken(acc *Account, now time.Time, window time.Duration) tokenEvalDecision {
	if acc == nil || strings.TrimSpace(acc.MSISDN) == "" {
		return evalSkipInvalid
	}
	if strings.TrimSpace(acc.RefreshToken) == "" && strings.TrimSpace(acc.SubscriberID) == "" {
		return evalMissingRefreshToken
	}
	if !isTokenExpiryKnown(acc.TokenExpiresAt) {
		return evalNeedsRefreshUnknownExpiry
	}
	if !acc.TokenExpiresAt.After(now) {
		return evalNeedsRefreshExpired
	}
	if window < DefaultTokenRefreshSkew {
		window = DefaultTokenRefreshSkew
	}
	if isTokenFreshAt(acc.TokenExpiresAt, now, window) {
		return evalAlreadyFresh
	}
	return evalNeedsRefreshWithinWindow
}

type maintenanceCandidate struct {
	account  *Account
	decision tokenEvalDecision
	priority int
}

func priorityForDecision(d tokenEvalDecision) int {
	switch d {
	case evalNeedsRefreshExpired:
		return 0
	case evalNeedsRefreshWithinWindow:
		return 1
	case evalNeedsRefreshUnknownExpiry:
		return 2
	default:
		return 3
	}
}

func selectMaintenanceAccounts(accounts []*Account, cfg TokenMaintenanceConfig, now time.Time) []maintenanceCandidate {
	var selectedSet map[string]struct{}
	if cfg.AccountScope == AccountScopeSelected {
		selectedSet = make(map[string]struct{}, len(cfg.SelectedMSISDNs))
		for _, m := range cfg.SelectedMSISDNs {
			selectedSet[strings.TrimSpace(m)] = struct{}{}
		}
	}

	candidates := make([]maintenanceCandidate, 0, len(accounts))
	for _, acc := range accounts {
		if acc == nil {
			continue
		}
		switch cfg.AccountScope {
		case AccountScopeActive:
			if !acc.IsActive {
				continue
			}
		case AccountScopeSelected:
			if _, ok := selectedSet[acc.MSISDN]; !ok {
				continue
			}
		}
		d := evaluateAccountToken(acc, now, cfg.RefreshWindow)
		candidates = append(candidates, maintenanceCandidate{
			account:  acc,
			decision: d,
			priority: priorityForDecision(d),
		})
	}

	sort.SliceStable(candidates, func(i, j int) bool {
		if candidates[i].priority != candidates[j].priority {
			return candidates[i].priority < candidates[j].priority
		}
		return candidates[i].account.TokenExpiresAt.Before(candidates[j].account.TokenExpiresAt)
	})
	return candidates
}

func classifyRefreshError(err error) AccountMaintenanceStatus {
	switch {
	case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
		return StatusCancelled
	case errors.Is(err, ErrRefreshMissingToken):
		return StatusMissingRefreshToken
	case errors.Is(err, ErrRefreshPersistence):
		return StatusPersistenceError
	case errors.Is(err, ErrRefreshRateLimited):
		return StatusRateLimited
	case errors.Is(err, ErrRefreshAuthRejected):
		return StatusAuthenticationRejected
	default:
		return StatusNetworkError
	}
}

// runTokenMaintenanceCycle executes one sequential, expiry-driven maintenance pass
// and persists a bounded MaintenanceRunSummary without storing tokens or full MSISDNs.
func runTokenMaintenanceCycle(
	ctx context.Context,
	repo Repository,
	client *Client,
	cfg TokenMaintenanceConfig,
	triggerSource string,
) (*MaintenanceRunSummary, []AccountMaintenanceOutcome, error) {
	if repo == nil {
		return nil, nil, errors.New("repository is nil")
	}
	if client == nil {
		return nil, nil, errors.New("myxl client is nil")
	}

	startedAt := time.Now().UTC()
	accounts, err := repo.List(ctx)
	if err != nil {
		return nil, nil, err
	}

	candidates := selectMaintenanceAccounts(accounts, cfg, startedAt)
	outcomes := make([]AccountMaintenanceOutcome, 0, len(candidates))
	summary := &MaintenanceRunSummary{
		TriggerSource: triggerSource,
		StartedAt:     startedAt,
		TotalAccounts: len(candidates),
	}

	refreshAttempts := 0
	for i, cand := range candidates {
		if ctx.Err() != nil {
			for j := i; j < len(candidates); j++ {
				outcomes = append(outcomes, AccountMaintenanceOutcome{
					MSISDN: candidates[j].account.MSISDN,
					Status: StatusCancelled,
				})
				summary.CancelledCount++
			}
			break
		}

		switch cand.decision {
		case evalSkipInvalid:
			outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: cand.account.MSISDN, Status: StatusSkipped})
			summary.SkippedCount++
		case evalMissingRefreshToken:
			outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: cand.account.MSISDN, Status: StatusMissingRefreshToken})
			summary.SkippedCount++
		case evalAlreadyFresh:
			outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: cand.account.MSISDN, Status: StatusAlreadyFresh})
			summary.AlreadyFreshCount++
		case evalNeedsRefreshExpired, evalNeedsRefreshWithinWindow, evalNeedsRefreshUnknownExpiry:
			if refreshAttempts >= MaxAccountsPerCycle {
				outcomes = append(outcomes, AccountMaintenanceOutcome{
					MSISDN: cand.account.MSISDN,
					Status: StatusSkipped,
					Detail: "max accounts per cycle reached",
				})
				summary.SkippedCount++
				continue
			}
			refreshAttempts++

			// Verify account still exists before refreshing (handles concurrent deletion).
			current, getErr := repo.GetByMSISDN(ctx, cand.account.MSISDN)
			if getErr != nil {
				status := classifyRefreshError(getErr)
				if status == StatusNetworkError {
					status = StatusPersistenceError
				}
				outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: cand.account.MSISDN, Status: status})
				summary.FailedCount++
				continue
			}
			if current == nil {
				outcomes = append(outcomes, AccountMaintenanceOutcome{
					MSISDN: cand.account.MSISDN,
					Status: StatusSkipped,
					Detail: "account deleted",
				})
				summary.SkippedCount++
				continue
			}

			if err := client.EnsureFreshTokenWithin(ctx, current, cfg.RefreshWindow); err != nil {
				status := classifyRefreshError(err)
				if status == StatusCancelled {
					outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: current.MSISDN, Status: StatusCancelled})
					summary.CancelledCount++
					continue
				}
				outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: current.MSISDN, Status: status})
				summary.FailedCount++
				continue
			}
			outcomes = append(outcomes, AccountMaintenanceOutcome{MSISDN: current.MSISDN, Status: StatusRefreshed})
			summary.RefreshedCount++
		}
	}

	completedAt := time.Now().UTC()
	summary.CompletedAt = completedAt
	summary.Duration = completedAt.Sub(startedAt)
	switch {
	case summary.CancelledCount > 0:
		summary.Status = RunStatusCancelled
	case summary.FailedCount > 0 && summary.RefreshedCount == 0 && summary.AlreadyFreshCount == 0:
		summary.Status = RunStatusFailed
	case summary.FailedCount > 0:
		summary.Status = RunStatusPartialFailure
	default:
		summary.Status = RunStatusCompleted
	}

	if err := repo.RecordTokenMaintenanceRun(ctx, summary); err != nil {
		return summary, outcomes, err
	}
	return summary, outcomes, nil
}
