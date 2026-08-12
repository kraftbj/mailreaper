package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// Verdict stores the expiration decision for a single message.
type Verdict struct {
	ID                int        `json:"id"`
	AccountID         string     `json:"accountId"`
	MessageIDHeader   string     `json:"messageIdHeader"`
	Subject           string     `json:"subject"`
	Sender            string     `json:"sender"`
	SentAt            time.Time  `json:"sentAt"`
	RuleID            *string    `json:"ruleId,omitempty"`
	Status            string     `json:"status"`
	DestinationFolder string     `json:"destinationFolder"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	Reason            string     `json:"reason"`
	Confidence        float64    `json:"confidence"`
	EvaluatedAt       time.Time  `json:"evaluatedAt"`
	ActedAt           *time.Time `json:"actedAt,omitempty"`
}

// SaveVerdict upserts a verdict keyed on message_id_header.
func (d *DB) SaveVerdict(v Verdict) error {
	_, err := d.Exec(`
		INSERT INTO verdicts
			(account_id, message_id_header, subject, sender, sent_at, rule_id,
			 status, destination_folder, expires_at, reason, confidence, evaluated_at, acted_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id_header) DO UPDATE SET
			account_id         = excluded.account_id,
			subject            = excluded.subject,
			sender             = excluded.sender,
			sent_at            = excluded.sent_at,
			rule_id            = excluded.rule_id,
			status             = excluded.status,
			destination_folder = excluded.destination_folder,
			expires_at         = excluded.expires_at,
			reason             = excluded.reason,
			confidence         = excluded.confidence,
			evaluated_at       = excluded.evaluated_at,
			acted_at           = excluded.acted_at
	`,
		v.AccountID, v.MessageIDHeader, v.Subject, v.Sender,
		formatDBTime(v.SentAt), v.RuleID,
		v.Status, v.DestinationFolder, formatDBNullTime(v.ExpiresAt),
		v.Reason, v.Confidence, formatDBTime(v.EvaluatedAt),
		formatDBNullTime(v.ActedAt),
	)
	if err != nil {
		return fmt.Errorf("db: save verdict: %w", err)
	}
	return nil
}

// GetVerdictByMessageID returns the verdict for the given Message-ID header,
// or nil if none exists.
func (d *DB) GetVerdictByMessageID(header string) (*Verdict, error) {
	row := d.QueryRow(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason,
		       confidence, evaluated_at, acted_at
		FROM verdicts
		WHERE message_id_header = ?
	`, header)

	v, err := scanVerdict(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: get verdict: %w", err)
	}
	return v, nil
}

// GetPendingVerdicts returns all verdicts with status 'pending'.
func (d *DB) GetPendingVerdicts() ([]Verdict, error) {
	rows, err := d.Query(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason,
		       confidence, evaluated_at, acted_at
		FROM verdicts
		WHERE status = 'pending'
		ORDER BY evaluated_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get pending verdicts: %w", err)
	}
	defer rows.Close()

	return scanVerdicts(rows)
}

// UpdateVerdictStatus sets the status of a verdict. For approved, rejected, or
// corrected statuses, acted_at is also set to now.
func (d *DB) UpdateVerdictStatus(messageIDHeader, status string) error {
	var actedAt any
	switch status {
	case "approved", "rejected", "corrected":
		actedAt = formatDBTime(time.Now().UTC())
	}

	_, err := d.Exec(`
		UPDATE verdicts
		SET status = ?, acted_at = ?
		WHERE message_id_header = ?
	`, status, actedAt, messageIDHeader)
	if err != nil {
		return fmt.Errorf("db: update verdict status: %w", err)
	}
	return nil
}

// ClearAllVerdicts removes all verdicts, forcing a full re-evaluation on next scan.
func (d *DB) ClearAllVerdicts() (int64, error) {
	result, err := d.Exec(`DELETE FROM verdicts`)
	if err != nil {
		return 0, fmt.Errorf("db: clear all verdicts: %w", err)
	}
	return result.RowsAffected()
}

