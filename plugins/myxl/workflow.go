package myxl

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// quotaSnapshot carries independently fetched account usage data. A failed
// source is kept separate from an empty successful source so presentation
// layers never turn transport/API failures into "no quota" or zero balance.
type quotaSnapshot struct {
	Balance    *BalanceData
	Quota      *QuotaDetailsData
	BalanceErr error
	QuotaErr   error
}

func (s quotaSnapshot) allFailed() bool {
	return s.BalanceErr != nil && s.QuotaErr != nil
}

func (s quotaSnapshot) partial() bool {
	return (s.BalanceErr != nil) != (s.QuotaErr != nil)
}

func (p *Plugin) loadQuotaSnapshot(ctx context.Context, acc *Account) quotaSnapshot {
	if p == nil || p.client == nil || acc == nil {
		err := fmt.Errorf("myxl: quota runtime unavailable")
		return quotaSnapshot{BalanceErr: err, QuotaErr: err}
	}
	if ctx == nil {
		ctx = context.Background()
	}
	balance, balanceErr := p.client.GetBalance(ctx, acc)
	quota, quotaErr := p.client.GetQuotaDetails(ctx, acc)
	return quotaSnapshot{
		Balance:    balance,
		Quota:      quota,
		BalanceErr: balanceErr,
		QuotaErr:   quotaErr,
	}
}

// requestLoginOTP owns the account/OTP request data flow for both command and
// Assistant surfaces. Persisting the subscriber id is part of the same
// operation; callers never need to duplicate placeholder-account logic.
func (p *Plugin) requestLoginOTP(ctx context.Context, rawMSISDN string) (string, error) {
	if p == nil || p.repo == nil || p.client == nil {
		return "", fmt.Errorf("myxl: login runtime unavailable")
	}
	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return "", err
	}
	subID, err := p.client.RequestOTP(ctx, msisdn)
	if err != nil {
		return "", err
	}
	acc, err := p.repo.GetByMSISDN(ctx, msisdn)
	if err != nil {
		return "", fmt.Errorf("read login account: %w", err)
	}
	if acc == nil {
		acc = &Account{MSISDN: msisdn}
	}
	if subID != "" {
		acc.SubscriberID = subID
	}
	if err := p.repo.Save(ctx, acc); err != nil {
		return "", fmt.Errorf("save login account: %w", err)
	}
	return msisdn, nil
}

// completeLoginOTP verifies the OTP and persists the resulting credentials.
// Saving an authenticated account as active intentionally reuses Repository's
// single-active-account invariant.
func (p *Plugin) completeLoginOTP(ctx context.Context, rawMSISDN, rawCode string) (*Account, error) {
	if p == nil || p.repo == nil || p.client == nil {
		return nil, fmt.Errorf("myxl: login runtime unavailable")
	}
	msisdn, err := NormalizeMSISDN(rawMSISDN)
	if err != nil {
		return nil, err
	}
	code, err := normalizeOTPCode(rawCode)
	if err != nil {
		return nil, err
	}
	tokens, err := p.client.SubmitOTP(ctx, msisdn, code)
	if err != nil {
		return nil, err
	}
	acc, err := p.repo.GetByMSISDN(ctx, msisdn)
	if err != nil {
		return nil, fmt.Errorf("read verified account: %w", err)
	}
	if acc == nil {
		acc = &Account{MSISDN: msisdn}
	}
	acc.AccessToken = tokens.AccessToken
	acc.IDToken = tokens.IDToken
	acc.RefreshToken = tokens.RefreshToken
	if tokens.ExpiresIn > 0 {
		acc.TokenExpiresAt = time.Now().Add(time.Duration(tokens.ExpiresIn) * time.Second)
	} else {
		acc.TokenExpiresAt = time.Now().Add(DefaultTokenExpiryFallback)
	}
	acc.IsActive = true
	if err := p.repo.Save(ctx, acc); err != nil {
		return nil, fmt.Errorf("save verified account: %w", err)
	}
	return acc, nil
}

func (p *Plugin) switchActiveAccount(ctx context.Context, identifier string) error {
	if p == nil || p.repo == nil {
		return fmt.Errorf("myxl: account runtime unavailable")
	}
	target := strings.TrimSpace(identifier)
	if normalized, err := NormalizeMSISDN(target); err == nil {
		target = normalized
	}
	if target == "" {
		return fmt.Errorf("myxl: account identifier is empty")
	}
	return p.repo.SetActive(ctx, target)
}

type purchaseOutcomeKind string

const (
	purchaseOutcomeSuccess     purchaseOutcomeKind = "SUCCESS"
	purchaseOutcomeFailed      purchaseOutcomeKind = "FAILED"
	purchaseOutcomePendingQRIS purchaseOutcomeKind = "PENDING_QRIS"
	purchaseOutcomeUnknown     purchaseOutcomeKind = "UNKNOWN"
	purchaseOutcomeDuplicate   purchaseOutcomeKind = "DUPLICATE"
)

type purchaseExecution struct {
	Kind     purchaseOutcomeKind
	Resolved resolvedPurchase
	Result   *SettlementResult
	Warning  string
}

