package db_test

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"

	_ "modernc.org/sqlite"
)

// withoutSeededExpired filters out the "expired" category that db.Open
// seeds into every database (see the v3_seed_expired_category one-shot
// migration), so tests can assert on user-created categories in isolation.
func withoutSeededExpired(cats []db.Category) []db.Category {
	out := make([]db.Category, 0, len(cats))
	for _, c := range cats {
		if c.ID == "expired" {
			continue
		}
		out = append(out, c)
	}
	return out
}

func TestSaveAndGetCategories(t *testing.T) {
	d := openTestDB(t)

	cats := []db.Category{
		{ID: "paper-trail", Name: "Paper Trail", FolderName: "Paper-Trail", Icon: "🗂️", Color: "#3b82f6"},
		{ID: "receipts", Name: "Receipts", FolderName: "Receipts", Icon: "🧾", Color: "#10b981"},
	}

	for _, c := range cats {
		if err := d.SaveCategory(c); err != nil {
			t.Fatalf("SaveCategory(%q) error = %v", c.ID, err)
		}
	}

	all, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
	got := withoutSeededExpired(all)
	if len(got) != 2 {
		t.Fatalf("expected 2 categories, got %d", len(got))
	}

	// Results should be ordered by name ASC.
	if got[0].Name != "Paper Trail" {
		t.Errorf("expected first category to be %q, got %q", "Paper Trail", got[0].Name)
	}
}

func TestGetCategoriesEmpty(t *testing.T) {
	d := openTestDB(t)
	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
	// A fresh database still carries the seeded "expired" category; only
	// user-created categories are expected to be absent.
	if got := withoutSeededExpired(cats); len(got) != 0 {
		t.Errorf("expected 0 user-created categories, got %d", len(got))
	}
}

func TestSaveCategoryUpsert(t *testing.T) {
	d := openTestDB(t)

	c := db.Category{ID: "cat-1", Name: "Original", FolderName: "Folder", Icon: "", Color: ""}
	if err := d.SaveCategory(c); err != nil {
		t.Fatalf("first SaveCategory() error = %v", err)
	}

	c.Name = "Updated"
	c.FolderName = "NewFolder"
	if err := d.SaveCategory(c); err != nil {
		t.Fatalf("second SaveCategory() error = %v", err)
	}

	all, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
	cats := withoutSeededExpired(all)
	if len(cats) != 1 {
		t.Fatalf("expected 1 category after upsert, got %d", len(cats))
	}
	if cats[0].Name != "Updated" {
		t.Errorf("Name after upsert: got %q, want %q", cats[0].Name, "Updated")
	}
	if cats[0].FolderName != "NewFolder" {
		t.Errorf("FolderName after upsert: got %q, want %q", cats[0].FolderName, "NewFolder")
	}
}

func TestExpiredCategorySeeded(t *testing.T) {
	d := openTestDB(t)

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		if c.ID == "expired" {
			if c.FolderName == "" {
				t.Error("expired category has an empty FolderName")
			}
			return
		}
	}
	t.Fatal("no category with ID \"expired\" was seeded; canonical Expired routing and the deferred sweep are both inert without it")
}

func TestDeleteCategory(t *testing.T) {
	d := openTestDB(t)

	c := db.Category{ID: "cat-del", Name: "To Delete", FolderName: "Folder"}
	if err := d.SaveCategory(c); err != nil {
		t.Fatalf("SaveCategory() error = %v", err)
	}
	if err := d.DeleteCategory("cat-del"); err != nil {
		t.Fatalf("DeleteCategory() error = %v", err)
	}

	all, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
	if got := withoutSeededExpired(all); len(got) != 0 {
		t.Errorf("expected 0 categories after delete, got %d", len(got))
	}
}

