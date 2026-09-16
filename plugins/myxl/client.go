package myxl

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/inipew/goultroid/internal/platform/network"
)

// ClientConfig holds configuration for the MyXL API client.
type ClientConfig struct {
	BaseAPIURL           string
	BaseCIAMURL          string
	APIKey               string
	BasicAuth            string
	UserAgent            string
	AxFPKey              string
	AxDeviceID           string
	XDataKey             string
	AxAPISigKey          string
	XAPIBaseSecret       string
	PaymentSigSecret     string
	EncryptedFieldKey    string
	CircleMSISDNKey      string
	XVersionApp          string
	AxRequestDevice      string
	AxRequestDeviceModel string
}

// DefaultClientConfig returns the production configuration for MyXL.
func DefaultClientConfig() ClientConfig {
	return ClientConfig{
		BaseAPIURL:           DefaultBaseAPIURL,
		BaseCIAMURL:          DefaultBaseCIAMURL,
		APIKey:               DefaultAPIKey,
		BasicAuth:            DefaultBasicAuth,
		UserAgent:            DefaultUA,
		AxFPKey:              DefaultAxFPKey,
		AxDeviceID:           DefaultAxDeviceID,
		XDataKey:             DefaultXDataKey,
		AxAPISigKey:          DefaultAxAPISigKey,
		XAPIBaseSecret:       DefaultXAPIBaseSecret,
		PaymentSigSecret:     DefaultPaymentSigSecret,
		EncryptedFieldKey:    DefaultEncryptedFieldKey,
		CircleMSISDNKey:      DefaultCircleMSISDNKey,
		XVersionApp:          DefaultXVersionApp,
		AxRequestDevice:      DefaultAxDevice,
		AxRequestDeviceModel: DefaultAxDeviceModel,
	}
}

// Client manages communications with MyXL CIAM and Engsel APIs.
type Client struct {
	mu      sync.Mutex
	cfg     ClientConfig
	httpCli *network.Client
	repo    Repository
	lastOTP map[string]time.Time
}

// NewClient constructs a new MyXL API client.
func NewClient(cfg ClientConfig, repo Repository, httpCli *network.Client) *Client {
	if httpCli == nil {
		httpCli = network.NewService(nil, nil).ForOwner("myxl")
	}
	return &Client{
		cfg:     cfg,
		httpCli: httpCli,
		repo:    repo,
		lastOTP: make(map[string]time.Time),
	}
}

// SetHTTP sets the managed network client.
func (c *Client) SetHTTP(httpCli *network.Client) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.httpCli = httpCli
}

// UpdateConfig updates the client configuration dynamically.
func (c *Client) UpdateConfig(fn func(cfg *ClientConfig)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fn(&c.cfg)
}

func (c *Client) getHTTP() *network.Client {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.httpCli == nil {
		c.httpCli = network.NewService(nil, nil).ForOwner("myxl")
	}
	return c.httpCli
}

// NormalizeMSISDN standardizes Indonesian phone numbers into format 628xxxxxxxx.
func NormalizeMSISDN(msisdn string) (string, error) {
	s := strings.TrimSpace(msisdn)
	s = strings.ReplaceAll(s, "-", "")
	s = strings.ReplaceAll(s, " ", "")
	s = strings.ReplaceAll(s, "+", "")

	if strings.HasPrefix(s, "08") {
		s = "62" + s[1:]
	} else if strings.HasPrefix(s, "8") {
		s = "62" + s
	}

	if !strings.HasPrefix(s, "628") || len(s) < 10 || len(s) > 15 {
		return "", fmt.Errorf("invalid Indonesian MSISDN: %s", msisdn)
	}
	return s, nil
}

func (c *Client) buildCIAMHeaders(requestAt string) map[string]string {
	return map[string]string{
		"Authorization":           "Basic " + c.cfg.BasicAuth,
		"Ax-Device-Id":            c.cfg.AxDeviceID,
		"Ax-Fingerprint":          GenerateDeviceFingerprint("", c.cfg.AxFPKey),
		"Ax-Request-Device":       c.cfg.AxRequestDevice,
		"Ax-Request-Device-Model": c.cfg.AxRequestDeviceModel,
		"Ax-Request-Id":           uuid.NewString(),
		"Ax-Substype":             "PREPAID",
		"User-Agent":              c.cfg.UserAgent,
		"Ax-Request-At":           requestAt,
	}
}

