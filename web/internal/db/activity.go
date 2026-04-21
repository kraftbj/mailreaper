package db

import (
	"fmt"
	"time"
)

// ActivityEntry records an action taken by MailReaper.
type ActivityEntry struct {
	ID              int       `json:"id"`
	Type            string    `json:"type"`
	AccountID       string    `json:"accountId"`
	MessageIDHeader string    `json:"messageIdHeader"`
	Subject         string    `json:"subject"`
	Sender          string    `json:"sender"`
	RuleName        string    `json:"ruleName"`
	Destination     string    `json:"destination"`
	Reason          string    `json:"reason"`
	Confidence      float64   `json:"confidence"`
	CreatedAt       time.Time `json:"createdAt"`
}

// LogActivity inserts a new entry into the activity log.
func (d *DB) LogActivity(entry ActivityEntry) error {
	_, err := d.Exec(`
		INSERT INTO activity_log
			(type, account_id, message_id_header, subject, sender,
			 rule_name, destination, reason, confidence, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		entry.Type, entry.AccountID, entry.MessageIDHeader, entry.Subject,
		entry.Sender, entry.RuleName, entry.Destination, entry.Reason,
		entry.Confidence, formatDBTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("db: log activity: %w", err)
	}
	return nil
}

// GetActivityLog returns the most recent entries, up to limit. Pass 0 for no limit.
func (d *DB) GetActivityLog(limit int) ([]ActivityEntry, error) {
	q := `
		SELECT id, type, account_id, message_id_header, subject, sender,
		       rule_name, destination, reason, confidence, created_at
		FROM activity_log
		ORDER BY created_at DESC
	`
	var args []any
	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	sqlRows, err := d.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: get activity log: %w", err)
	}
	defer sqlRows.Close()

	var entries []ActivityEntry
	for sqlRows.Next() {
		var e ActivityEntry
		var createdAtStr string
		if err := sqlRows.Scan(
			&e.ID, &e.Type, &e.AccountID, &e.MessageIDHeader, &e.Subject,
			&e.Sender, &e.RuleName, &e.Destination, &e.Reason,
			&e.Confidence, &createdAtStr,
		); err != nil {
			return nil, fmt.Errorf("db: scan activity entry: %w", err)
		}
		if e.CreatedAt, err = parseDBTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("db: parse activity created_at: %w", err)
		}
		entries = append(entries, e)
	}
	return entries, sqlRows.Err()
}

// ClearActivityLog deletes all entries from the activity log.
func (d *DB) ClearActivityLog() error {
	_, err := d.Exec(`DELETE FROM activity_log`)
	if err != nil {
		return fmt.Errorf("db: clear activity log: %w", err)
	}
	return nil
}
