package db

import (
	"database/sql"
	"errors"
	"fmt"
	"log"
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

	/* modernc.org/sqlite takes per-connection pragmas as _pragma=name(value);
	it silently ignores the _journal_mode / _busy_timeout spellings used by
	mattn/go-sqlite3. Setting them in the DSN applies them to every pooled
	connection, which a one-off PRAGMA Exec does not. */
	dsn := path + "?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("db: open: %w", err)
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
			message_id_header TEXT NOT NULL,
			cache_key         TEXT NOT NULL DEFAULT '',
			verdict           JSON NOT NULL,
			cached_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (message_id_header, cache_key)
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

		`CREATE TABLE IF NOT EXISTS schema_meta (
			key   TEXT PRIMARY KEY,
			value TEXT
		)`,

		`CREATE TABLE IF NOT EXISTS placements (
			message_id_header TEXT NOT NULL,
			folder            TEXT NOT NULL,
			placed_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (message_id_header, folder)
		)`,
	}

	for _, stmt := range stmts {
		if _, err := d.Exec(stmt); err != nil {
			return fmt.Errorf("exec migration: %w", err)
		}
	}

	if err := d.applyOneShotMigrations(); err != nil {
		return fmt.Errorf("apply one-shot migrations: %w", err)
	}

	return nil
}

// applyOneShotMigrations runs idempotent data migrations that should only
// take effect once per database. The schema_meta table tracks which have
// already run.
func (d *DB) applyOneShotMigrations() error {
	// Exactly one of stmt or fn must be set; the loop below enforces this
	// and errors out on either both-set or neither-set rather than silently
	// running d.Exec("") and recording the migration as applied anyway.
	type oneShot struct {
		key  string
		stmt string
		// fn, when set, runs instead of stmt. Used by migrations that need
		// more than a single unconditional statement (e.g. probing the
		// schema first).
		fn func(*DB) error
	}
	migrations := []oneShot{
		// 2026-04-29 expiry redesign: the multi-classify and analysis prompts
		// changed shape. Cached verdicts written before this change carry a
		// now-meaningless "expired" boolean. Flush the cache so the next scan
		// repopulates it under the new schema. See design doc:
		// ~/.gstack/projects/kraftbj-mailreaper/kraft-web-rewrite-design-20260429-081906.md
		{key: "v2_expiry_redesign_cache_flush", stmt: `DELETE FROM llm_cache`},

		/* 2026-08-12: canonicalExpiredFolder() (scanner.go) resolves the
		"Expired" destination by looking for a category whose id is
		literally "expired". Nothing ever created one, so past-deadline
		mail fell back to whichever triage folder the matching classify
		rule owned, and SweepDeferredExpiries returned early on every
		run. Seed it; ON CONFLICT DO NOTHING preserves a user's own
		"expired" category if they already created one. */
		{key: "v3_seed_expired_category", stmt: `
			INSERT INTO categories (id, name, folder_name, icon, color, created_at)
			VALUES ('expired', 'Expired', 'Expired', '⏰', '#888888', datetime('now'))
			ON CONFLICT(id) DO NOTHING
		`},

		/* 2026-08-12: llm_cache keyed on message_id_header alone, so a model
		swap left the previous model's verdicts authoritative, and the
		analysis and classify prompt paths overwrote each other's row for the
		same message. Rebuild with a (message_id_header, cache_key) primary
		key. Fresh databases already have this shape from the base schema
		(see the CREATE TABLE above), so migrateLLMCacheCompositeKey probes
		before touching anything. */
		{key: "v4_llm_cache_composite_key", fn: (*DB).migrateLLMCacheCompositeKey},

		/* 2026-08-12: a durable record of every message MailReaper moved on
		its own, consulted by DetectManualClassifications so a full verdict
		clear (ClearAllVerdicts / POST /api/rescan) can no longer be read
		back as "the user filed this" for a message MailReaper placed there
		itself. Fresh databases already have the placements table from the
		base schema (see the CREATE TABLE above), so migratePlacements
		probes before creating anything. */
		{key: "v5_placements", fn: (*DB).migratePlacements},
	}

	for _, m := range migrations {
		var existing string
		err := d.QueryRow(`SELECT value FROM schema_meta WHERE key = ?`, m.key).Scan(&existing)
		if err == nil {
			continue // already applied
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("check schema_meta %q: %w", m.key, err)
		}

		switch {
		case m.fn != nil && m.stmt != "":
			return fmt.Errorf("migration %q: both stmt and fn set; exactly one is allowed", m.key)
		case m.fn != nil:
			if err := m.fn(d); err != nil {
				return fmt.Errorf("run migration %q: %w", m.key, err)
			}
		case m.stmt != "":
			if _, err := d.Exec(m.stmt); err != nil {
				return fmt.Errorf("run migration %q: %w", m.key, err)
			}
		default:
			return fmt.Errorf("migration %q: neither stmt nor fn set", m.key)
		}
		if _, err := d.Exec(`INSERT INTO schema_meta (key, value) VALUES (?, ?)`, m.key, time.Now().UTC().Format(time.RFC3339)); err != nil {
			return fmt.Errorf("record migration %q: %w", m.key, err)
		}
		log.Printf("db: applied one-shot migration %q", m.key)
	}
	return nil
}

