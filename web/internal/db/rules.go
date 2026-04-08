package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// MatchConfig holds pattern-based filters for a rule.
type MatchConfig struct {
	SenderPatterns  []string          `json:"senderPatterns,omitempty"`
	SubjectPatterns []string          `json:"subjectPatterns,omitempty"`
	Folders         []string          `json:"folders,omitempty"`
	HeaderMatch     map[string]string `json:"headerMatch,omitempty"`
}

// ExpirationConfig describes how expiration is determined.
type ExpirationConfig struct {
	Type     string `json:"type"`
	Hours    int    `json:"hours,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Category string `json:"category,omitempty"`
}

// Rule represents a single MailReaper rule.
type Rule struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Enabled           bool             `json:"enabled"`
	Priority          int              `json:"priority"`
	Builtin           bool             `json:"builtin"`
	MatchConfig       MatchConfig      `json:"matchConfig"`
	ExpirationConfig  ExpirationConfig `json:"expirationConfig"`
	Action            string           `json:"action"`
	DestinationFolder string           `json:"destinationFolder"`
	NextRuleID        *string          `json:"nextRuleId,omitempty"`
	GracePeriodDays   int              `json:"gracePeriodDays"`
}

// GetRules returns all rules sorted by priority ascending.
func (d *DB) GetRules() ([]Rule, error) {
	rows, err := d.Query(`
		SELECT id, name, enabled, priority, builtin,
		       match_config, expiration_config, action,
		       destination_folder, next_rule_id, grace_period_days
		FROM rules
		ORDER BY priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get rules: %w", err)
	}
	defer rows.Close()

	return scanRules(rows)
}

// GetRule returns a single rule by ID.
func (d *DB) GetRule(id string) (*Rule, error) {
	row := d.QueryRow(`
		SELECT id, name, enabled, priority, builtin,
		       match_config, expiration_config, action,
		       destination_folder, next_rule_id, grace_period_days
		FROM rules
		WHERE id = ?
	`, id)

	r, err := scanRule(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: get rule: %w", err)
	}
	return r, nil
}

// GetEnabledRules returns all enabled rules sorted by priority ascending.
func (d *DB) GetEnabledRules() ([]Rule, error) {
	rows, err := d.Query(`
		SELECT id, name, enabled, priority, builtin,
		       match_config, expiration_config, action,
		       destination_folder, next_rule_id, grace_period_days
		FROM rules
		WHERE enabled = TRUE
		ORDER BY priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get enabled rules: %w", err)
	}
	defer rows.Close()

	return scanRules(rows)
}

// SaveRule upserts a rule. created_at is set on insert only; updated_at always refreshed.
func (d *DB) SaveRule(r Rule) error {
	matchJSON, err := json.Marshal(r.MatchConfig)
	if err != nil {
		return fmt.Errorf("db: marshal match config: %w", err)
	}
	expirationJSON, err := json.Marshal(r.ExpirationConfig)
	if err != nil {
		return fmt.Errorf("db: marshal expiration config: %w", err)
	}

	now := time.Now().UTC()
	_, err = d.Exec(`
		INSERT INTO rules
			(id, name, enabled, priority, builtin, match_config, expiration_config,
			 action, destination_folder, next_rule_id, grace_period_days, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name               = excluded.name,
			enabled            = excluded.enabled,
			priority           = excluded.priority,
			builtin            = excluded.builtin,
			match_config       = excluded.match_config,
			expiration_config  = excluded.expiration_config,
			action             = excluded.action,
			destination_folder = excluded.destination_folder,
			next_rule_id       = excluded.next_rule_id,
			grace_period_days  = excluded.grace_period_days,
			updated_at         = excluded.updated_at
	`,
		r.ID, r.Name, r.Enabled, r.Priority, r.Builtin,
		string(matchJSON), string(expirationJSON),
		r.Action, r.DestinationFolder, r.NextRuleID, r.GracePeriodDays,
		now, now,
	)
	if err != nil {
		return fmt.Errorf("db: save rule: %w", err)
	}
	return nil
}

// DeleteRule removes a rule by ID.
func (d *DB) DeleteRule(id string) error {
	_, err := d.Exec(`DELETE FROM rules WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete rule: %w", err)
	}
	return nil
}

// SeedDefaults inserts each default rule only if a rule with that ID does not
// already exist. It is safe to call multiple times (idempotent).
func (d *DB) SeedDefaults(defaults []Rule) error {
	for _, r := range defaults {
		matchJSON, err := json.Marshal(r.MatchConfig)
		if err != nil {
			return fmt.Errorf("db: seed defaults marshal match: %w", err)
		}
		expirationJSON, err := json.Marshal(r.ExpirationConfig)
		if err != nil {
			return fmt.Errorf("db: seed defaults marshal expiration: %w", err)
		}

		now := time.Now().UTC()
		_, err = d.Exec(`
			INSERT OR IGNORE INTO rules
				(id, name, enabled, priority, builtin, match_config, expiration_config,
				 action, destination_folder, next_rule_id, grace_period_days, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		`,
			r.ID, r.Name, r.Enabled, r.Priority, r.Builtin,
			string(matchJSON), string(expirationJSON),
			r.Action, r.DestinationFolder, r.NextRuleID, r.GracePeriodDays,
			now, now,
		)
		if err != nil {
			return fmt.Errorf("db: seed defaults insert %q: %w", r.ID, err)
		}
	}
	return nil
}

// scanRules reads all rows into a []Rule slice.
func scanRules(rows *sql.Rows) ([]Rule, error) {
	var rules []Rule
	for rows.Next() {
		r, err := scanRuleFromRow(rows)
		if err != nil {
			return nil, err
		}
		rules = append(rules, *r)
	}
	return rules, rows.Err()
}

// rowScanner is satisfied by both *sql.Row and *sql.Rows so we can share scan logic.
type rowScanner interface {
	Scan(dest ...any) error
}

// scanRule scans a *sql.Row (single-row query).
func scanRule(row *sql.Row) (*Rule, error) {
	return scanRuleFromRow(row)
}

func scanRuleFromRow(row rowScanner) (*Rule, error) {
	var r Rule
	var matchJSON, expirationJSON string
	var nextRuleID sql.NullString

	err := row.Scan(
		&r.ID, &r.Name, &r.Enabled, &r.Priority, &r.Builtin,
		&matchJSON, &expirationJSON,
		&r.Action, &r.DestinationFolder, &nextRuleID, &r.GracePeriodDays,
	)
	if err != nil {
		return nil, err
	}

	if nextRuleID.Valid {
		r.NextRuleID = &nextRuleID.String
	}

	if err := json.Unmarshal([]byte(matchJSON), &r.MatchConfig); err != nil {
		return nil, fmt.Errorf("db: unmarshal match config: %w", err)
	}
	if err := json.Unmarshal([]byte(expirationJSON), &r.ExpirationConfig); err != nil {
		return nil, fmt.Errorf("db: unmarshal expiration config: %w", err)
	}

	return &r, nil
}
