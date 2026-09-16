package myxl

import (
	"encoding/json"
	"strings"
	"time"
)

// Account represents a stored MyXL account in the database.
type Account struct {
	MSISDN           string    `json:"msisdn"`
	Alias            string    `json:"alias"`
	IsActive         bool      `json:"is_active"`
	AccessToken      string    `json:"access_token"`
	IDToken          string    `json:"id_token"`
	RefreshToken     string    `json:"refresh_token"`
	SubscriberID     string    `json:"subscriber_id"`
	SubscriptionType string    `json:"subscription_type"`
	CreatedAt        time.Time `json:"created_at"`
	UpdatedAt        time.Time `json:"updated_at"`
}

// Tokens represents authentication credentials returned by CIAM.
type Tokens struct {
	AccessToken  string `json:"access_token"`
	IDToken      string `json:"id_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in,omitempty"`
	TokenType    string `json:"token_type,omitempty"`
}

// APIResponse represents the standard decrypted envelope returned by MyXL API.
type APIResponse struct {
	Status  string          `json:"status"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

// IsSuccess checks if the API response indicates a successful operation.
func (r *APIResponse) IsSuccess() bool {
	switch strings.ToUpper(r.Status) {
	case "SUCCESS", "OK", "00", "200":
		return true
	default:
		return false
	}
}

// IsUnauthorized checks if the API response indicates an expired or invalid token.
func (r *APIResponse) IsUnauthorized() bool {
	status := strings.ToUpper(r.Status)
	msg := strings.ToLower(r.Message)

	if status == "401" || status == "REQUEST_MISSING_BEARER" ||
		strings.Contains(status, "UNAUTHORIZED") ||
		strings.Contains(status, "INVALID_TOKEN") ||
		strings.Contains(status, "EXPIRED_TOKEN") {
		return true
	}

	return strings.Contains(msg, "unauthorized") ||
		strings.Contains(msg, "token expired") ||
		strings.Contains(msg, "expired token") ||
		strings.Contains(msg, "invalid token") ||
		strings.Contains(msg, "invalid_token") ||
		strings.Contains(msg, "invalid_grant") ||
		strings.Contains(msg, "session not active")
}

// BalanceData represents the balance and validity details from Engsel API.
type BalanceData struct {
	Remaining float64 `json:"remaining"`
	ExpiredAt float64 `json:"expired_at"`
}

// QuotaDetailsData represents the package and quota response from Engsel API.
type QuotaDetailsData struct {
	Quotas []QuotaInfo `json:"quotas"`
}

// QuotaInfo represents an active package quota.
type QuotaInfo struct {
	Name              string        `json:"name"`
	QuotaCode         string        `json:"quota_code"`
	GroupCode         string        `json:"group_code"`
	GroupName         string        `json:"group_name"`
	FamilyCode        string        `json:"family_code"`
	PackageFamilyCode string        `json:"package_family_code"`
	ExpiredAt         float64       `json:"expired_at"`
	Benefits          []BenefitInfo `json:"benefits"`
}

// BenefitInfo represents an individual quota bucket (e.g. Utama, Lokal, Voice).
type BenefitInfo struct {
	ID        string  `json:"id"`
	Name      string  `json:"name"`
	DataType  string  `json:"data_type"`
	Remaining float64 `json:"remaining"`
	Total     float64 `json:"total"`
}

// EncryptedBody represents the envelope sent to and received from MyXL Engsel API.
type EncryptedBody struct {
	XData string `json:"xdata"`
	XTime int64  `json:"xtime"`
}
