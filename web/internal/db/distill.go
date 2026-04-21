package db

import (
	"fmt"
	"time"
)

// SenderClassification holds the aggregate of manual verdicts for a single
// sender email, used to decide whether to auto-generate a static rule.
type SenderClassification struct {
	SenderEmail       string
	DestinationFolder string
	Count             int
}

// GetManualSenderPatterns returns sender emails that have been manually
// classified to the same folder at least minCount times, with no
// contradictions (all manual verdicts for that sender go to the same folder).
func (d *DB) GetManualSenderPatterns(minCount int) ([]SenderClassification, error) {
	rows, err := d.Query(`
		SELECT sender_email, destination_folder, cnt
		FROM (
			SELECT
				CASE
					WHEN sender LIKE '%<%'
					THEN substr(sender, instr(sender, '<') + 1, instr(sender, '>') - instr(sender, '<') - 1)
					ELSE sender
				END AS sender_email,
				destination_folder,
				COUNT(*) AS cnt
			FROM verdicts
			WHERE rule_id IS NULL AND status = 'manual'
			GROUP BY sender_email, destination_folder
		) grouped
		WHERE cnt >= ?
		AND sender_email NOT IN (
			-- Exclude senders that appear in multiple folders (ambiguous)
			SELECT sender_email FROM (
				SELECT
					CASE
						WHEN sender LIKE '%<%'
						THEN substr(sender, instr(sender, '<') + 1, instr(sender, '>') - instr(sender, '<') - 1)
						ELSE sender
					END AS sender_email,
					destination_folder
				FROM verdicts
				WHERE rule_id IS NULL AND status = 'manual'
				GROUP BY sender_email, destination_folder
			) multi
			GROUP BY sender_email
			HAVING COUNT(DISTINCT destination_folder) > 1
		)
		ORDER BY cnt DESC
	`, minCount)
	if err != nil {
		return nil, fmt.Errorf("db: get manual sender patterns: %w", err)
	}
	defer rows.Close()

	var results []SenderClassification
	for rows.Next() {
		var sc SenderClassification
		if err := rows.Scan(&sc.SenderEmail, &sc.DestinationFolder, &sc.Count); err != nil {
			return nil, fmt.Errorf("db: scan sender classification: %w", err)
		}
		results = append(results, sc)
	}
	return results, rows.Err()
}

// GetDeclinedAutoRuleIDs returns the IDs of auto-generated rules that the user
// has deleted. These are tracked so the system doesn't re-propose them.
func (d *DB) GetDeclinedAutoRuleIDs() (map[string]bool, error) {
	rows, err := d.Query(`
		SELECT id FROM declined_auto_rules
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get declined auto rules: %w", err)
	}
	defer rows.Close()

	result := make(map[string]bool)
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("db: scan declined auto rule: %w", err)
		}
		result[id] = true
	}
	return result, rows.Err()
}

// DeclineAutoRule records that the user deleted an auto-generated rule so it
// won't be re-proposed.
func (d *DB) DeclineAutoRule(id string) error {
	_, err := d.Exec(`
		INSERT OR IGNORE INTO declined_auto_rules (id, declined_at)
		VALUES (?, ?)
	`, id, formatDBTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("db: decline auto rule: %w", err)
	}
	return nil
}
