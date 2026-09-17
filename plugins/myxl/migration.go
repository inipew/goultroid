package myxl

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

var _ database.SchemaInvariantMigration = migration001{}

type migration001 struct{}

func (migration001) ID() string          { return "myxl.001" }
func (migration001) Description() string { return "Persistent MyXL accounts and token storage" }
func (migration001) Checksum() string {
	return "3b81523612b636619cdbd544b705ea8401b04e6427f85b6b8178d6e26fe39942"
}
func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `
	CREATE TABLE IF NOT EXISTS myxl_accounts (
		msisdn TEXT PRIMARY KEY,
		alias TEXT NOT NULL DEFAULT '',
		is_active INTEGER NOT NULL DEFAULT 0,
		access_token TEXT NOT NULL DEFAULT '',
		id_token TEXT NOT NULL DEFAULT '',
		refresh_token TEXT NOT NULL DEFAULT '',
		subscriber_id TEXT NOT NULL DEFAULT '',
		subscription_type TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL
	);
	CREATE INDEX IF NOT EXISTS idx_myxl_accounts_active ON myxl_accounts(is_active);
	`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_accounts'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_accounts does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration002{}

type migration002 struct{}

func (migration002) ID() string          { return "myxl.002" }
func (migration002) Description() string { return "Persistent saved packages and bookmarks" }
func (migration002) Checksum() string {
	return "8f9361ad7611c05d15a5bb1d79435b675571694f585f8385bb691230e84cbb01"
}
func (migration002) LegacyVersions() []int { return nil }

func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `
	CREATE TABLE IF NOT EXISTS myxl_saved_packages (
		msisdn TEXT NOT NULL DEFAULT '',
		option_code TEXT NOT NULL,
		name TEXT NOT NULL,
		price INTEGER NOT NULL,
		family_code TEXT NOT NULL DEFAULT '',
		PRIMARY KEY (msisdn, option_code)
	);
	CREATE INDEX IF NOT EXISTS idx_myxl_saved_packages_msisdn ON myxl_saved_packages(msisdn);
	`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_saved_packages'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_saved_packages does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration003{}

type migration003 struct{}

func (migration003) ID() string          { return "myxl.003" }
func (migration003) Description() string { return "Persistent decoy target configurations" }
func (migration003) Checksum() string {
	return "9c74596b63c788647cfc1d9b3a4a753443831b14a27546cb50ec9580b2a95e02"
}
func (migration003) LegacyVersions() []int { return nil }

func (migration003) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `
	CREATE TABLE IF NOT EXISTS myxl_decoy_configs (
		key TEXT PRIMARY KEY,
		family_code TEXT NOT NULL,
		variant_code TEXT NOT NULL,
		order_no INTEGER NOT NULL,
		price INTEGER NOT NULL,
		option_code TEXT NOT NULL DEFAULT '',
		token_confirmation TEXT NOT NULL DEFAULT '',
		last_fetched_at INTEGER NOT NULL DEFAULT 0,
		is_enterprise INTEGER NOT NULL DEFAULT 0,
		migration_type TEXT NOT NULL DEFAULT 'NONE',
		updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
	);

	INSERT OR IGNORE INTO myxl_decoy_configs (key, family_code, variant_code, order_no, price, is_enterprise, migration_type) VALUES
	('default-balance', 'b0a20d74-0c54-4e3b-8f3f-01e7482e50bf', '719d093f-6f8d-46a4-8390-6a0003a172ea', 1, 889750, 1, 'NONE'),
	('default-qris', '580c1f94-7dc4-416e-96f6-8faf26567516', 'b50f954a-696e-46d0-8700-8e4d38521525', 27, 1000, 0, 'NONE'),
	('default-qris0', 'c6f2cd72-8b21-420a-b8d5-e0186afe5be6', 'b7cf278e-c989-4760-8a26-0a20c1a47a40', 11, 0, 1, 'NONE'),
	('prio-balance', '2512b72a-a3cd-4c70-a736-132cf2c1f0c0', 'cff298bd-8ec8-4696-b689-12407d36be15', 1, 999000, 0, 'NONE'),
	('prio-qris', '5dab52d5-6f02-4678-b72f-088396ceb113', 'bf84ad5b-87e9-4769-9bd2-2359535c05e4', 1, 2000, 1, 'PRIOH_TO_PRIO'),
	('prio-qris0', '5dab52d5-6f02-4678-b72f-088396ceb113', 'bf84ad5b-87e9-4769-9bd2-2359535c05e4', 1, 2000, 1, 'PRIOH_TO_PRIO');
	`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration003) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_decoy_configs'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_decoy_configs does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration004{}

