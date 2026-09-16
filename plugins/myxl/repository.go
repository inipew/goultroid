package myxl

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/inipew/goultroid/internal/database"
)

// Repository defines operations for managing MyXL accounts.
type Repository interface {
	GetActive(ctx context.Context) (*Account, error)
	GetByMSISDN(ctx context.Context, msisdn string) (*Account, error)
	List(ctx context.Context) ([]*Account, error)
	Save(ctx context.Context, acc *Account) error
	SetActive(ctx context.Context, msisdn string) error
	Delete(ctx context.Context, msisdn string) error
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
	       subscriber_id, subscription_type, created_at, updated_at
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
	       subscriber_id, subscription_type, created_at, updated_at
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
	       subscriber_id, subscription_type, created_at, updated_at
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

	query := `
	INSERT INTO myxl_accounts (
		msisdn, alias, is_active, access_token, id_token, refresh_token,
		subscriber_id, subscription_type, created_at, updated_at
	) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	ON CONFLICT(msisdn) DO UPDATE SET
		alias = excluded.alias,
		is_active = CASE WHEN excluded.is_active = 1 THEN 1 ELSE myxl_accounts.is_active END,
		access_token = excluded.access_token,
		id_token = excluded.id_token,
		refresh_token = excluded.refresh_token,
		subscriber_id = excluded.subscriber_id,
		subscription_type = excluded.subscription_type,
		updated_at = excluded.updated_at
	`
	isActiveInt := 0
	if acc.IsActive {
		isActiveInt = 1
	}

	_, err := r.db.ExecContext(ctx, query,
		acc.MSISDN, acc.Alias, isActiveInt, acc.AccessToken, acc.IDToken, acc.RefreshToken,
		acc.SubscriberID, acc.SubscriptionType, acc.CreatedAt, acc.UpdatedAt,
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
		&acc.SubscriberID, &acc.SubscriptionType, &acc.CreatedAt, &acc.UpdatedAt,
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
		&acc.SubscriberID, &acc.SubscriptionType, &acc.CreatedAt, &acc.UpdatedAt,
	)
	if err != nil {
		return nil, err
	}
	acc.IsActive = isActiveInt == 1
	return &acc, nil
}
