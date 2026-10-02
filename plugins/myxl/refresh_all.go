package myxl

import (
	"context"
)

type tokenRefreshResult struct {
	MSISDN string
	Err    error
}

// refreshAllTokens forces a CIAM refresh for every saved account. A rejected
// session does not prevent the remaining accounts from being attempted.
func (p *Plugin) refreshAllTokens(ctx context.Context) ([]tokenRefreshResult, error) {
	accounts, err := p.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	results := make([]tokenRefreshResult, 0, len(accounts))
	for _, acc := range accounts {
		if err := ctx.Err(); err != nil {
			return results, err
		}
		_, err := p.client.ForceRefreshToken(ctx, acc.MSISDN)
		results = append(results, tokenRefreshResult{MSISDN: acc.MSISDN, Err: err})
	}
	return results, nil
}