type migration004 struct{}

func (migration004) ID() string          { return "myxl.004" }
func (migration004) Description() string { return "Persistent token expiration timestamp" }
func (migration004) Checksum() string {
	return "e9c6a3fc56679b01d475185bef9fd0d35c38b5e02f6a9b166b07005c1bbaa4e2"
}
func (migration004) LegacyVersions() []int { return nil }

func (migration004) Up(ctx context.Context, tx database.SQLExecutor) error {
	query := `ALTER TABLE myxl_accounts ADD COLUMN token_expires_at DATETIME NOT NULL DEFAULT '1970-01-01 00:00:00';`
	_, err := tx.ExecContext(ctx, query)
	return err
}

func (migration004) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM pragma_table_info('myxl_accounts') WHERE name='token_expires_at'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required column token_expires_at in table myxl_accounts does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration005{}

type migration005 struct{}

func (migration005) ID() string { return "myxl.005" }
func (migration005) Description() string {
	return "Persistent idempotency records for confirmed purchases"
}
func (migration005) Checksum() string {
	return "38e27ef761b1ea86cbe4945993e92c8484ea3cd90071b8c66a585356d72d6294"
}
func (migration005) LegacyVersions() []int { return nil }

func (migration005) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS myxl_purchase_requests (
			idempotency_key TEXT PRIMARY KEY,
			msisdn TEXT NOT NULL,
			option_code TEXT NOT NULL,
			payment_method TEXT NOT NULL,
			status TEXT NOT NULL,
			transaction_code TEXT NOT NULL DEFAULT '',
			message TEXT NOT NULL DEFAULT '',
			created_at DATETIME NOT NULL,
			updated_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_myxl_purchase_requests_account
		ON myxl_purchase_requests(msisdn, created_at);
	`)
	return err
}

func (migration005) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_purchase_requests'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_purchase_requests does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration006{}

type migration006 struct{}

func (migration006) ID() string { return "myxl.006" }
func (migration006) Description() string {
	return "Enforce a single active MyXL account"
}
func (migration006) Checksum() string {
	return "6df6f6c281a5c010f899261f950267540860c02124870bf85c8dcce28ef3077c"
}
func (migration006) LegacyVersions() []int { return nil }

func (migration006) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		UPDATE myxl_accounts
		SET is_active = 0
		WHERE is_active = 1
		  AND msisdn <> (
			SELECT msisdn
			FROM myxl_accounts
			WHERE is_active = 1
			ORDER BY updated_at DESC, created_at ASC, msisdn ASC
			LIMIT 1
		  );
		CREATE UNIQUE INDEX IF NOT EXISTS idx_myxl_accounts_single_active
		ON myxl_accounts(is_active) WHERE is_active = 1;
	`)
	return err
}

func (migration006) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='index' AND name='idx_myxl_accounts_single_active'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required unique active-account index does not exist")
	}
	return nil
}

var _ database.SchemaInvariantMigration = migration007{}

type migration007 struct{}

func (migration007) ID() string { return "myxl.007" }
func (migration007) Description() string {
	return "Persistent storage for active pending QRIS transactions with 5-minute expiration"
}
func (migration007) Checksum() string {
	return "f7c12024c590c2dbee5c55d5f19655104d0b5855a1029ca13bd087bd4e3bf1d2"
}
func (migration007) LegacyVersions() []int { return nil }

func (migration007) Up(ctx context.Context, tx database.SQLExecutor) error {
	_, err := tx.ExecContext(ctx, `
		CREATE TABLE IF NOT EXISTS myxl_pending_qris (
			transaction_code TEXT PRIMARY KEY,
			idempotency_key TEXT NOT NULL DEFAULT '',
			msisdn TEXT NOT NULL,
			option_code TEXT NOT NULL,
			package_name TEXT NOT NULL,
			price INTEGER NOT NULL,
			qr_code TEXT NOT NULL,
			status TEXT NOT NULL DEFAULT 'PENDING',
			created_at DATETIME NOT NULL,
			expires_at DATETIME NOT NULL
		);
		CREATE INDEX IF NOT EXISTS idx_myxl_pending_qris_lookup
		ON myxl_pending_qris(msisdn, status, expires_at);
	`)
	return err
}

func (migration007) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `SELECT count(*) FROM sqlite_master WHERE type='table' AND name='myxl_pending_qris'`).Scan(&count); err != nil {
		return err
	}
	if count == 0 {
		return fmt.Errorf("required table myxl_pending_qris does not exist")
	}
	return nil
}

// Migrations returns the database migrations for the myxl plugin.
func Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}, migration003{}, migration004{}, migration005{}, migration006{}, migration007{}}
}
