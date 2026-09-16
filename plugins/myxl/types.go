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

// SavedPackage represents a bookmarked package for quick re-purchase.
type SavedPackage struct {
	MSISDN     string `json:"msisdn"`
	OptionCode string `json:"option_code"`
	Name       string `json:"name"`
	Price      int64  `json:"price"`
	FamilyCode string `json:"family_code"`
}

// DecoyConfig represents a configured decoy target in the database.
type DecoyConfig struct {
	Key               string `json:"key"`
	FamilyCode        string `json:"family_code"`
	VariantCode       string `json:"variant_code"`
	OrderNo           int64  `json:"order_no"`
	Price             int64  `json:"price"`
	OptionCode        string `json:"option_code"`
	TokenConfirmation string `json:"token_confirmation"`
	LastFetchedAt     int64  `json:"last_fetched_at"`
	IsEnterprise      bool   `json:"is_enterprise"`
	MigrationType     string `json:"migration_type"`
	UpdatedAt         string `json:"updated_at"`
}

// PackageFamily represents metadata about a package group / family.
type PackageFamily struct {
	Name              string `json:"name"`
	PackageFamilyCode string `json:"package_family_code"`
}

// PackageOption represents an individual purchaseable package within a variant.
type PackageOption struct {
	Name              string  `json:"name"`
	PackageOptionCode string  `json:"package_option_code"`
	Price             float64 `json:"price"`
}

// PackageVariant represents a category variant containing package options.
type PackageVariant struct {
	Name               string          `json:"name"`
	PackageVariantCode string          `json:"package_variant_code"`
	PackageOptions     []PackageOption `json:"package_options"`
}

// PackageListResponse represents the response when querying packages by family.
type PackageListResponse struct {
	PackageFamily   PackageFamily    `json:"package_family"`
	PackageVariants []PackageVariant `json:"package_variants"`
}

// PackageDetailsData represents the response for option detail lookup.
type PackageDetailsData struct {
	PackageFamily     PackageFamily  `json:"package_family"`
	PackageOption     *PackageOption `json:"package_option,omitempty"`
	TokenConfirmation string         `json:"token_confirmation"`
}

// PaymentMethodsOptionData represents the response from payment-methods-option.
type PaymentMethodsOptionData struct {
	TokenPayment  string  `json:"token_payment"`
	PaymentFor    string  `json:"payment_for"`
	PaymentMethod string  `json:"payment_method"`
	Price         float64 `json:"price"`
	Timestamp     float64 `json:"timestamp"`
}

// PurchaseItem represents an individual item in a settlement payload.
type PurchaseItem struct {
	ItemCode          string `json:"item_code"`
	ItemPrice         int64  `json:"item_price"`
	ItemName          string `json:"item_name,omitempty"`
	ProductType       string `json:"product_type,omitempty"`
	Tax               int64  `json:"tax"`
	TokenConfirmation string `json:"token_confirmation,omitempty"`
}

// AutobuyThresholdSetting is used in settlement requests.
type AutobuyThresholdSetting struct {
	Label string `json:"label"`
	Type  string `json:"type"`
	Value int64  `json:"value"`
}

// AkrabPayload is used in settlement requests.
type AkrabPayload struct {
	AkrabMembers     []any  `json:"akrab_members"`
	AkrabParentAlias string `json:"akrab_parent_alias"`
	Members          []any  `json:"members"`
}

// AutobuyPayload is used in settlement requests.
type AutobuyPayload struct {
	IsUsingAutobuy          bool                    `json:"is_using_autobuy"`
	ActivatedAutobuyCode    string                  `json:"activated_autobuy_code"`
	AutobuyThresholdSetting AutobuyThresholdSetting `json:"autobuy_threshold_setting"`
}

