package myxl

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxPurchaseOptionCodeBytes = 128
	purchaseConfirmationTTL    = 5 * time.Minute
)

var (
	ErrPurchaseIntentInvalid = errors.New("myxl: purchase intent invalid")
	ErrPurchaseQuoteChanged  = errors.New("myxl: purchase quote changed")
)

// purchaseIntentState is the only purchase confirmation state that new flows
// may retain. QuotedPrice is a consent fence, never settlement authority: the
// package is resolved again immediately before reservation/settlement.
type purchaseIntentState struct {
	MSISDN         string `json:"msisdn"`
	OptionCode     string `json:"option_code"`
	Method         string `json:"method"`
	WalletNumber   string `json:"wallet_number,omitempty"`
	QuotedPrice    int64  `json:"quoted_price"`
	OverwritePrice int64  `json:"overwrite_price,omitempty"`
	HasOverwrite   bool   `json:"has_overwrite,omitempty"`
}

type purchaseCheckoutPreview struct {
	Intent         purchaseIntentState
	PackageName    string
	CanonicalPrice int64
	EffectivePrice int64
}

type resolvedPurchase struct {
	Intent         purchaseIntentState
	Account        *Account
	Item           PurchaseItem
	PackageName    string
	CanonicalPrice int64
	EffectivePrice int64
	Overwrite      *int64
	IdempotencyKey string
}

func normalizePurchaseIntent(intent purchaseIntentState) (purchaseIntentState, error) {
	msisdn, err := NormalizeMSISDN(intent.MSISDN)
	if err != nil {
		return purchaseIntentState{}, fmt.Errorf("%w: invalid account", ErrPurchaseIntentInvalid)
	}
	intent.MSISDN = msisdn
	intent.OptionCode = strings.TrimSpace(intent.OptionCode)
	if intent.OptionCode == "" || len(intent.OptionCode) > maxPurchaseOptionCodeBytes || !utf8.ValidString(intent.OptionCode) {
		return purchaseIntentState{}, fmt.Errorf("%w: invalid option code", ErrPurchaseIntentInvalid)
	}

	method, err := normalizePurchaseMethod(intent.Method)
	if err != nil {
		return purchaseIntentState{}, err
	}
	intent.Method = method

	if intent.QuotedPrice < 0 {
		return purchaseIntentState{}, fmt.Errorf("%w: negative quoted price", ErrPurchaseIntentInvalid)
	}
	if intent.HasOverwrite {
		if intent.OverwritePrice < 0 {
			return purchaseIntentState{}, fmt.Errorf("%w: negative overwrite price", ErrPurchaseIntentInvalid)
		}
	} else {
		intent.OverwritePrice = 0
	}

	if isWalletPurchaseMethod(method) {
		wallet, err := NormalizeMSISDN(intent.WalletNumber)
		if err != nil {
			return purchaseIntentState{}, fmt.Errorf("%w: invalid wallet number", ErrPurchaseIntentInvalid)
		}
		intent.WalletNumber = wallet
	} else {
		intent.WalletNumber = ""
	}
	return intent, nil
}

func normalizePurchaseMethod(method string) (string, error) {
	method = strings.ToLower(strings.TrimSpace(method))
	method = strings.ReplaceAll(method, "-", "_")
	switch method {
	case "pulsa":
		method = "balance"
	case "decoy_pulsa":
		method = "decoy_balance"
	}
	switch method {
	case "balance", "qris", "gopay", "ovo", "dana", "shopeepay", "decoy_balance", "decoy_qris", "decoy_qris0":
		return method, nil
	default:
		return "", fmt.Errorf("%w: unsupported payment method", ErrPurchaseIntentInvalid)
	}
}

func isWalletPurchaseMethod(method string) bool {
	switch method {
	case "gopay", "ovo", "dana", "shopeepay":
		return true
	default:
		return false
	}
}

// preparePurchaseIntent resolves a preview quote and stores only the minimal
// consent intent. The token confirmation and package metadata are deliberately
// discarded after rendering.
func (p *Plugin) preparePurchaseIntent(ctx context.Context, intent purchaseIntentState) (purchaseIntentState, purchaseCheckoutPreview, error) {
	intent.QuotedPrice = 0
	normalized, err := normalizePurchaseIntent(intent)
	if err != nil {
		return purchaseIntentState{}, purchaseCheckoutPreview{}, err
	}
	resolved, err := p.resolvePurchaseDetails(ctx, normalized)
	if err != nil {
		return purchaseIntentState{}, purchaseCheckoutPreview{}, err
	}
	normalized.QuotedPrice = resolved.CanonicalPrice
	resolved.Intent = normalized
	resolved.EffectivePrice = normalized.QuotedPrice
	if normalized.HasOverwrite {
		resolved.EffectivePrice = normalized.OverwritePrice
	}
	return normalized, purchaseCheckoutPreview{
		Intent:         normalized,
		PackageName:    resolved.PackageName,
		CanonicalPrice: resolved.CanonicalPrice,
		EffectivePrice: resolved.EffectivePrice,
	}, nil
}

