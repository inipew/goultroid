package myxl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// Repository defines operations for managing MyXL accounts, saved packages, and decoy targets.
type Repository interface {
	GetActive(ctx context.Context) (*Account, error)
	GetByMSISDN(ctx context.Context, msisdn string) (*Account, error)
	List(ctx context.Context) ([]*Account, error)
	Save(ctx context.Context, acc *Account) error
	SetActive(ctx context.Context, msisdn string) error
	SetAlias(ctx context.Context, identifier, alias string) error
	Delete(ctx context.Context, msisdn string) error

	// Saved packages
	SavePackage(ctx context.Context, pkg *SavedPackage) error
	GetSavedPackages(ctx context.Context, msisdn string) ([]*SavedPackage, error)
	GetSavedPackage(ctx context.Context, msisdn, optionCode string) (*SavedPackage, error)
	DeleteSavedPackage(ctx context.Context, msisdn, optionCode string) error

	// Decoy configurations
	GetDecoy(ctx context.Context, key string) (*DecoyConfig, error)
	UpsertDecoy(ctx context.Context, decoy *DecoyConfig) error
}

// SQLiteRepository implements Repository using *database.DB.
type SQLiteRepository struct {
	db *database.DB
}

// NewSQLiteRepository constructs a SQLiteRepository.
func NewSQLiteRepository(db *database.DB) *SQLiteRepository {
	return &SQLiteRepository{db: db}
}