// migrateLLMCacheCompositeKey rebuilds llm_cache with a (message_id_header,
// cache_key) primary key so the analysis and classify prompt paths, and
// answers from different models, coexist instead of overwriting each other.
// Fresh databases already have this shape from the base schema, so the
// migration probes PRAGMA table_info for the cache_key column before doing
// anything: applyOneShotMigrations runs after the CREATE TABLE block, and an
// unconditional rebuild would run against a table that is already correct.
func (d *DB) migrateLLMCacheCompositeKey() error {
	var hasCacheKey bool
	rows, err := d.Query(`PRAGMA table_info(llm_cache)`)
	if err != nil {
		return fmt.Errorf("probe llm_cache schema: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, ctype string
		var notnull int
		var dflt any
		var pk int
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return fmt.Errorf("scan llm_cache schema: %w", err)
		}
		if name == "cache_key" {
			hasCacheKey = true
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("iterate llm_cache schema: %w", err)
	}
	if hasCacheKey {
		return nil // fresh DB, or already migrated
	}

	/* Existing rows came from a single model and the analysis prompt path
	only. Drop them rather than guessing a cache_key for each: they are at
	most 7 days of cached answers, and a wrong guess would serve one prompt
	path another's answer, which is worse than a cold cache. */
	if _, err := d.Exec(`DROP TABLE IF EXISTS llm_cache`); err != nil {
		return fmt.Errorf("drop old llm_cache: %w", err)
	}
	_, err = d.Exec(`
		CREATE TABLE llm_cache (
			message_id_header TEXT NOT NULL,
			cache_key         TEXT NOT NULL DEFAULT '',
			verdict           JSON NOT NULL,
			cached_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (message_id_header, cache_key)
		)
	`)
	if err != nil {
		return fmt.Errorf("rebuild llm_cache: %w", err)
	}
	return nil
}

// migratePlacements is a safety net, not the primary path: fresh databases
// and any database that has already run migrate() once get the placements
// table from the base CREATE TABLE block above, which runs before
// applyOneShotMigrations. This probes sqlite_master and only creates the
// table if it is somehow still missing, then records v5_placements in
// schema_meta either way so the migration ledger reflects when this
// feature shipped.
func (d *DB) migratePlacements() error {
	var name string
	err := d.QueryRow(`SELECT name FROM sqlite_master WHERE type = 'table' AND name = 'placements'`).Scan(&name)
	if err == nil {
		return nil // already exists
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("probe placements table: %w", err)
	}

	_, err = d.Exec(`
		CREATE TABLE placements (
			message_id_header TEXT NOT NULL,
			folder            TEXT NOT NULL,
			placed_at         TIMESTAMP NOT NULL,
			PRIMARY KEY (message_id_header, folder)
		)
	`)
	if err != nil {
		return fmt.Errorf("create placements table: %w", err)
	}
	return nil
}
