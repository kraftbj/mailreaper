package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// DB wraps a *sql.DB with MailReaper-specific helpers.
type DB struct {
	*sql.DB
}

// Open creates the directory for path if needed, opens an SQLite database with
// WAL journal mode and a 5-second busy timeout, enables foreign key enforcement,
// and runs all schema migrations.
func Open(path string) (*DB, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("db: create directory: %w", err)
	}

	dsn := path + "?_journal_mode=WAL&_busy_timeout=5000"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
	}

	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("db: enable foreign keys: %w", err)
	}

	d := &DB{sqlDB}
	if err := d.migrate(); err != nil {
		d.Close()
		return nil, fmt.Errorf("db: migrate: %w", err)
	}

	return d, nil
}

// Close closes the underlying database connection.
func (d *DB) Close() error {
	return d.DB.Close()
}

// UpsertAccount inserts or updates an account record.
func (d *DB) UpsertAccount(id, name string) error {
	_, err := d.Exec(`
		INSERT INTO accounts (id, name)
		VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name = excluded.name
	`, id, name)
	if err != nil {
		return fmt.Errorf("db: upsert account: %w", err)
	}
	return nil
}

// UpdateAccountScan records the results of the latest scan for an account.
func (d *DB) UpdateAccountScan(id string, messageCount int) error {
	_, err := d.Exec(`
		UPDATE accounts
		SET last_scan_at = ?, last_scan_message_count = ?
		WHERE id = ?
	`, time.Now().UTC(), messageCount, id)
	if err != nil {
		return fmt.Errorf("db: update account scan: %w", err)
	}
	return nil
}

// migrate creates all tables if they do not already exist.
func (d *DB) migrate() error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id                       TEXT PRIMARY KEY,
			name                     TEXT,
			last_scan_at             TIMESTAMP,
			last_scan_message_count  INT
		)`,

		`CREATE TABLE IF NOT EXISTS rules (
			id                  TEXT PRIMARY KEY,
			name                TEXT,
			enabled             BOOL,
			priority            INT,
			builtin             BOOL,
			match_config        JSON,
			expiration_config   JSON,
			action              TEXT,
			destination_folder  TEXT,
			next_rule_id        TEXT REFERENCES rules(id) ON DELETE SET NULL,
			grace_period_days   INT,
			created_at          TIMESTAMP,
			updated_at          TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS verdicts (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id          TEXT REFERENCES accounts(id),
			message_id_header   TEXT UNIQUE,
			subject             TEXT,
			sender              TEXT,
			sent_at             TIMESTAMP,
			rule_id             TEXT REFERENCES rules(id),
			status              TEXT DEFAULT 'pending',
			destination_folder  TEXT,
			expires_at          TIMESTAMP,
			reason              TEXT,
			confidence          REAL,
			evaluated_at        TIMESTAMP,
			acted_at            TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS activity_log (
			id                  INTEGER PRIMARY KEY AUTOINCREMENT,
			type                TEXT,
			account_id          TEXT,
			message_id_header   TEXT,
			subject             TEXT,
			sender              TEXT,
			rule_name           TEXT,
			destination         TEXT,
			reason              TEXT,
			confidence          REAL,
			created_at          TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS llm_cache (
			message_id_header   TEXT PRIMARY KEY,
			verdict             JSON,
			cached_at           TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS training_examples (
			id            INTEGER PRIMARY KEY AUTOINCREMENT,
			category      TEXT,
			subject       TEXT,
			sender        TEXT,
			body_snippet  TEXT,
			label         TEXT,
			source        TEXT DEFAULT 'dashboard',
			created_at    TIMESTAMP
		)`,

		`CREATE TABLE IF NOT EXISTS categories (
			id           TEXT PRIMARY KEY,
			name         TEXT,
			folder_name  TEXT,
			icon         TEXT,
			color        TEXT,
			created_at   TIMESTAMP
		)`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}

	return nil
}