// BalanceAdditionalData holds extra parameters for balance settlements.
type BalanceAdditionalData struct {
	OriginalPrice         int64  `json:"original_price"`
	IsSpendLimitTemporary bool   `json:"is_spend_limit_temporary"`
	MigrationType         string `json:"migration_type"`
	AkrabM2MGroupID       string `json:"akrab_m2m_group_id"`
	SpendLimitAmount      int64  `json:"spend_limit_amount"`
	IsSpendLimit          bool   `json:"is_spend_limit"`
	MissionID             string `json:"mission_id"`
	Tax                   int64  `json:"tax"`
	QuotaBonus            int64  `json:"quota_bonus"`
	Cashtag               string `json:"cashtag"`
	IsFamilyPlan          bool   `json:"is_family_plan"`
	ComboDetails          []any  `json:"combo_details"`
	IsSwitchPlan          bool   `json:"is_switch_plan"`
	DiscountRecurring     int64  `json:"discount_recurring"`
	IsAkrabM2M            bool   `json:"is_akrab_m2m"`
	BalanceType           string `json:"balance_type"`
	HasBonus              bool   `json:"has_bonus"`
	DiscountPromo         int64  `json:"discount_promo"`
}

// SettlementBalanceRequest represents the request payload for balance settlements.
type SettlementBalanceRequest struct {
	TotalDiscount             int64                   `json:"total_discount"`
	IsEnterprise              bool                    `json:"is_enterprise"`
	PaymentToken              string                  `json:"payment_token"`
	TokenPayment              string                  `json:"token_payment"`
	ActivatedAutobuyCode      string                  `json:"activated_autobuy_code"`
	CCPaymentType             string                  `json:"cc_payment_type"`
	IsMyXLWallet              bool                    `json:"is_myxl_wallet"`
	PIN                       string                  `json:"pin"`
	EWalletPromoID            string                  `json:"ewallet_promo_id"`
	Members                   []any                   `json:"members"`
	TotalFee                  int64                   `json:"total_fee"`
	Fingerprint               string                  `json:"fingerprint"`
	AutobuyThresholdSetting   AutobuyThresholdSetting `json:"autobuy_threshold_setting"`
	IsUsePoint                bool                    `json:"is_use_point"`
	Lang                      string                  `json:"lang"`
	PaymentMethod             string                  `json:"payment_method"`
	Timestamp                 int64                   `json:"timestamp"`
	PointsGained              int64                   `json:"points_gained"`
	CanTriggerRating          bool                    `json:"can_trigger_rating"`
	AkrabMembers              []any                   `json:"akrab_members"`
	AkrabParentAlias          string                  `json:"akrab_parent_alias"`
	ReferralUniqueCode        string                  `json:"referral_unique_code"`
	Coupon                    string                  `json:"coupon"`
	PaymentFor                string                  `json:"payment_for"`
	WithUpsell                bool                    `json:"with_upsell"`
	TopupNumber               string                  `json:"topup_number"`
	StageToken                string                  `json:"stage_token"`
	AuthenticationID          string                  `json:"authentication_id"`
	EncryptedPaymentToken     string                  `json:"encrypted_payment_token"`
	Token                     string                  `json:"token"`
	TokenConfirmation         string                  `json:"token_confirmation"`
	AccessToken               string                  `json:"access_token"`
	WalletNumber              string                  `json:"wallet_number"`
	EncryptedAuthenticationID string                  `json:"encrypted_authentication_id"`
	AdditionalData            BalanceAdditionalData   `json:"additional_data"`
	TotalAmount               int64                   `json:"total_amount"`
	IsUsingAutobuy            bool                    `json:"is_using_autobuy"`
	Items                     []PurchaseItem          `json:"items"`
}

