package myxl

import (
	"bytes"
	"context"
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
