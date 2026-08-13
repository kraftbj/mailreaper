package db_test

import (
	"path/filepath"
	"testing"

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
