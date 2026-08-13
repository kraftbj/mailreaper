package db

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// RecordPlacement upserts a durable record that MailReaper itself moved the
// message identified by messageIDHeader into folder. Unlike a verdict row,
// a placement is never touched by ClearAllVerdicts: it is what
// DetectManualClassifications consults after a rescan wipes the verdict
// table, so MailReaper's own past decisions cannot be read back as "the
// user filed this" just because the verdict that recorded them is gone.
func (d *DB) RecordPlacement(messageIDHeader, folder string) error {
	_, err := d.Exec(`
		INSERT INTO placements (message_id_header, folder, placed_at)
		VALUES (?, ?, ?)
		ON CONFLICT(message_id_header, folder) DO UPDATE SET
			placed_at = excluded.placed_at
	`, messageIDHeader, folder, formatDBTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("db: record placement: %w", err)
	}
	return nil
}

// HasPlacement reports whether MailReaper has a durable record of having
// moved the message identified by messageIDHeader into folder itself.
func (d *DB) HasPlacement(messageIDHeader, folder string) (bool, error) {
	var exists int
	err := d.QueryRow(`
		SELECT 1 FROM placements WHERE message_id_header = ? AND folder = ?
	`, messageIDHeader, folder).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("db: check placement: %w", err)
	}
	return true, nil
}