// resolvePurchaseIntent performs the fresh authority check immediately before a
// purchase can be reserved. A changed canonical price invalidates the user's
// previous consent and requires a new preview.
func (p *Plugin) resolvePurchaseIntent(ctx context.Context, intent purchaseIntentState) (resolvedPurchase, error) {
	normalized, err := normalizePurchaseIntent(intent)
	if err != nil {
		return resolvedPurchase{}, err
	}
	resolved, err := p.resolvePurchaseDetails(ctx, normalized)
	if err != nil {
		return resolvedPurchase{}, err
	}
	if resolved.CanonicalPrice != normalized.QuotedPrice {
		return resolvedPurchase{}, fmt.Errorf(
			"%w: quoted=%d current=%d",
			ErrPurchaseQuoteChanged,
			normalized.QuotedPrice,
			resolved.CanonicalPrice,
		)
	}
	resolved.Intent = normalized
	resolved.EffectivePrice = resolved.CanonicalPrice
	if normalized.HasOverwrite {
		overwrite := normalized.OverwritePrice
		resolved.Overwrite = &overwrite
		resolved.EffectivePrice = overwrite
	}
	return resolved, nil
}

func (p *Plugin) resolvePurchaseDetails(ctx context.Context, intent purchaseIntentState) (resolvedPurchase, error) {
	if p == nil || p.repo == nil || p.client == nil {
		return resolvedPurchase{}, fmt.Errorf("%w: runtime unavailable", ErrPurchaseIntentInvalid)
	}
	if ctx == nil {
		ctx = context.Background()
	}
	acc, err := p.repo.GetByMSISDN(ctx, intent.MSISDN)
	if err != nil {
		return resolvedPurchase{}, fmt.Errorf("resolve purchase account: %w", err)
	}
	if acc == nil {
		return resolvedPurchase{}, fmt.Errorf("%w: account not found", ErrPurchaseIntentInvalid)
	}

	details, err := p.client.GetPackageDetails(ctx, acc, intent.OptionCode)
	if err != nil {
		return resolvedPurchase{}, fmt.Errorf("resolve purchase package: %w", err)
	}
	if details == nil || details.PackageOption == nil {
		return resolvedPurchase{}, fmt.Errorf("%w: package details incomplete", ErrPurchaseIntentInvalid)
	}
	optionCode := strings.TrimSpace(details.PackageOption.PackageOptionCode)
	if optionCode == "" || !strings.EqualFold(optionCode, intent.OptionCode) {
		return resolvedPurchase{}, fmt.Errorf("%w: option code changed", ErrPurchaseIntentInvalid)
	}
	token := strings.TrimSpace(details.TokenConfirmation)
	if token == "" {
		return resolvedPurchase{}, fmt.Errorf("%w: confirmation token unavailable", ErrPurchaseIntentInvalid)
	}

	rawPrice := details.PackageOption.Price
	if math.IsNaN(rawPrice) || math.IsInf(rawPrice, 0) || rawPrice < 0 || rawPrice > float64(^uint64(0)>>1) || math.Trunc(rawPrice) != rawPrice {
		return resolvedPurchase{}, fmt.Errorf("%w: invalid canonical price", ErrPurchaseIntentInvalid)
	}
	price := int64(rawPrice)
	name := strings.TrimSpace(details.PackageOption.Name)
	if name == "" {
		name = intent.OptionCode
	}

	tokenHash := sha256.Sum256([]byte(token))
	key := fmt.Sprintf("%s:%s:%x", intent.MSISDN, intent.OptionCode, tokenHash[:16])
	return resolvedPurchase{
		Account: acc,
		Item: PurchaseItem{
			ItemCode:          intent.OptionCode,
			ItemPrice:         price,
			ItemName:          name,
			TokenConfirmation: token,
		},
		PackageName:    name,
		CanonicalPrice: price,
		EffectivePrice: price,
		IdempotencyKey: key,
	}, nil
}