// GetDeferredExpiries returns verdicts that have an expires_at in the past but
// haven't been moved to the expired folder yet. These are messages that were
// classified with a future expiry date that has now arrived.
func (d *DB) GetDeferredExpiries() ([]Verdict, error) {
	rows, err := d.Query(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason,
		       confidence, evaluated_at, acted_at
		FROM verdicts
		WHERE expires_at IS NOT NULL
		  AND expires_at != ''
		  AND status IN ('executed', 'manual')
		  AND destination_folder NOT LIKE '%Expired%'
		ORDER BY expires_at ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get deferred expiries: %w", err)
	}
	defer rows.Close()

	all, err := scanVerdicts(rows)
	if err != nil {
		return nil, err
	}

	// Filter to only those whose expires_at is in the past.
	now := time.Now()
	var expired []Verdict
	for _, v := range all {
		if v.ExpiresAt != nil && now.After(*v.ExpiresAt) {
			expired = append(expired, v)
		}
	}
	return expired, nil
}

// GetExecutedMessageIDs returns all message_id_header values for verdicts
// belonging to the given account that have been acted on (acted_at IS NOT NULL).
func (d *DB) GetExecutedMessageIDs(accountID string) ([]string, error) {
	rows, err := d.Query(`
		SELECT message_id_header
		FROM verdicts
		WHERE account_id = ? AND acted_at IS NOT NULL
	`, accountID)
	if err != nil {
		return nil, fmt.Errorf("db: get executed message ids: %w", err)
	}
	defer rows.Close()

	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scan message id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanVerdict(row *sql.Row) (*Verdict, error) {
	var v Verdict
	var ruleID sql.NullString
	var sentAtStr, evaluatedAtStr string
	var expiresAtStr, actedAtStr sql.NullString

	err := row.Scan(
		&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject, &v.Sender, &sentAtStr,
		&ruleID, &v.Status, &v.DestinationFolder, &expiresAtStr,
		&v.Reason, &v.Confidence, &evaluatedAtStr, &actedAtStr,
	)
	if err != nil {
		return nil, err
	}

	if v.SentAt, err = parseDBTime(sentAtStr); err != nil {
		return nil, fmt.Errorf("parse sent_at: %w", err)
	}
	if v.EvaluatedAt, err = parseDBTime(evaluatedAtStr); err != nil {
		return nil, fmt.Errorf("parse evaluated_at: %w", err)
	}
	if ruleID.Valid {
		v.RuleID = &ruleID.String
	}
	if v.ExpiresAt, err = parseDBNullTime(expiresAtStr); err != nil {
		return nil, fmt.Errorf("parse expires_at: %w", err)
	}
	if v.ActedAt, err = parseDBNullTime(actedAtStr); err != nil {
		return nil, fmt.Errorf("parse acted_at: %w", err)
	}
	return &v, nil
}

func scanVerdicts(rows *sql.Rows) ([]Verdict, error) {
	var verdicts []Verdict
	for rows.Next() {
		var v Verdict
		var ruleID sql.NullString
		var sentAtStr, evaluatedAtStr string
		var expiresAtStr, actedAtStr sql.NullString

		err := rows.Scan(
			&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject, &v.Sender, &sentAtStr,
			&ruleID, &v.Status, &v.DestinationFolder, &expiresAtStr,
			&v.Reason, &v.Confidence, &evaluatedAtStr, &actedAtStr,
		)
		if err != nil {
			return nil, fmt.Errorf("db: scan verdict: %w", err)
		}
		if v.SentAt, err = parseDBTime(sentAtStr); err != nil {
			return nil, fmt.Errorf("db: parse sent_at: %w", err)
		}
		if v.EvaluatedAt, err = parseDBTime(evaluatedAtStr); err != nil {
			return nil, fmt.Errorf("db: parse evaluated_at: %w", err)
		}
		if ruleID.Valid {
			v.RuleID = &ruleID.String
		}
		if v.ExpiresAt, err = parseDBNullTime(expiresAtStr); err != nil {
			return nil, fmt.Errorf("db: parse expires_at: %w", err)
		}
		if v.ActedAt, err = parseDBNullTime(actedAtStr); err != nil {
			return nil, fmt.Errorf("db: parse acted_at: %w", err)
		}
		verdicts = append(verdicts, v)
	}
	return verdicts, rows.Err()
}