// RequestOTP sends an OTP request via SMS for the given MSISDN.
func (c *Client) RequestOTP(ctx context.Context, msisdn string) (string, error) {
	cleanMSISDN, err := NormalizeMSISDN(msisdn)
	if err != nil {
		return "", err
	}

	c.mu.Lock()
	if last, exists := c.lastOTP[cleanMSISDN]; exists {
		elapsed := time.Since(last)
		if elapsed < 60*time.Second {
			c.mu.Unlock()
			waitSec := int(math.Ceil((60*time.Second - elapsed).Seconds()))
			return "", fmt.Errorf("mohon tunggu %d detik sebelum meminta kode OTP kembali", waitSec)
		}
	}
	c.lastOTP[cleanMSISDN] = time.Now()
	c.mu.Unlock()

	reqURL := fmt.Sprintf("%s/realms/xl-ciam/auth/otp?contact=%s&contactType=SMS&alternateContact=false",
		c.cfg.BaseCIAMURL, url.QueryEscape(cleanMSISDN))

	now := time.Now()
	headers := c.buildCIAMHeaders(FormatMyXLHeaderTS(now))
	headers["Content-Type"] = "application/json"

	resp, err := c.getHTTP().DoRequest(ctx, network.MethodGet, reqURL, nil, headers)
	if err != nil {
		return "", fmt.Errorf("request otp failed: %w", err)
	}

	body, err := resp.Bytes()
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var res map[string]any
		if err := json.Unmarshal(body, &res); err == nil {
			if subID, ok := res["subscriber_id"].(string); ok {
				return subID, nil
			}
		}
		return "", nil
	}

	var errResp map[string]any
	if err := json.Unmarshal(body, &errResp); err == nil {
		if desc, ok := errResp["error_description"].(string); ok && desc != "" {
			return "", fmt.Errorf("OTP error: %s", desc)
		}
		if desc, ok := errResp["message"].(string); ok && desc != "" {
			return "", fmt.Errorf("OTP error: %s", desc)
		}
	}

	return "", fmt.Errorf("OTP request failed (HTTP %d): %s", resp.StatusCode, string(body))
}

// SubmitOTP submits the received OTP and returns authentication tokens.
func (c *Client) SubmitOTP(ctx context.Context, msisdn, code string) (*Tokens, error) {
	cleanMSISDN, err := NormalizeMSISDN(msisdn)
	if err != nil {
		return nil, err
	}
	code = strings.TrimSpace(code)

	reqURL := fmt.Sprintf("%s/realms/xl-ciam/protocol/openid-connect/token", c.cfg.BaseCIAMURL)

	now := time.Now()
	wib := time.FixedZone("WIB", 7*3600)
	nowWIB := now.In(wib)
	tsForSign := nowWIB.Format("2006-01-02T15:04:05.000-0700")
	// Clock skew offset header (-5 minutes)
	headerTime := nowWIB.Add(-5 * time.Minute).Format("2006-01-02T15:04:05.000-0700")

	sig := MakeAxAPISignature(tsForSign, cleanMSISDN, code, "SMS", c.cfg.AxAPISigKey)

	formData := url.Values{}
	formData.Set("contactType", "SMS")
	formData.Set("code", code)
	formData.Set("grant_type", "password")
	formData.Set("contact", cleanMSISDN)
	formData.Set("scope", "openid")

	headers := c.buildCIAMHeaders(headerTime)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	headers["Ax-Api-Signature"] = sig

	resp, err := c.getHTTP().DoRequest(ctx, network.MethodPost, reqURL, strings.NewReader(formData.Encode()), headers)
	if err != nil {
		return nil, fmt.Errorf("submit otp failed: %w", err)
	}

	body, err := resp.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var tokens Tokens
		if err := json.Unmarshal(body, &tokens); err != nil {
			return nil, fmt.Errorf("parse tokens: %w", err)
		}
		if tokens.IDToken == "" {
			return nil, errors.New("id_token not found in response")
		}
		return &tokens, nil
	}

	var errResp map[string]any
	if err := json.Unmarshal(body, &errResp); err == nil {
		if desc, ok := errResp["error_description"].(string); ok && desc != "" {
			return nil, fmt.Errorf("authentication error: %s", desc)
		}
		if desc, ok := errResp["message"].(string); ok && desc != "" {
			return nil, fmt.Errorf("authentication error: %s", desc)
		}
	}

	return nil, fmt.Errorf("OTP submission failed (HTTP %d): %s", resp.StatusCode, string(body))
}

// RefreshToken exchanges a refresh token for new credentials, with automatic extend_session fallback.
func (c *Client) RefreshToken(ctx context.Context, acc *Account) (*Tokens, error) {
	if acc.RefreshToken == "" {
		return nil, errors.New("no refresh token available")
	}

	reqURL := fmt.Sprintf("%s/realms/xl-ciam/protocol/openid-connect/token", c.cfg.BaseCIAMURL)
	formData := url.Values{}
	formData.Set("grant_type", "refresh_token")
	formData.Set("refresh_token", acc.RefreshToken)

	headers := c.buildCIAMHeaders(FormatMyXLHeaderTS(time.Now()))
	headers["Content-Type"] = "application/x-www-form-urlencoded"

	resp, err := c.getHTTP().DoRequest(ctx, network.MethodPost, reqURL, strings.NewReader(formData.Encode()), headers)
	if err != nil {
		return nil, fmt.Errorf("refresh token request: %w", err)
	}

	body, err := resp.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read refresh response: %w", err)
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		var tokens Tokens
		if err := json.Unmarshal(body, &tokens); err == nil && tokens.IDToken != "" {
			acc.AccessToken = tokens.AccessToken
			acc.IDToken = tokens.IDToken
			if tokens.RefreshToken != "" {
				acc.RefreshToken = tokens.RefreshToken
			}
			if c.repo != nil {
				_ = c.repo.Save(ctx, acc)
			}
			return &tokens, nil
		}
	}

	// Fallback to extend-session if subscriber_id is present
	if acc.SubscriberID != "" {
		tokens, err := c.extendSession(ctx, acc.SubscriberID)
		if err == nil && tokens != nil && tokens.IDToken != "" {
			acc.AccessToken = tokens.AccessToken
			acc.IDToken = tokens.IDToken
			if tokens.RefreshToken != "" {
				acc.RefreshToken = tokens.RefreshToken
			}
			if c.repo != nil {
				_ = c.repo.Save(ctx, acc)
			}
			return tokens, nil
		}
	}

	return nil, fmt.Errorf("refresh token failed (HTTP %d): %s", resp.StatusCode, string(body))
}

