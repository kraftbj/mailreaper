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
		v.AccountID, v.MessageIDHeader, v.Subject, v.Sender, v.SentAt, v.RuleID,
		v.Status, v.DestinationFolder, v.ExpiresAt, v.Reason, v.Confidence,
		v.EvaluatedAt, v.ActedAt,
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
	var actedAt *time.Time
	switch status {
	case "approved", "rejected", "corrected":
		now := time.Now().UTC()
		actedAt = &now
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
	var expiresAt sql.NullTime
	var actedAt sql.NullTime

	err := row.Scan(
		&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject, &v.Sender, &v.SentAt,
		&ruleID, &v.Status, &v.DestinationFolder, &expiresAt,
		&v.Reason, &v.Confidence, &v.EvaluatedAt, &actedAt,
	)
	if err != nil {
		return nil, err
	}

	if ruleID.Valid {
		v.RuleID = &ruleID.String
	}
	if expiresAt.Valid {
		v.ExpiresAt = &expiresAt.Time
	}
	if actedAt.Valid {
		v.ActedAt = &actedAt.Time
	}
	return &v, nil
}

func scanVerdicts(rows *sql.Rows) ([]Verdict, error) {
	var verdicts []Verdict
	for rows.Next() {
		var v Verdict
		var ruleID sql.NullString
		var expiresAt sql.NullTime
		var actedAt sql.NullTime

		err := rows.Scan(
			&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject, &v.Sender, &v.SentAt,
			&ruleID, &v.Status, &v.DestinationFolder, &expiresAt,
			&v.Reason, &v.Confidence, &v.EvaluatedAt, &actedAt,
		)
		if err != nil {
			return nil, fmt.Errorf("db: scan verdict: %w", err)
		}
		if ruleID.Valid {
			v.RuleID = &ruleID.String
		}
		if expiresAt.Valid {
			v.ExpiresAt = &expiresAt.Time
		}
		if actedAt.Valid {
			v.ActedAt = &actedAt.Time
		}
		verdicts = append(verdicts, v)
	}
	return verdicts, rows.Err()
}
