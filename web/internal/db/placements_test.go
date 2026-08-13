package db_test

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

func TestRecordAndHasPlacement(t *testing.T) {
	d := openTestDB(t)

	has, err := d.HasPlacement("<no-placement@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement() error = %v", err)
	}
	if has {
		t.Error("expected no placement before RecordPlacement")
	}

	if err := d.RecordPlacement("<placed@example.com>", "Expired"); err != nil {
		t.Fatalf("RecordPlacement() error = %v", err)
	}

	has, err = d.HasPlacement("<placed@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement() error = %v", err)
	}
	if !has {
		t.Error("expected placement after RecordPlacement")
	}

	// A different folder for the same message is a different placement.
	has, err = d.HasPlacement("<placed@example.com>", "Paper-Trail")
	if err != nil {
		t.Fatalf("HasPlacement() error = %v", err)
	}
	if has {
		t.Error("expected no placement for a folder that was never recorded")
	}
}

func TestRecordPlacementUpsert(t *testing.T) {
	d := openTestDB(t)

	// Recording the same (message, folder) pair twice must not error --
	// e.g. retryFailedMove and the auto-execute path can both record the
	// same destination over time.
	if err := d.RecordPlacement("<repeat@example.com>", "Expired"); err != nil {
		t.Fatalf("first RecordPlacement() error = %v", err)
	}
	if err := d.RecordPlacement("<repeat@example.com>", "Expired"); err != nil {
		t.Fatalf("second RecordPlacement() error = %v", err)
	}

	has, err := d.HasPlacement("<repeat@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement() error = %v", err)
	}
	if !has {
		t.Error("expected placement to survive repeated recording")
	}
}

// TestPlacementSurvivesClearAllVerdicts is the property the laundering fix
// depends on: ClearAllVerdicts must not touch the placements table.
func TestPlacementSurvivesClearAllVerdicts(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	if err := d.RecordPlacement("<survives@example.com>", "Expired"); err != nil {
		t.Fatalf("RecordPlacement() error = %v", err)
	}

	v := makeVerdict("<survives@example.com>")
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error = %v", err)
	}

	if _, err := d.ClearAllVerdicts(); err != nil {
		t.Fatalf("ClearAllVerdicts() error = %v", err)
	}

	// The verdict is gone...
	got, err := d.GetVerdictByMessageID("<survives@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got != nil {
		t.Fatalf("expected verdict to be cleared, got %+v", got)
	}

	// ...but the placement record is not.
	has, err := d.HasPlacement("<survives@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement() error = %v", err)
	}
	if !has {
		t.Error("expected placement to survive ClearAllVerdicts")
	}
}

// TestPlacementsMigrationSafeOnFreshDB proves migratePlacements no-ops
// against a table that already exists and already holds data -- the case
// that actually matters, since applyOneShotMigrations runs after the base
// schema's CREATE TABLE block, so a fresh DB already has the placements
// table before the migration function ever runs. Simply opening the same
// path twice does not exercise the probe: the second Open finds the
// "v5_placements" row already in schema_meta and skips the migration
// function entirely. To force the probe to actually run against a
// populated, already-correct table, this test deletes that schema_meta row
// before reopening.
func TestPlacementsMigrationSafeOnFreshDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "fresh.db")

	first, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.RecordPlacement("<fresh@example.com>", "Expired"); err != nil {
		t.Fatalf("seed placement: %v", err)
	}

	if _, err := first.Exec(`DELETE FROM schema_meta WHERE key = ?`, "v5_placements"); err != nil {
		t.Fatalf("clear v5 schema_meta marker: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first handle: %v", err)
	}

	second, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()

	has, err := second.HasPlacement("<fresh@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement() after forced re-migration error = %v", err)
	}
	if !has {
		t.Error("placement lost when migratePlacements re-ran against an already-correct, populated table")
	}
}