// SettlementMultipaymentRequest represents the request payload for e-wallet settlements.
type SettlementMultipaymentRequest struct {
	Akrab             AkrabPayload   `json:"akrab"`
	CanTriggerRating  bool           `json:"can_trigger_rating"`
	TotalDiscount     int64          `json:"total_discount"`
	Coupon            string         `json:"coupon"`
	PaymentFor        string         `json:"payment_for"`
	TopupNumber       string         `json:"topup_number"`
	IsEnterprise      bool           `json:"is_enterprise"`
	Autobuy           AutobuyPayload `json:"autobuy"`
	CCPaymentType     string         `json:"cc_payment_type"`
	AccessToken       string         `json:"access_token"`
	IsMyXLWallet      bool           `json:"is_myxl_wallet"`
	WalletNumber      string         `json:"wallet_number"`
	AdditionalData    map[string]any `json:"additional_data"`
	TotalAmount       int64          `json:"total_amount"`
	TotalFee          int64          `json:"total_fee"`
	IsUsePoint        bool           `json:"is_use_point"`
	Lang              string         `json:"lang"`
	Items             []PurchaseItem `json:"items"`
	VerificationToken string         `json:"verification_token"`
	PaymentMethod     string         `json:"payment_method"`
	Timestamp         int64          `json:"timestamp"`
}

// QrisAdditionalData holds extra parameters for QRIS settlements.
type QrisAdditionalData struct {
	OriginalPrice         int64  `json:"original_price"`
	IsSpendLimitTemporary bool   `json:"is_spend_limit_temporary"`
	MigrationType         string `json:"migration_type"`
	SpendLimitAmount      int64  `json:"spend_limit_amount"`
	IsSpendLimit          bool   `json:"is_spend_limit"`
	Tax                   int64  `json:"tax"`
	BenefitType           string `json:"benefit_type"`
	QuotaBonus            int64  `json:"quota_bonus"`
	Cashtag               string `json:"cashtag"`
	IsFamilyPlan          bool   `json:"is_family_plan"`
	ComboDetails          []any  `json:"combo_details"`
	IsSwitchPlan          bool   `json:"is_switch_plan"`
	DiscountRecurring     int64  `json:"discount_recurring"`
	HasBonus              bool   `json:"has_bonus"`
	DiscountPromo         int64  `json:"discount_promo"`
}

// SettlementQrisRequest represents the request payload for QRIS settlements.
type SettlementQrisRequest struct {
	Akrab             AkrabPayload       `json:"akrab"`
	CanTriggerRating  bool               `json:"can_trigger_rating"`
	TotalDiscount     int64              `json:"total_discount"`
	Coupon            string             `json:"coupon"`
	PaymentFor        string             `json:"payment_for"`
	TopupNumber       string             `json:"topup_number"`
	StageToken        string             `json:"stage_token"`
	IsEnterprise      bool               `json:"is_enterprise"`
	Autobuy           AutobuyPayload     `json:"autobuy"`
	AccessToken       string             `json:"access_token"`
	IsMyXLWallet      bool               `json:"is_myxl_wallet"`
	AdditionalData    QrisAdditionalData `json:"additional_data"`
	TotalAmount       int64              `json:"total_amount"`
	TotalFee          int64              `json:"total_fee"`
	IsUsePoint        bool               `json:"is_use_point"`
	Lang              string             `json:"lang"`
	Items             []PurchaseItem     `json:"items"`
	VerificationToken string             `json:"verification_token"`
	PaymentMethod     string             `json:"payment_method"`
	Timestamp         int64              `json:"timestamp"`
}

// SettlementData represents data returned on settlement success.
type SettlementData struct {
	TransactionCode string `json:"transaction_code"`
	Deeplink        string `json:"deeplink"`
}

// QrisPendingDetailData represents QR code response for pending QRIS transactions.
type QrisPendingDetailData struct {
	QRCode string `json:"qr_code"`
}

// SettlementResult represents the unified outcome of a purchase attempt.
type SettlementResult struct {
	IsSuccess       bool   `json:"is_success"`
	Status          string `json:"status"`
	Message         string `json:"message"`
	TransactionCode string `json:"transaction_code,omitempty"`
	Deeplink        string `json:"deeplink,omitempty"`
	QRCode          string `json:"qr_code,omitempty"`
}
