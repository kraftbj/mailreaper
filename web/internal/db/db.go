package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"time"

	_ "modernc.org/sqlite"
)

// timeLayouts lists the formats tried when parsing timestamps back from SQLite.
// modernc.org/sqlite stores Go time.Time values as strings; the exact format
// varies (UTC vs offset, nanoseconds vs not).
var timeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999 -0700 -0700", // modernc.org/sqlite time.Time output
	"2006-01-02 15:04:05 -0700 -0700",
	"2006-01-02 15:04:05.999999999 -0700 MST", // Go default time.String()
	"2006-01-02 15:04:05 -0700 MST",
	"2006-01-02 15:04:05.999999999Z07:00",
	"2006-01-02 15:04:05Z07:00",
	"2006-01-02 15:04:05.999999999+00:00",
	"2006-01-02 15:04:05+00:00",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

// parseDBTime parses a time string stored by SQLite.
func parseDBTime(s string) (time.Time, error) {
	for _, layout := range timeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("db: unrecognized time format: %q", s)
}

// parseDBNullTime parses an optional time string from SQLite.
func parseDBNullTime(s sql.NullString) (*time.Time, error) {
	if !s.Valid || s.String == "" {
		return nil, nil
	}
	t, err := parseDBTime(s.String)
	if err != nil {
		return nil, err
	}
	return &t, nil
}

// formatDBTime formats a time.Time as RFC3339Nano for consistent SQLite storage.
// Nanosecond precision ensures correct ordering for entries created in rapid succession.
func formatDBTime(t time.Time) string {
	return t.UTC().Format(time.RFC3339Nano)
}

// formatDBNullTime formats an optional time for SQLite storage.
func formatDBNullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return formatDBTime(*t)
}

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
	`, formatDBTime(time.Now().UTC()), messageCount, id)
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

		`CREATE TABLE IF NOT EXISTS declined_auto_rules (
			id          TEXT PRIMARY KEY,
			declined_at TIMESTAMP
		)`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}

	return nil
}
