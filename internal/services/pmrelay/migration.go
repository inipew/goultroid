package pmrelay

import (
	"context"
	"fmt"

	"github.com/inipew/goultroid/internal/database"
)

type MigrationProvider struct{}

type migration001 struct{}
type migration002 struct{}
type migration003 struct{}
type migration004 struct{}

var _ database.SchemaInvariantMigration = migration001{}
var _ database.SchemaInvariantMigration = migration002{}
var _ database.SchemaInvariantMigration = migration003{}
var _ database.SchemaInvariantMigration = migration004{}

var schemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS pm_relay_mappings (
		owner_chat_id INTEGER NOT NULL CHECK (owner_chat_id > 0),
		owner_message_id INTEGER NOT NULL CHECK (owner_message_id > 0),
		visitor_user_id INTEGER NOT NULL CHECK (visitor_user_id > 0),
		visitor_message_id INTEGER NOT NULL CHECK (visitor_message_id > 0),
		created_at DATETIME NOT NULL,
		expires_at DATETIME NOT NULL,
		PRIMARY KEY (owner_chat_id, owner_message_id),
		UNIQUE (visitor_user_id, visitor_message_id)
	);`,
	`CREATE UNIQUE INDEX IF NOT EXISTS idx_pm_relay_mappings_visitor_message
		ON pm_relay_mappings(visitor_user_id, visitor_message_id);`,
	`CREATE INDEX IF NOT EXISTS idx_pm_relay_mappings_expiry
		ON pm_relay_mappings(expires_at, owner_chat_id, owner_message_id);`,
	`CREATE TABLE IF NOT EXISTS pm_relay_deliveries (
		direction TEXT NOT NULL CHECK (direction IN ('visitor_to_owner', 'owner_to_visitor')),
		source_chat_id INTEGER NOT NULL CHECK (source_chat_id > 0),
		source_message_id INTEGER NOT NULL CHECK (source_message_id > 0),
		target_chat_id INTEGER NOT NULL CHECK (target_chat_id > 0),
		random_id INTEGER NOT NULL CHECK (random_id <> 0),
		target_message_id INTEGER NOT NULL DEFAULT 0 CHECK (target_message_id >= 0),
		claim_id TEXT NOT NULL DEFAULT '',
		claim_expires_at DATETIME,
		attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts >= 0),
		last_error TEXT NOT NULL DEFAULT '',
		created_at DATETIME NOT NULL,
		updated_at DATETIME NOT NULL,
		delivered_at DATETIME,
		expires_at DATETIME NOT NULL,
		PRIMARY KEY (direction, source_chat_id, source_message_id),
		CHECK ((claim_id = '' AND claim_expires_at IS NULL) OR (claim_id <> '' AND claim_expires_at IS NOT NULL)),
		CHECK ((delivered_at IS NULL AND target_message_id = 0) OR (delivered_at IS NOT NULL AND target_message_id > 0)),
		CHECK (delivered_at IS NULL OR (claim_id = '' AND claim_expires_at IS NULL))
	);`,
	`CREATE INDEX IF NOT EXISTS idx_pm_relay_deliveries_claim
		ON pm_relay_deliveries(delivered_at, claim_expires_at, direction, source_chat_id, source_message_id);`,
	`CREATE INDEX IF NOT EXISTS idx_pm_relay_deliveries_expiry
		ON pm_relay_deliveries(expires_at, direction, source_chat_id, source_message_id);`,
	`CREATE TABLE IF NOT EXISTS assistant_audience_members (
		user_id INTEGER PRIMARY KEY CHECK (user_id > 0),
		sources INTEGER NOT NULL CHECK (sources > 0),
		first_seen_at DATETIME NOT NULL,
		last_seen_at DATETIME NOT NULL
	);`,
	`CREATE INDEX IF NOT EXISTS idx_assistant_audience_last_seen
		ON assistant_audience_members(last_seen_at, user_id);`,
}

func (MigrationProvider) Migrations() []database.Migration {
	return []database.Migration{migration001{}, migration002{}, migration003{}, migration004{}}
}

func (migration001) ID() string { return "pmrelay.001" }

func (migration001) Description() string {
	return "Durable Assistant PM relay mappings, delivery intents, and audience registry"
}

func (migration001) Checksum() string {
	return "810644be799770d3c05d21a61950682233ad10b18d566eec962436aab494beef"
}

func (migration001) LegacyVersions() []int { return nil }

func (migration001) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range schemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration001) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, table := range []string{
		"pm_relay_mappings",
		"pm_relay_deliveries",
		"assistant_audience_members",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'table' AND name = ?
		`, table).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required table %s does not exist", table)
		}
	}

	for _, index := range []string{
		"idx_pm_relay_mappings_visitor_message",
		"idx_pm_relay_mappings_expiry",
		"idx_pm_relay_deliveries_claim",
		"idx_pm_relay_deliveries_expiry",
		"idx_assistant_audience_last_seen",
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = 'index' AND name = ?
		`, index).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required index %s does not exist", index)
		}
	}
	return nil
}

var visitorBlockSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS pm_relay_visitor_blocks (
		visitor_user_id INTEGER PRIMARY KEY CHECK (visitor_user_id > 0),
		blocked_at DATETIME NOT NULL,
		reason TEXT NOT NULL DEFAULT ''
	);`,
	`CREATE INDEX IF NOT EXISTS idx_pm_relay_visitor_blocks_blocked_at
		ON pm_relay_visitor_blocks(blocked_at, visitor_user_id);`,
}