func (p *Plugin) executeResolvedPurchase(ctx context.Context, resolved resolvedPurchase) (purchaseExecution, error) {
	out := purchaseExecution{Resolved: resolved}
	if p == nil || p.repo == nil || p.client == nil {
		return out, fmt.Errorf("myxl: purchase runtime unavailable")
	}

	reserved, err := p.repo.ReservePurchase(
		ctx,
		resolved.IdempotencyKey,
		resolved.Intent.MSISDN,
		resolved.Intent.OptionCode,
		resolved.Intent.Method,
	)
	if err != nil {
		return out, fmt.Errorf("reserve purchase: %w", err)
	}
	if !reserved {
		out.Kind = purchaseOutcomeDuplicate
		return out, nil
	}

	var result *SettlementResult
	switch resolved.Intent.Method {
	case "balance":
		result, err = p.client.SettlementBalance(ctx, resolved.Account, resolved.Item, resolved.Overwrite)
	case "qris":
		result, err = p.client.SettlementQRIS(ctx, resolved.Account, resolved.Item, resolved.Overwrite)
	case "gopay", "ovo", "dana", "shopeepay":
		result, err = p.client.SettlementMultipayment(
			ctx,
			resolved.Account,
			resolved.Item,
			strings.ToUpper(resolved.Intent.Method),
			resolved.Intent.WalletNumber,
			resolved.Overwrite,
		)
	case "decoy_balance":
		result, err = p.client.SettlementDecoy(ctx, resolved.Account, resolved.Item, "balance", resolved.Overwrite)
	case "decoy_qris":
		result, err = p.client.SettlementDecoy(ctx, resolved.Account, resolved.Item, "qris", resolved.Overwrite)
	case "decoy_qris0":
		result, err = p.client.SettlementDecoy(ctx, resolved.Account, resolved.Item, "qris0", resolved.Overwrite)
	default:
		err = fmt.Errorf("unsupported payment method %q", resolved.Intent.Method)
	}

	if err != nil {
		out.Kind = purchaseOutcomeUnknown
		p.persistPurchaseOutcome(ctx, resolved.IdempotencyKey, "UNKNOWN", "", err.Error())
		return out, nil
	}
	if result == nil {
		out.Kind = purchaseOutcomeUnknown
		p.persistPurchaseOutcome(ctx, resolved.IdempotencyKey, "UNKNOWN", "", "empty settlement result")
		return out, nil
	}

	out.Result = result
	dbStatus := "FAILED"
	if result.IsSuccess {
		dbStatus = "SUCCESS"
	}
	if err := p.persistPurchaseOutcome(ctx, resolved.IdempotencyKey, dbStatus, result.TransactionCode, result.Message); err != nil {
		out.Warning = "Hasil operator sudah diterima, tetapi status transaksi gagal dicatat di penyimpanan bot."
	}

	if result.IsSuccess && strings.Contains(resolved.Intent.Method, "qris") {
		out.Kind = purchaseOutcomePendingQRIS
		if result.QRCode == "" {
			out.Warning = joinPurchaseWarning(out.Warning, "Operator menerima transaksi QRIS tetapi payload QR belum tersedia. Periksa kembali status sebelum membuat transaksi baru.")
			return out, nil
		}
		qrPayload, qrErr := normalizeQRPayload(result.QRCode)
		if qrErr != nil {
			result.QRCode = ""
			out.Warning = joinPurchaseWarning(out.Warning, "Payload QRIS dari operator tidak valid; gambar QR tidak dapat dibuat.")
			return out, nil
		}
		result.QRCode = qrPayload
		now := time.Now().UTC()
		pending := &PendingQRIS{
			TransactionCode: result.TransactionCode,
			IdempotencyKey:  resolved.IdempotencyKey,
			MSISDN:          resolved.Intent.MSISDN,
			OptionCode:      resolved.Intent.OptionCode,
			PackageName:     resolved.PackageName,
			Price:           resolved.EffectivePrice,
			QRCode:          qrPayload,
			Status:          "PENDING",
			CreatedAt:       now,
			ExpiresAt:       now.Add(pendingQRISTTL),
		}
		persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		saveErr := p.repo.SavePendingQRIS(persistCtx, pending)
		cancel()
		if saveErr != nil {
			out.Warning = joinPurchaseWarning(out.Warning, "QRIS berhasil dibuat, tetapi tagihan gagal disimpan untuk dilihat kembali di bot.")
		}
		return out, nil
	}

	if result.IsSuccess {
		out.Kind = purchaseOutcomeSuccess
	} else {
		out.Kind = purchaseOutcomeFailed
	}
	return out, nil
}

func (p *Plugin) persistPurchaseOutcome(ctx context.Context, key, status, transactionCode, message string) error {
	persistCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()
	return p.repo.FinishPurchase(persistCtx, key, status, transactionCode, message)
}

func joinPurchaseWarning(current, next string) string {
	current = strings.TrimSpace(current)
	next = strings.TrimSpace(next)
	switch {
	case current == "":
		return next
	case next == "":
		return current
	default:
		return current + " " + next
	}
}