// GetActive returns the currently active account, or nil if none is active.
func (r *SQLiteRepository) GetActive(ctx context.Context) (*Account, error) {
	query := `
	SELECT msisdn, alias, is_active, access_token, id_token, refresh_token,
	       subscriber_id, subscription_type, token_expires_at, created_at, updated_at
	FROM myxl_accounts
	WHERE is_active = 1
	LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query)
	acc, err := scanAccount(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get active account: %w", err)
	}
	return acc, nil
}

// GetByMSISDN returns an account matching the MSISDN or alias, or nil if not found.
func (r *SQLiteRepository) GetByMSISDN(ctx context.Context, identifier string) (*Account, error) {
	query := `
	SELECT msisdn, alias, is_active, access_token, id_token, refresh_token,
	       subscriber_id, subscription_type, token_expires_at, created_at, updated_at
	FROM myxl_accounts
	WHERE msisdn = ? OR alias = ?
	LIMIT 1
	`
	row := r.db.QueryRowContext(ctx, query, identifier, identifier)
	acc, err := scanAccount(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get account by identifier: %w", err)
	}
	return acc, nil
}

// List returns all registered accounts sorted by active status and creation time.
func (r *SQLiteRepository) List(ctx context.Context) ([]*Account, error) {
	query := `
	SELECT msisdn, alias, is_active, access_token, id_token, refresh_token,
	       subscriber_id, subscription_type, token_expires_at, created_at, updated_at
	FROM myxl_accounts
	ORDER BY is_active DESC, created_at ASC
	`
	rows, err := r.db.QueryContext(ctx, query)
	if err != nil {
		return nil, fmt.Errorf("failed to list accounts: %w", err)
	}
	defer rows.Close()

	var accounts []*Account
	for rows.Next() {
		acc, err := scanAccountRow(rows)
		if err != nil {
			return nil, fmt.Errorf("failed to scan account: %w", err)
		}
		accounts = append(accounts, acc)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("row iteration error: %w", err)
	}
	return accounts, nil
}

// Save inserts or updates an account. If no active account exists, sets this account as active.
func (r *SQLiteRepository) Save(ctx context.Context, acc *Account) error {
	now := time.Now().UTC()
	if acc.CreatedAt.IsZero() {
		acc.CreatedAt = now
	}
	acc.UpdatedAt = now

	// Check if any active account already exists
	var activeCount int
	_ = r.db.QueryRowContext(ctx, "SELECT count(*) FROM myxl_accounts WHERE is_active = 1").Scan(&activeCount)
	if activeCount == 0 {
		acc.IsActive = true
	}

	tokenExpiresAt := acc.TokenExpiresAt
	if tokenExpiresAt.IsZero() {
		tokenExpiresAt = time.Unix(0, 0).UTC()
	}

	query := `
	INSERT INTO myxl_accounts (
		msisdn, alias, is_active, access_token, id_token, refresh_token,
		subscriber_id, subscription_type, token_expires_at, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(msisdn) DO UPDATE SET
		alias = excluded.alias,
		is_active = CASE WHEN excluded.is_active = 1 THEN 1 ELSE myxl_accounts.is_active END,
		access_token = excluded.access_token,
		id_token = excluded.id_token,
		refresh_token = excluded.refresh_token,
		subscriber_id = excluded.subscriber_id,
		subscription_type = excluded.subscription_type,
		token_expires_at = excluded.token_expires_at,
		updated_at = excluded.updated_at
	`
	isActiveInt := 0
	if acc.IsActive {
		isActiveInt = 1
	}

	_, err := r.db.ExecContext(ctx, query,
		acc.MSISDN, acc.Alias, isActiveInt, acc.AccessToken, acc.IDToken, acc.RefreshToken,
		acc.SubscriberID, acc.SubscriptionType, tokenExpiresAt, acc.CreatedAt, acc.UpdatedAt,
	)
	if err != nil {
		return fmt.Errorf("failed to save account: %w", err)
	}
	return nil
}

// SetActive sets the specified account as active and deactivates all other accounts.
func (r *SQLiteRepository) SetActive(ctx context.Context, identifier string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	// Verify target account exists
	var msisdn string
	err = tx.QueryRowContext(ctx, "SELECT msisdn FROM myxl_accounts WHERE msisdn = ? OR alias = ?", identifier, identifier).Scan(&msisdn)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("account not found")
		}
		return fmt.Errorf("check account: %w", err)
	}

	// Deactivate all
	if _, err := tx.ExecContext(ctx, "UPDATE myxl_accounts SET is_active = 0"); err != nil {
		return fmt.Errorf("deactivate accounts: %w", err)
	}

	// Activate target
	if _, err := tx.ExecContext(ctx, "UPDATE myxl_accounts SET is_active = 1, updated_at = ? WHERE msisdn = ?", time.Now().UTC(), msisdn); err != nil {
		return fmt.Errorf("activate account: %w", err)
	}

	return tx.Commit()
}

// SetAlias updates the alias for an account identified by MSISDN or current alias.
func (r *SQLiteRepository) SetAlias(ctx context.Context, identifier, alias string) error {
	query := `
	UPDATE myxl_accounts
	SET alias = ?, updated_at = ?
	WHERE msisdn = ? OR alias = ?
	`
	res, err := r.db.ExecContext(ctx, query, strings.TrimSpace(alias), time.Now().UTC(), identifier, identifier)
	if err != nil {
		return fmt.Errorf("failed to set alias: %w", err)
	}
	rows, err := res.RowsAffected()
	if err != nil || rows == 0 {
		return errors.New("account not found")
	}
	return nil
}

// Delete removes an account. If the removed account was active, another account is promoted to active.
func (r *SQLiteRepository) Delete(ctx context.Context, identifier string) error {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	var msisdn string
	var wasActive int
	err = tx.QueryRowContext(ctx, "SELECT msisdn, is_active FROM myxl_accounts WHERE msisdn = ? OR alias = ?", identifier, identifier).Scan(&msisdn, &wasActive)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return errors.New("account not found")
		}
		return fmt.Errorf("check account: %w", err)
	}

	if _, err := tx.ExecContext(ctx, "DELETE FROM myxl_accounts WHERE msisdn = ?", msisdn); err != nil {
		return fmt.Errorf("delete account: %w", err)
	}

	// If deleted account was active, promote the oldest remaining account to active
	if wasActive == 1 {
		_, _ = tx.ExecContext(ctx, `
			UPDATE myxl_accounts SET is_active = 1
			WHERE msisdn = (SELECT msisdn FROM myxl_accounts ORDER BY created_at ASC LIMIT 1)
		`)
	}

	return tx.Commit()
}

func scanAccount(row *sql.Row) (*Account, error) {
	var acc Account
	var isActiveInt int
	err := row.Scan(
		&acc.MSISDN, &acc.Alias, &isActiveInt, &acc.AccessToken, &acc.IDToken, &acc.RefreshToken,
		&acc.SubscriberID, &acc.SubscriptionType, &acc.TokenExpiresAt, &acc.CreatedAt, &acc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	acc.IsActive = isActiveInt == 1
	return &acc, nil
}

func scanAccountRow(rows *sql.Rows) (*Account, error) {
	var acc Account
	var isActiveInt int
	err := rows.Scan(
		&acc.MSISDN, &acc.Alias, &isActiveInt, &acc.AccessToken, &acc.IDToken, &acc.RefreshToken,
		&acc.SubscriberID, &acc.SubscriptionType, &acc.TokenExpiresAt, &acc.CreatedAt, &acc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	acc.IsActive = isActiveInt == 1
	return &acc, nil
}

// SavePackage persists or updates a bookmarked package.
func (r *SQLiteRepository) SavePackage(ctx context.Context, pkg *SavedPackage) error {
	if pkg == nil || pkg.OptionCode == "" {
		return errors.New("invalid package: option_code is required")
	}
	query := `
	INSERT INTO myxl_saved_packages (msisdn, option_code, name, price, family_code)
	VALUES (?, ?, ?, ?, ?)
	ON CONFLICT(msisdn, option_code) DO UPDATE SET
		name = excluded.name,
		price = excluded.price,
		family_code = excluded.family_code
	`
	_, err := r.db.ExecContext(ctx, query, pkg.MSISDN, pkg.OptionCode, pkg.Name, pkg.Price, pkg.FamilyCode)
	if err != nil {
		return fmt.Errorf("failed to save package: %w", err)
	}
	return nil
}

// GetSavedPackages retrieves all saved packages for an account (or global packages if msisdn is "").
func (r *SQLiteRepository) GetSavedPackages(ctx context.Context, msisdn string) ([]*SavedPackage, error) {
	query := `
	SELECT msisdn, option_code, name, price, family_code
	FROM myxl_saved_packages
	WHERE msisdn = ? OR msisdn = ''
	ORDER BY rowid ASC
	`
	rows, err := r.db.QueryContext(ctx, query, msisdn)
	if err != nil {
		return nil, fmt.Errorf("failed to list saved packages: %w", err)
	}
	defer rows.Close()

	var result []*SavedPackage
	for rows.Next() {
		var p SavedPackage
		if err := rows.Scan(&p.MSISDN, &p.OptionCode, &p.Name, &p.Price, &p.FamilyCode); err != nil {
			return nil, fmt.Errorf("failed to scan saved package: %w", err)
		}
		result = append(result, &p)
	}
	return result, rows.Err()
}

// GetSavedPackage retrieves a single saved package by MSISDN and option code.
func (r *SQLiteRepository) GetSavedPackage(ctx context.Context, msisdn, optionCode string) (*SavedPackage, error) {
	query := `
	SELECT msisdn, option_code, name, price, family_code
	FROM myxl_saved_packages
	WHERE (msisdn = ? OR msisdn = '') AND option_code = ?
	LIMIT 1
	`
	var p SavedPackage
	err := r.db.QueryRowContext(ctx, query, msisdn, optionCode).Scan(&p.MSISDN, &p.OptionCode, &p.Name, &p.Price, &p.FamilyCode)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get saved package: %w", err)
	}
	return &p, nil
}

// DeleteSavedPackage deletes a bookmarked package.
func (r *SQLiteRepository) DeleteSavedPackage(ctx context.Context, msisdn, optionCode string) error {
	query := `DELETE FROM myxl_saved_packages WHERE (msisdn = ? OR msisdn = '') AND option_code = ?`
	res, err := r.db.ExecContext(ctx, query, msisdn, optionCode)
	if err != nil {
		return fmt.Errorf("failed to delete saved package: %w", err)
	}
	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		return errors.New("saved package not found")
	}
	return nil
}

// GetDecoy retrieves a decoy target configuration by key (e.g. "default-balance", "default-qris").
func (r *SQLiteRepository) GetDecoy(ctx context.Context, key string) (*DecoyConfig, error) {
	query := `
	SELECT key, family_code, variant_code, order_no, price, option_code,
	       token_confirmation, last_fetched_at, is_enterprise, migration_type, updated_at
	FROM myxl_decoy_configs
	WHERE key = ?
	LIMIT 1
	`
	var d DecoyConfig
	var isEntInt int
	err := r.db.QueryRowContext(ctx, query, key).Scan(
		&d.Key, &d.FamilyCode, &d.VariantCode, &d.OrderNo, &d.Price, &d.OptionCode,
		&d.TokenConfirmation, &d.LastFetchedAt, &isEntInt, &d.MigrationType, &d.UpdatedAt,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, fmt.Errorf("failed to get decoy config: %w", err)
	}
	d.IsEnterprise = isEntInt == 1
	return &d, nil
}

// UpsertDecoy creates or updates a decoy configuration entry.
func (r *SQLiteRepository) UpsertDecoy(ctx context.Context, decoy *DecoyConfig) error {
	if decoy == nil || decoy.Key == "" {
		return errors.New("invalid decoy config: key is required")
	}
	query := `
	INSERT INTO myxl_decoy_configs (
		key, family_code, variant_code, order_no, price, option_code,
		token_confirmation, last_fetched_at, is_enterprise, migration_type, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
	ON CONFLICT(key) DO UPDATE SET
		family_code = excluded.family_code,
		variant_code = excluded.variant_code,
		order_no = excluded.order_no,
		price = excluded.price,
		option_code = excluded.option_code,
		token_confirmation = excluded.token_confirmation,
		last_fetched_at = excluded.last_fetched_at,
		is_enterprise = excluded.is_enterprise,
		migration_type = excluded.migration_type,
		updated_at = CURRENT_TIMESTAMP
	`
	isEntInt := 0
	if decoy.IsEnterprise {
		isEntInt = 1
	}
	_, err := r.db.ExecContext(
		ctx, query,
		decoy.Key, decoy.FamilyCode, decoy.VariantCode, decoy.OrderNo, decoy.Price,
		decoy.OptionCode, decoy.TokenConfirmation, decoy.LastFetchedAt, isEntInt,
		decoy.MigrationType,
	)
	if err != nil {
		return fmt.Errorf("failed to upsert decoy config: %w", err)
	}
	return nil
}
