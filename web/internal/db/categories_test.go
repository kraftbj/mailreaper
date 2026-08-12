package db_test

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
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