func (c *Client) extendSession(ctx context.Context, subscriberID string) (*Tokens, error) {
	b64SubID := base64.StdEncoding.EncodeToString([]byte(subscriberID))
	reqURL := fmt.Sprintf("%s/realms/xl-ciam/auth/extend-session?contact=%s&contactType=DEVICEID",
		c.cfg.BaseCIAMURL, url.QueryEscape(b64SubID))

	headers := c.buildCIAMHeaders(FormatMyXLHeaderTS(time.Now()))
	headers["Content-Type"] = "application/json"

	resp, err := c.getHTTP().DoRequest(ctx, network.MethodGet, reqURL, nil, headers)
	if err != nil {
		return nil, err
	}

	body, err := resp.Bytes()
	if err != nil {
		return nil, err
	}

	var res struct {
		Data struct {
			ExchangeCode string `json:"exchange_code"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &res); err != nil || res.Data.ExchangeCode == "" {
		return nil, fmt.Errorf("extend session missing exchange_code: %s", string(body))
	}

	// Submit exchange_code with contactType=DEVICEID
	return c.submitDeviceIDToken(ctx, b64SubID, res.Data.ExchangeCode)
}

func (c *Client) submitDeviceIDToken(ctx context.Context, b64Contact, exchangeCode string) (*Tokens, error) {
	reqURL := fmt.Sprintf("%s/realms/xl-ciam/protocol/openid-connect/token", c.cfg.BaseCIAMURL)

	now := time.Now()
	wib := time.FixedZone("WIB", 7*3600)
	nowWIB := now.In(wib)
	tsForSign := nowWIB.Format("2006-01-02T15:04:05.000-0700")
	headerTime := nowWIB.Add(-5 * time.Minute).Format("2006-01-02T15:04:05.000-0700")

	sig := MakeAxAPISignature(tsForSign, b64Contact, exchangeCode, "DEVICEID", c.cfg.AxAPISigKey)

	formData := url.Values{}
	formData.Set("contactType", "DEVICEID")
	formData.Set("code", exchangeCode)
	formData.Set("grant_type", "password")
	formData.Set("contact", b64Contact)
	formData.Set("scope", "openid")

	headers := c.buildCIAMHeaders(headerTime)
	headers["Content-Type"] = "application/x-www-form-urlencoded"
	headers["Ax-Api-Signature"] = sig

	resp, err := c.getHTTP().DoRequest(ctx, network.MethodPost, reqURL, strings.NewReader(formData.Encode()), headers)
	if err != nil {
		return nil, err
	}

	body, err := resp.Bytes()
	if err != nil {
		return nil, err
	}

	var tokens Tokens
	if err := json.Unmarshal(body, &tokens); err != nil {
		return nil, fmt.Errorf("parse tokens from device exchange: %w", err)
	}
	return &tokens, nil
}

// ExecuteEngsel sends an encrypted request to the MyXL Engsel API with automatic retry on token expiry.
func (c *Client) ExecuteEngsel(ctx context.Context, acc *Account, method, path string, payload any) (*APIResponse, error) {
	resp, err := c.executeEngselOnce(ctx, acc, method, path, payload)
	if err == nil && !resp.IsUnauthorized() {
		return resp, nil
	}

	// If unauthorized or error indicating expired token, refresh token and retry once
	if (err != nil && strings.Contains(err.Error(), "unauthorized")) || (resp != nil && resp.IsUnauthorized()) {
		_, refreshErr := c.RefreshToken(ctx, acc)
		if refreshErr != nil {
			if err != nil {
				return nil, fmt.Errorf("api request failed (%w) and token refresh also failed: %v", err, refreshErr)
			}
			return resp, fmt.Errorf("session expired and token refresh failed: %w", refreshErr)
		}

		// Retry with updated tokens
		return c.executeEngselOnce(ctx, acc, method, path, payload)
	}

	return resp, err
}

func (c *Client) executeEngselOnce(ctx context.Context, acc *Account, method, path string, payload any) (*APIResponse, error) {
	var plainJSON string
	if payload == nil {
		plainJSON = `{"is_enterprise":false,"lang":"en"}`
	} else if s, ok := payload.(string); ok {
		plainJSON = s
	} else {
		b, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("marshal payload: %w", err)
		}
		plainJSON = string(b)
	}

	nowMs := time.Now().UnixMilli()
	xdata, err := EncryptXData(plainJSON, nowMs, c.cfg.XDataKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt xdata: %w", err)
	}

	reqEnvelope := EncryptedBody{
		XData: xdata,
		XTime: nowMs,
	}
	bodyBytes, err := json.Marshal(reqEnvelope)
	if err != nil {
		return nil, fmt.Errorf("marshal encrypted envelope: %w", err)
	}

	reqURL := fmt.Sprintf("%s/%s", strings.TrimRight(c.cfg.BaseAPIURL, "/"), strings.TrimLeft(path, "/"))

	sigTimeSec := nowMs / 1000
	xSig := MakeXSignature(acc.IDToken, method, strings.TrimLeft(path, "/"), sigTimeSec, c.cfg.XAPIBaseSecret)

	headers := map[string]string{
		"Content-Type":     "application/json; charset=utf-8",
		"User-Agent":       c.cfg.UserAgent,
		"x-api-key":        c.cfg.APIKey,
		"authorization":    "Bearer " + acc.IDToken,
		"x-hv":             "v3",
		"x-signature-time": strconv.FormatInt(sigTimeSec, 10),
		"x-signature":      xSig,
		"x-request-id":     uuid.NewString(),
		"x-request-at":     FormatMyXLHeaderTS(time.Now()),
		"x-version-app":    c.cfg.XVersionApp,
	}

	httpResp, err := c.getHTTP().DoRequest(ctx, method, reqURL, bytes.NewReader(bodyBytes), headers)
	if err != nil {
		return nil, fmt.Errorf("http request: %w", err)
	}

	respBody, err := httpResp.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	return c.parseEngselResponse(respBody)
}

func (c *Client) parseEngselResponse(body []byte) (*APIResponse, error) {
	// 1. Try parsing as encrypted envelope
	var env EncryptedBody
	if err := json.Unmarshal(body, &env); err == nil && env.XData != "" && env.XTime > 0 {
		decrypted, err := DecryptXData(env.XData, env.XTime, c.cfg.XDataKey)
		if err == nil {
			var apiResp APIResponse
			if err := json.Unmarshal([]byte(decrypted), &apiResp); err == nil {
				return &apiResp, nil
			}
		}
	}

	// 2. Try parsing directly as plain APIResponse
	var apiResp APIResponse
	if err := json.Unmarshal(body, &apiResp); err == nil && (apiResp.Status != "" || apiResp.Message != "") {
		return &apiResp, nil
	}

	return nil, fmt.Errorf("failed to parse response: %s", string(body))
}

// GetBalance retrieves the balance details for the account.
func (c *Client) GetBalance(ctx context.Context, acc *Account) (*BalanceData, error) {
	apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "api/v8/packages/balance-and-credit", nil)
	if err != nil {
		return nil, err
	}
	if !apiResp.IsSuccess() {
		return nil, fmt.Errorf("API error: %s (%s)", apiResp.Message, apiResp.Status)
	}

	var wrapper struct {
		Balance BalanceData `json:"balance"`
	}
	if err := json.Unmarshal(apiResp.Data, &wrapper); err != nil {
		return nil, fmt.Errorf("unmarshal balance: %w", err)
	}
	return &wrapper.Balance, nil
}

// GetQuotaDetails retrieves active packages and quota breakdown for the account.
func (c *Client) GetQuotaDetails(ctx context.Context, acc *Account) (*QuotaDetailsData, error) {
	payload := map[string]any{
		"is_enterprise":    false,
		"lang":             "en",
		"family_member_id": "",
	}
	apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "api/v8/packages/quota-details", payload)
	if err != nil {
		return nil, err
	}
	if !apiResp.IsSuccess() {
		return nil, fmt.Errorf("API error: %s (%s)", apiResp.Message, apiResp.Status)
	}

	var quotaData QuotaDetailsData
	if err := json.Unmarshal(apiResp.Data, &quotaData); err != nil {
		return nil, fmt.Errorf("unmarshal quota details: %w", err)
	}
	return &quotaData, nil
}

// ForceRefreshToken forces an immediate token renewal via CIAM and persists it to the database.
func (c *Client) ForceRefreshToken(ctx context.Context, identifier string) (*Tokens, error) {
	acc, err := c.repo.GetByMSISDN(ctx, identifier)
	if err != nil {
		return nil, fmt.Errorf("get account: %w", err)
	}
	if acc == nil {
		return nil, errors.New("account not found")
	}

	tokens, err := c.RefreshToken(ctx, acc)
	if err != nil {
		// Fallback to extend session using SubscriberID if available
		if acc.SubscriberID != "" {
			tokens, err = c.extendSession(ctx, acc.SubscriberID)
		}
		if err != nil {
			return nil, fmt.Errorf("force refresh token failed: %w", err)
		}
	}

	acc.AccessToken = tokens.AccessToken
	acc.IDToken = tokens.IDToken
	acc.RefreshToken = tokens.RefreshToken
	acc.UpdatedAt = time.Now()

	if err := c.repo.Save(ctx, acc); err != nil {
		return nil, fmt.Errorf("save refreshed tokens: %w", err)
	}

	return tokens, nil
}

// GetPackagesByFamily queries available packages for a given family code across multiple migration configurations.
func (c *Client) GetPackagesByFamily(ctx context.Context, acc *Account, familyCode string) (*PackageListResponse, error) {
	trialConfigs := []struct {
		MigrationType string
		IsEnterprise  bool
	}{
		{"NONE", false},
		{"NONE", true},
		{"PRE_TO_PRIOH", false},
		{"PRIOH_TO_PRIO", false},
		{"PRIO_TO_PRIOH", false},
		{"PRE_TO_PRIOH", true},
	}

	for _, tc := range trialConfigs {
		payload := map[string]any{
			"is_show_tagging_tab":    true,
			"is_dedicated_event":     true,
			"is_transaction_routine": false,
			"migration_type":         tc.MigrationType,
			"package_family_code":    familyCode,
			"is_autobuy":             false,
			"is_enterprise":          tc.IsEnterprise,
			"is_pdlp":                true,
			"referral_code":          "",
			"is_migration":           false,
			"lang":                   "en",
		}

		apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "api/v8/xl-stores/options/list", payload)
		if err != nil {
			continue
		}
		if !apiResp.IsSuccess() {
			continue
		}

		var res PackageListResponse
		if err := json.Unmarshal(apiResp.Data, &res); err == nil && res.PackageFamily.Name != "" {
			return &res, nil
		}
	}

	return nil, fmt.Errorf("no packages found for family code: %s", familyCode)
}

// GetPackageDetails retrieves specific details for a package option code.
func (c *Client) GetPackageDetails(ctx context.Context, acc *Account, packageCode string) (*PackageDetailsData, error) {
	payload := map[string]any{
		"is_enterprise":        false,
		"package_code":         packageCode,
		"package_option_code":  packageCode,
		"is_from_hot_campaign": false,
		"lang":                 "en",
		"family_member_id":     "",
	}

	apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "api/v8/xl-stores/options/detail", payload)
	if err != nil {
		return nil, err
	}
	if !apiResp.IsSuccess() {
		return nil, fmt.Errorf("API error: %s (%s)", apiResp.Message, apiResp.Status)
	}

	var data PackageDetailsData
	if err := json.Unmarshal(apiResp.Data, &data); err != nil {
		return nil, fmt.Errorf("unmarshal package details: %w", err)
	}
	return &data, nil
}

// GetPackageDetailsByVariant resolves an option by its variant code and 1-based order index.
func (c *Client) GetPackageDetailsByVariant(ctx context.Context, acc *Account, familyCode, variantCode string, optionOrder int) (*PackageDetailsData, error) {
	familyData, err := c.GetPackagesByFamily(ctx, acc, familyCode)
	if err != nil {
		return nil, err
	}

	var targetOptionCode string
	for _, v := range familyData.PackageVariants {
		if v.PackageVariantCode == variantCode {
			idx := optionOrder - 1
			if idx >= 0 && idx < len(v.PackageOptions) {
				targetOptionCode = v.PackageOptions[idx].PackageOptionCode
				break
			}
		}
	}

	if targetOptionCode == "" {
		return nil, fmt.Errorf("option not found for variant %s (order %d)", variantCode, optionOrder)
	}

	return c.GetPackageDetails(ctx, acc, targetOptionCode)
}

// GetPaymentMethodsOption retrieves payment options, timestamp, and token_payment for purchasing.
func (c *Client) GetPaymentMethodsOption(ctx context.Context, acc *Account, tokenConfirmation, itemCode string) (*PaymentMethodsOptionData, error) {
	payload := map[string]any{
		"payment_type":       "PURCHASE",
		"is_enterprise":      false,
		"payment_target":     itemCode,
		"lang":               "en",
		"is_referral":        false,
		"token_confirmation": tokenConfirmation,
	}

	apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "payments/api/v8/payment-methods-option", payload)
	if err != nil {
		return nil, err
	}
	if !apiResp.IsSuccess() {
		return nil, fmt.Errorf("API error: %s (%s)", apiResp.Message, apiResp.Status)
	}

	var data PaymentMethodsOptionData
	if err := json.Unmarshal(apiResp.Data, &data); err != nil {
		return nil, fmt.Errorf("unmarshal payment methods option: %w", err)
	}
	return &data, nil
}

// SendPayment executes an encrypted payment request with payment-specific signature.
func (c *Client) SendPayment(ctx context.Context, acc *Account, path string, payload any, params PaymentSignatureParams) (*APIResponse, error) {
	plainBodyBytes, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("marshal payment payload: %w", err)
	}

	xtimeMs := time.Now().UnixMilli()
	xdata, err := EncryptXData(string(plainBodyBytes), xtimeMs, c.cfg.XDataKey)
	if err != nil {
		return nil, fmt.Errorf("encrypt payment data: %w", err)
	}

	bodyBytes, err := json.Marshal(EncryptedBody{
		XData: xdata,
		XTime: xtimeMs,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal encrypted payment envelope: %w", err)
	}

	xSig := MakeXSignaturePaymentParams(params, c.cfg.PaymentSigSecret)
	reqURL := fmt.Sprintf("%s/%s", strings.TrimRight(c.cfg.BaseAPIURL, "/"), strings.TrimLeft(path, "/"))

	headers := map[string]string{
		"Content-Type":     "application/json; charset=utf-8",
		"User-Agent":       c.cfg.UserAgent,
		"x-api-key":        c.cfg.APIKey,
		"authorization":    "Bearer " + acc.IDToken,
		"x-hv":             "v3",
		"x-signature-time": strconv.FormatInt(params.SigTimeSec, 10),
		"x-signature":      xSig,
		"x-request-id":     uuid.NewString(),
		"x-request-at":     FormatMyXLHeaderTS(time.Now()),
		"x-version-app":    c.cfg.XVersionApp,
	}

	httpResp, err := c.getHTTP().DoRequest(ctx, network.MethodPost, reqURL, bytes.NewReader(bodyBytes), headers)
	if err != nil {
		return nil, fmt.Errorf("payment http request: %w", err)
	}

	respBody, err := httpResp.Bytes()
	if err != nil {
		return nil, fmt.Errorf("read payment response: %w", err)
	}

	return c.parseEngselResponse(respBody)
}

// SettlementBalance executes purchase using main credit / pulsa (BALANCE).
func (c *Client) SettlementBalance(ctx context.Context, acc *Account, req PurchaseItem, overwriteAmount *int64) (*SettlementResult, error) {
	payMethods, err := c.GetPaymentMethodsOption(ctx, acc, req.TokenConfirmation, req.ItemCode)
	if err != nil {
		return nil, fmt.Errorf("get payment methods: %w", err)
	}

	totalAmount := req.ItemPrice
	if overwriteAmount != nil {
		totalAmount = *overwriteAmount
	}

	encryptedPaymentToken := BuildEncryptedFieldWithKey(c.cfg.EncryptedFieldKey, true)
	encryptedAuthID := BuildEncryptedFieldWithKey(c.cfg.EncryptedFieldKey, true)

	settlementReq := SettlementBalanceRequest{
		TotalDiscount:             0,
		IsEnterprise:              false,
		TokenPayment:              payMethods.TokenPayment,
		EncryptedPaymentToken:     encryptedPaymentToken,
		EncryptedAuthenticationID: encryptedAuthID,
		AccessToken:               acc.AccessToken,
		PaymentMethod:             "BALANCE",
		Timestamp:                 int64(payMethods.Timestamp),
		PaymentFor:                "BUY_PACKAGE",
		TotalAmount:               totalAmount,
		Items:                     []PurchaseItem{req},
		AdditionalData: BalanceAdditionalData{
			OriginalPrice: req.ItemPrice,
			BalanceType:   "PREPAID_BALANCE",
		},
	}

	path := "payments/api/v8/settlement-multipayment"
	params := PaymentSignatureParams{
		AccessToken:    acc.AccessToken,
		SigTimeSec:     int64(payMethods.Timestamp),
		PackageCode:    req.ItemCode,
		TokenPayment:   payMethods.TokenPayment,
		PaymentMethod:  "BALANCE",
		PaymentFor:     "BUY_PACKAGE",
		Path:           path,
		XAPIBaseSecret: c.cfg.XAPIBaseSecret,
	}

	apiResp, err := c.SendPayment(ctx, acc, path, settlementReq, params)
	if err != nil {
		return nil, err
	}

	res := &SettlementResult{
		IsSuccess: apiResp.IsSuccess(),
		Status:    apiResp.Status,
		Message:   apiResp.Message,
	}

	if apiResp.IsSuccess() {
		var stData SettlementData
		if err := json.Unmarshal(apiResp.Data, &stData); err == nil {
			res.TransactionCode = stData.TransactionCode
		}
	}

	return res, nil
}

// SettlementMultipayment executes purchase using e-wallets (GOPAY, OVO, DANA, SHOPEEPAY).
func (c *Client) SettlementMultipayment(ctx context.Context, acc *Account, req PurchaseItem, paymentMethod, walletNumber string, overwriteAmount *int64) (*SettlementResult, error) {
	payMethods, err := c.GetPaymentMethodsOption(ctx, acc, req.TokenConfirmation, req.ItemCode)
	if err != nil {
		return nil, fmt.Errorf("get payment methods: %w", err)
	}

	totalAmount := req.ItemPrice
	if overwriteAmount != nil {
		totalAmount = *overwriteAmount
	}

	settlementReq := SettlementMultipaymentRequest{
		CanTriggerRating:  false,
		TotalDiscount:     0,
		PaymentFor:        "BUY_PACKAGE",
		IsEnterprise:      false,
		AccessToken:       acc.AccessToken,
		IsMyXLWallet:      false,
		WalletNumber:      strings.TrimSpace(walletNumber),
		AdditionalData:    map[string]any{},
		TotalAmount:       totalAmount,
		TotalFee:          0,
		IsUsePoint:        false,
		Lang:              "en",
		Items:             []PurchaseItem{req},
		VerificationToken: payMethods.TokenPayment,
		PaymentMethod:     paymentMethod,
		Timestamp:         int64(payMethods.Timestamp),
	}

	path := "payments/api/v8/settlement-multipayment/ewallet"
	params := PaymentSignatureParams{
		AccessToken:    acc.AccessToken,
		SigTimeSec:     int64(payMethods.Timestamp),
		PackageCode:    req.ItemCode,
		TokenPayment:   payMethods.TokenPayment,
		PaymentMethod:  "EWALLET",
		PaymentFor:     "BUY_PACKAGE",
		Path:           path,
		XAPIBaseSecret: c.cfg.XAPIBaseSecret,
	}

	apiResp, err := c.SendPayment(ctx, acc, path, settlementReq, params)
	if err != nil {
		return nil, err
	}

	res := &SettlementResult{
		IsSuccess: apiResp.IsSuccess(),
		Status:    apiResp.Status,
		Message:   apiResp.Message,
	}

	if apiResp.IsSuccess() {
		var stData SettlementData
		if err := json.Unmarshal(apiResp.Data, &stData); err == nil {
			res.TransactionCode = stData.TransactionCode
			res.Deeplink = stData.Deeplink
		}
	}

	return res, nil
}

// SettlementQRIS executes purchase using QRIS and retrieves the QR payload code.
func (c *Client) SettlementQRIS(ctx context.Context, acc *Account, req PurchaseItem, overwriteAmount *int64) (*SettlementResult, error) {
	payMethods, err := c.GetPaymentMethodsOption(ctx, acc, req.TokenConfirmation, req.ItemCode)
	if err != nil {
		return nil, fmt.Errorf("get payment methods: %w", err)
	}

	totalAmount := req.ItemPrice
	if overwriteAmount != nil {
		totalAmount = *overwriteAmount
	}

	settlementReq := SettlementQrisRequest{
		CanTriggerRating:  false,
		TotalDiscount:     0,
		PaymentFor:        "BUY_PACKAGE",
		IsEnterprise:      false,
		AccessToken:       acc.AccessToken,
		IsMyXLWallet:      false,
		AdditionalData:    QrisAdditionalData{OriginalPrice: req.ItemPrice},
		TotalAmount:       totalAmount,
		TotalFee:          0,
		IsUsePoint:        false,
		Lang:              "en",
		Items:             []PurchaseItem{req},
		VerificationToken: payMethods.TokenPayment,
		PaymentMethod:     "QRIS",
		Timestamp:         int64(payMethods.Timestamp),
	}

	path := "payments/api/v8/settlement-multipayment/qris"
	params := PaymentSignatureParams{
		AccessToken:    acc.AccessToken,
		SigTimeSec:     int64(payMethods.Timestamp),
		PackageCode:    req.ItemCode,
		TokenPayment:   payMethods.TokenPayment,
		PaymentMethod:  "QRIS",
		PaymentFor:     "BUY_PACKAGE",
		Path:           path,
		XAPIBaseSecret: c.cfg.XAPIBaseSecret,
	}

	apiResp, err := c.SendPayment(ctx, acc, path, settlementReq, params)
	if err != nil {
		return nil, err
	}

	res := &SettlementResult{
		IsSuccess: apiResp.IsSuccess(),
		Status:    apiResp.Status,
		Message:   apiResp.Message,
	}

	if apiResp.IsSuccess() {
		var stData SettlementData
		if err := json.Unmarshal(apiResp.Data, &stData); err == nil {
			res.TransactionCode = stData.TransactionCode
			if stData.TransactionCode != "" {
				qr, err := c.GetQRISCode(ctx, acc, stData.TransactionCode)
				if err == nil {
					res.QRCode = qr
				}
			}
		}
	}

	return res, nil
}

// GetQRISCode retrieves the pending QR string from pending-detail API.
func (c *Client) GetQRISCode(ctx context.Context, acc *Account, transactionID string) (string, error) {
	payload := map[string]any{
		"transaction_id": transactionID,
		"is_enterprise":  false,
		"lang":           "en",
		"status":         "",
	}

	apiResp, err := c.ExecuteEngsel(ctx, acc, network.MethodPost, "payments/api/v8/pending-detail", payload)
	if err != nil {
		return "", err
	}
	if !apiResp.IsSuccess() {
		return "", fmt.Errorf("API error: %s (%s)", apiResp.Message, apiResp.Status)
	}

	var data QrisPendingDetailData
	if err := json.Unmarshal(apiResp.Data, &data); err != nil {
		return "", fmt.Errorf("unmarshal qris code: %w", err)
	}
	return data.QRCode, nil
}

// GetDecoy resolves and refreshes decoy configuration for the given account subscription.
func (c *Client) GetDecoy(ctx context.Context, acc *Account, decoyPaymentType string) (*DecoyConfig, error) {
	norm := strings.ToLower(strings.TrimSpace(decoyPaymentType))
	if norm == "pulsa" {
		norm = "balance"
	}
	if norm != "balance" && norm != "qris" && norm != "qris0" {
		return nil, fmt.Errorf("unsupported decoy type: %s", decoyPaymentType)
	}

	prefix := "default"
	subType := strings.ToUpper(acc.SubscriptionType)
	if subType == "PRIORITAS" || subType == "PRIOHYBRID" || subType == "GO" {
		prefix = "prio"
	}

	key := fmt.Sprintf("%s-%s", prefix, norm)
	decoy, err := c.repo.GetDecoy(ctx, key)
	if err != nil {
		return nil, fmt.Errorf("get decoy from db: %w", err)
	}
	if decoy == nil {
		return nil, fmt.Errorf("decoy configuration not found for key: %s", key)
	}

	now := time.Now().Unix()
	if decoy.OptionCode == "" || decoy.TokenConfirmation == "" || now-decoy.LastFetchedAt > 300 {
		detail, err := c.GetPackageDetailsByVariant(ctx, acc, decoy.FamilyCode, decoy.VariantCode, int(decoy.OrderNo))
		if err != nil {
			if decoy.OptionCode != "" {
				return decoy, nil // Use stale cache if refresh fails
			}
			return nil, fmt.Errorf("refresh decoy failed: %w", err)
		}

		if detail.PackageOption != nil {
			decoy.OptionCode = detail.PackageOption.PackageOptionCode
		}
		decoy.TokenConfirmation = detail.TokenConfirmation
		decoy.LastFetchedAt = now
		_ = c.repo.UpsertDecoy(ctx, decoy)
	}

	return decoy, nil
}

func makeRandPrefix() string {
	var b [2]byte
	_, _ = rand.Read(b[:])
	val := (int(b[0])<<8|int(b[1]))%9000 + 1000
	return strconv.Itoa(val)
}

// SettlementDecoy executes purchase pairing the target package with a decoy item.
func (c *Client) SettlementDecoy(ctx context.Context, acc *Account, req PurchaseItem, decoyPaymentType string, overwriteAmount *int64) (*SettlementResult, error) {
	decoy, err := c.GetDecoy(ctx, acc, decoyPaymentType)
	if err != nil {
		return nil, err
	}

	// Fetch decoy package name (fallback to option code)
	decoyName := decoy.OptionCode
	if decoyDetail, err := c.GetPackageDetails(ctx, acc, decoy.OptionCode); err == nil && decoyDetail.PackageOption != nil {
		decoyName = decoyDetail.PackageOption.Name
	}

	randPrefix := makeRandPrefix()
	targetItem := PurchaseItem{
		ItemCode:          req.ItemCode,
		ItemPrice:         req.ItemPrice,
		ItemName:          fmt.Sprintf("%s %s", randPrefix, req.ItemName),
		TokenConfirmation: req.TokenConfirmation,
	}

	decoyItem := PurchaseItem{
		ItemCode:          decoy.OptionCode,
		ItemPrice:         decoy.Price,
		ItemName:          fmt.Sprintf("%s %s", randPrefix, decoyName),
		TokenConfirmation: decoy.TokenConfirmation,
	}

	items := []PurchaseItem{targetItem, decoyItem}
	defaultTotal := req.ItemPrice + decoy.Price
	totalAmount := defaultTotal
	if overwriteAmount != nil {
		totalAmount = *overwriteAmount
	}

	// For decoy: call GetPaymentMethodsOption with index 1 (decoy's token_confirmation & option_code)
	payMethods, err := c.GetPaymentMethodsOption(ctx, acc, decoy.TokenConfirmation, decoy.OptionCode)
	if err != nil {
		return nil, fmt.Errorf("get decoy payment methods: %w", err)
	}

	packageCodes := fmt.Sprintf("%s;%s", targetItem.ItemCode, decoyItem.ItemCode)
	norm := strings.ToLower(strings.TrimSpace(decoyPaymentType))
	if norm == "pulsa" {
		norm = "balance"
	}

	if norm == "balance" {
		path := "payments/api/v8/settlement-multipayment"
		encryptedPaymentToken := BuildEncryptedFieldWithKey(c.cfg.EncryptedFieldKey, true)
		encryptedAuthID := BuildEncryptedFieldWithKey(c.cfg.EncryptedFieldKey, true)

		settlementReq := SettlementBalanceRequest{
			TotalDiscount:             0,
			IsEnterprise:              false,
			TokenPayment:              payMethods.TokenPayment,
			EncryptedPaymentToken:     encryptedPaymentToken,
			EncryptedAuthenticationID: encryptedAuthID,
			AccessToken:               acc.AccessToken,
			PaymentMethod:             "BALANCE",
			Timestamp:                 int64(payMethods.Timestamp),
			PaymentFor:                "SHARE_PACKAGE",
			TotalAmount:               totalAmount,
			Items:                     items,
			AdditionalData: BalanceAdditionalData{
				OriginalPrice: req.ItemPrice,
				BalanceType:   "PREPAID_BALANCE",
			},
		}

		params := PaymentSignatureParams{
			AccessToken:    acc.AccessToken,
			SigTimeSec:     int64(payMethods.Timestamp),
			PackageCode:    packageCodes,
			TokenPayment:   payMethods.TokenPayment,
			PaymentMethod:  "BALANCE",
			PaymentFor:     "SHARE_PACKAGE",
			Path:           path,
			XAPIBaseSecret: c.cfg.XAPIBaseSecret,
		}

		apiResp, err := c.SendPayment(ctx, acc, path, settlementReq, params)
		if err != nil {
			return nil, err
		}

		res := &SettlementResult{
			IsSuccess: apiResp.IsSuccess(),
			Status:    apiResp.Status,
			Message:   apiResp.Message,
		}
		if apiResp.IsSuccess() {
			var stData SettlementData
			if err := json.Unmarshal(apiResp.Data, &stData); err == nil {
				res.TransactionCode = stData.TransactionCode
			}
		}
		return res, nil
	}

	// QRIS / QRIS0 decoy
	path := "payments/api/v8/settlement-multipayment/qris"
	settlementReq := SettlementQrisRequest{
		CanTriggerRating:  false,
		TotalDiscount:     0,
		PaymentFor:        "SHARE_PACKAGE",
		IsEnterprise:      false,
		AccessToken:       acc.AccessToken,
		IsMyXLWallet:      false,
		AdditionalData:    QrisAdditionalData{OriginalPrice: req.ItemPrice},
		TotalAmount:       totalAmount,
		TotalFee:          0,
		IsUsePoint:        false,
		Lang:              "en",
		Items:             items,
		VerificationToken: payMethods.TokenPayment,
		PaymentMethod:     "QRIS",
		Timestamp:         int64(payMethods.Timestamp),
	}

	params := PaymentSignatureParams{
		AccessToken:    acc.AccessToken,
		SigTimeSec:     int64(payMethods.Timestamp),
		PackageCode:    packageCodes,
		TokenPayment:   payMethods.TokenPayment,
		PaymentMethod:  "QRIS",
		PaymentFor:     "SHARE_PACKAGE",
		Path:           path,
		XAPIBaseSecret: c.cfg.XAPIBaseSecret,
	}

	apiResp, err := c.SendPayment(ctx, acc, path, settlementReq, params)
	if err != nil {
		return nil, err
	}

	res := &SettlementResult{
		IsSuccess: apiResp.IsSuccess(),
		Status:    apiResp.Status,
		Message:   apiResp.Message,
	}

	if apiResp.IsSuccess() {
		var stData SettlementData
		if err := json.Unmarshal(apiResp.Data, &stData); err == nil {
			res.TransactionCode = stData.TransactionCode
			if stData.TransactionCode != "" {
				qr, err := c.GetQRISCode(ctx, acc, stData.TransactionCode)
				if err == nil {
					res.QRCode = qr
				}
			}
		}
	}

	return res, nil
}