func (migration002) ID() string { return "pmrelay.002" }

func (migration002) Description() string {
	return "Durable Assistant PM relay visitor block policy"
}

func (migration002) Checksum() string {
	return "7a2e5961397d4c286dcb66bfb2335b385525919567644340bb3043a1fcebfbd0"
}

func (migration002) LegacyVersions() []int { return nil }

func (migration002) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range visitorBlockSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration002) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, item := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "pm_relay_visitor_blocks"},
		{kind: "index", name: "idx_pm_relay_visitor_blocks_blocked_at"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = ? AND name = ?
		`, item.kind, item.name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required %s %s does not exist", item.kind, item.name)
		}
	}
	return nil
}


var audienceMembershipOrderSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS assistant_audience_membership_order (
		sequence INTEGER PRIMARY KEY AUTOINCREMENT,
		user_id INTEGER NOT NULL UNIQUE CHECK (user_id > 0)
	);`,
	`CREATE INDEX IF NOT EXISTS idx_assistant_audience_membership_user
		ON assistant_audience_membership_order(user_id);`,
	`INSERT OR IGNORE INTO assistant_audience_membership_order (user_id)
		SELECT user_id FROM assistant_audience_members ORDER BY user_id ASC;`,
}

func (migration003) ID() string { return "pmrelay.003" }

func (migration003) Description() string {
	return "Stable keyset order for Assistant audience snapshots"
}

func (migration003) Checksum() string {
	return "f374580eb39d876668c11d295a709c2b264f339c8b983cf1b67845d7fda6b4a5"
}

func (migration003) LegacyVersions() []int { return nil }

func (migration003) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range audienceMembershipOrderSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration003) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	for _, item := range []struct {
		kind string
		name string
	}{
		{kind: "table", name: "assistant_audience_membership_order"},
		{kind: "index", name: "idx_assistant_audience_membership_user"},
	} {
		var count int
		if err := tx.QueryRowContext(ctx, `
			SELECT count(*) FROM sqlite_master
			WHERE type = ? AND name = ?
		`, item.kind, item.name).Scan(&count); err != nil {
			return err
		}
		if count != 1 {
			return fmt.Errorf("required %s %s does not exist", item.kind, item.name)
		}
	}
	return nil
}


var forceSubSchemaStatements = []string{
	`CREATE TABLE IF NOT EXISTS pm_relay_force_sub_config (
		singleton_id INTEGER PRIMARY KEY CHECK (singleton_id = 1),
		enabled INTEGER NOT NULL CHECK (enabled IN (0, 1)),
		channel_username TEXT NOT NULL DEFAULT '',
		join_url TEXT NOT NULL DEFAULT '',
		failure_mode TEXT NOT NULL CHECK (failure_mode IN ('closed', 'open')),
		revision INTEGER NOT NULL CHECK (revision > 0),
		updated_at DATETIME NOT NULL
	);`,
	`INSERT OR IGNORE INTO pm_relay_force_sub_config (
		singleton_id, enabled, channel_username, join_url, failure_mode, revision, updated_at
	) VALUES (1, 0, '', '', 'closed', 1, CURRENT_TIMESTAMP);`,
}

func (migration004) ID() string { return "pmrelay.004" }

func (migration004) Description() string {
	return "Durable Assistant PM relay force-sub policy"
}

func (migration004) Checksum() string {
	return "5ac099f30662bfa92e95bf475c25dd8507c12a35b42d51dbd500eb28ef0d6823"
}

func (migration004) LegacyVersions() []int { return nil }

func (migration004) Up(ctx context.Context, tx database.SQLExecutor) error {
	for _, statement := range forceSubSchemaStatements {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return err
		}
	}
	return nil
}

func (migration004) VerifySchema(ctx context.Context, tx database.SQLExecutor) error {
	var count int
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM sqlite_master
		WHERE type = 'table' AND name = 'pm_relay_force_sub_config'
	`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("required table pm_relay_force_sub_config does not exist")
	}
	if err := tx.QueryRowContext(ctx, `
		SELECT count(*) FROM pm_relay_force_sub_config
		WHERE singleton_id = 1
		  AND enabled IN (0, 1)
		  AND failure_mode IN ('closed', 'open')
		  AND revision > 0
	`).Scan(&count); err != nil {
		return err
	}
	if count != 1 {
		return fmt.Errorf("required force-sub singleton row does not exist")
	}
	return nil
}