// TestCategoryCheckDeadlinesRoundTrip proves the flag survives a save/load
// cycle in both directions. A one-directional test would pass against a
// SaveCategory whose ON CONFLICT clause forgets the column, which is the
// realistic way to get this wrong.
func TestCategoryCheckDeadlinesRoundTrip(t *testing.T) {
	d := openTestDB(t)

	if err := d.SaveCategory(db.Category{
		ID: "promotions", Name: "Promotions", FolderName: "Promotions",
		CheckDeadlines: true,
	}); err != nil {
		t.Fatalf("SaveCategory: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "paper-trail", Name: "Paper-Trail", FolderName: "Paper-Trail",
	}); err != nil {
		t.Fatalf("SaveCategory: %v", err)
	}

	got := map[string]bool{}
	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		got[c.ID] = c.CheckDeadlines
	}

	if !got["promotions"] {
		t.Error("promotions: CheckDeadlines = false, want true")
	}
	if got["paper-trail"] {
		t.Error("paper-trail: CheckDeadlines = true, want false")
	}
}

// TestCategoryCheckDeadlinesUpsertClearsFlag verifies the flag can be turned
// back off. SaveCategory is an upsert, so a missing column in the ON CONFLICT
// SET list leaves a stale 1 in place forever and the UI checkbox appears
// broken only when unchecking.
func TestCategoryCheckDeadlinesUpsertClearsFlag(t *testing.T) {
	d := openTestDB(t)

	cat := db.Category{ID: "hobbies", Name: "Hobbies", FolderName: "Hobbies", CheckDeadlines: true}
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("SaveCategory (on): %v", err)
	}
	cat.CheckDeadlines = false
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("SaveCategory (off): %v", err)
	}

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	for _, c := range cats {
		if c.ID == "hobbies" && c.CheckDeadlines {
			t.Fatal("CheckDeadlines stayed true after saving it false")
		}
	}
}

// TestCheckDeadlinesMigrationSeedsPerishableCategories exercises the upgrade
// path a real user is on: a database whose categories table predates the
// column, holding the plural category ids this deployment actually uses.
//
// The table is created by hand with the old shape, then reopened so
// applyOneShotMigrations runs against it. Asserting on a fresh database
// instead would prove nothing -- the base CREATE TABLE already has the
// column there, and the seeding UPDATE would match no rows.
func TestCheckDeadlinesMigrationSeedsPerishableCategories(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.db")

	legacy, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatalf("open legacy db: %v", err)
	}
	if _, err := legacy.Exec(`
		CREATE TABLE categories (
			id           TEXT PRIMARY KEY,
			name         TEXT,
			folder_name  TEXT,
			icon         TEXT,
			color        TEXT,
			created_at   TIMESTAMP
		)`); err != nil {
		t.Fatalf("create legacy categories table: %v", err)
	}
	for _, row := range [][2]string{
		{"promotions", "Folders/AI-Triage/Promotions"},
		{"notifications", "Folders/AI-Triage/Notifications"},
		{"hobbies", "Folders/AI-Triage/Hobbies"},
		{"newsletters", "Folders/AI-Triage/Newsletters"},
		{"paper-trail", "Folders/AI-Triage/Paper-Trail"},
	} {
		if _, err := legacy.Exec(
			`INSERT INTO categories (id, name, folder_name, created_at) VALUES (?, ?, ?, datetime('now'))`,
			row[0], row[0], row[1],
		); err != nil {
			t.Fatalf("seed legacy category %q: %v", row[0], err)
		}
	}
	if err := legacy.Close(); err != nil {
		t.Fatalf("close legacy db: %v", err)
	}

	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("db.Open on legacy database: %v", err)
	}
	t.Cleanup(func() { d.Close() })

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	got := map[string]bool{}
	for _, c := range cats {
		got[c.ID] = c.CheckDeadlines
	}

	for _, id := range []string{"promotions", "notifications", "hobbies"} {
		if !got[id] {
			t.Errorf("%s: CheckDeadlines = false after migration, want true", id)
		}
	}
	for _, id := range []string{"newsletters", "paper-trail", "expired"} {
		if got[id] {
			t.Errorf("%s: CheckDeadlines = true after migration, want false", id)
		}
	}
}