// TestPlacementsMigrationBackfillsExecutedOnly proves migratePlacements'
// one-time backfill seeds a placement for every historical "executed"
// verdict, while "corrected" and "move_failed" rows of the same shape
// produce nothing:
//
//   - "corrected" is excluded because UpdateVerdictStatus never clears
//     destination_folder when it flips a verdict's status -- that column
//     still names the folder the user just reversed out of. Backfilling a
//     placement from it would encode "MailReaper owns this destination" for
//     exactly the placement the user overrode.
//   - "move_failed" is excluded because the move outcome is ambiguous (the
//     IMAP MOVE may have silently succeeded before the error came back),
//     matching R3's treatment of move_failed in DetectManualClassifications.
//
// It also proves the COALESCE(acted_at, evaluated_at) fallback: an executed
// row with a NULL acted_at must still produce a placement, not violate
// placements.placed_at's NOT NULL constraint and abort the whole backfill.
func TestPlacementsMigrationBackfillsExecutedOnly(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "backfill.db")

	first, err := db.Open(path)
	if err != nil {
		t.Fatalf("first Open() error = %v", err)
	}
	if err := first.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	now := time.Now().UTC()

	executed := makeVerdict("<backfill-executed@example.com>")
	executed.Status = "executed"
	executed.DestinationFolder = "Expired"
	executed.ActedAt = &now
	if err := first.SaveVerdict(executed); err != nil {
		t.Fatalf("save executed verdict: %v", err)
	}

	// acted_at intentionally left nil to exercise the fallback to
	// evaluated_at.
	executedNoActedAt := makeVerdict("<backfill-executed-no-acted-at@example.com>")
	executedNoActedAt.Status = "executed"
	executedNoActedAt.DestinationFolder = "Paper-Trail"
	if err := first.SaveVerdict(executedNoActedAt); err != nil {
		t.Fatalf("save executed verdict with no acted_at: %v", err)
	}

	corrected := makeVerdict("<backfill-corrected@example.com>")
	corrected.Status = "corrected"
	corrected.DestinationFolder = "Promotions" // left in place by UpdateVerdictStatus
	corrected.ActedAt = &now
	if err := first.SaveVerdict(corrected); err != nil {
		t.Fatalf("save corrected verdict: %v", err)
	}

	moveFailed := makeVerdict("<backfill-move-failed@example.com>")
	moveFailed.Status = "move_failed"
	moveFailed.DestinationFolder = "Notifications"
	if err := first.SaveVerdict(moveFailed); err != nil {
		t.Fatalf("save move_failed verdict: %v", err)
	}

	// Force applyOneShotMigrations to run migratePlacements again, now
	// against a populated verdicts table -- the scenario the backfill
	// exists for. The initial Open ran the backfill against an empty
	// verdicts table, so it inserted nothing; deleting the schema_meta
	// marker and reopening is the only way to make it run again without
	// also reverting the placements table.
	if _, err := first.Exec(`DELETE FROM schema_meta WHERE key = ?`, "v5_placements"); err != nil {
		t.Fatalf("clear v5 schema_meta marker: %v", err)
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close first handle: %v", err)
	}

	second, err := db.Open(path)
	if err != nil {
		t.Fatalf("second Open() error = %v", err)
	}
	defer second.Close()

	has, err := second.HasPlacement("<backfill-executed@example.com>", "Expired")
	if err != nil {
		t.Fatalf("HasPlacement(executed): %v", err)
	}
	if !has {
		t.Error("expected a placement backfilled from the executed verdict")
	}

	has, err = second.HasPlacement("<backfill-executed-no-acted-at@example.com>", "Paper-Trail")
	if err != nil {
		t.Fatalf("HasPlacement(executed, no acted_at): %v", err)
	}
	if !has {
		t.Error("expected a placement backfilled from the executed verdict with a null acted_at")
	}

	has, err = second.HasPlacement("<backfill-corrected@example.com>", "Promotions")
	if err != nil {
		t.Fatalf("HasPlacement(corrected): %v", err)
	}
	if has {
		t.Error("corrected verdict must not seed a placement -- its destination_folder still names the folder the user reversed out of")
	}

	has, err = second.HasPlacement("<backfill-move-failed@example.com>", "Notifications")
	if err != nil {
		t.Fatalf("HasPlacement(move_failed): %v", err)
	}
	if has {
		t.Error("move_failed verdict must not seed a placement -- the move outcome is ambiguous")
	}
}
