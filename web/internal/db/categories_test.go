package db_test

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
)

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

	got, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
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
	if len(cats) != 0 {
		t.Errorf("expected 0 categories, got %d", len(cats))
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

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
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

func TestDeleteCategory(t *testing.T) {
	d := openTestDB(t)

	c := db.Category{ID: "cat-del", Name: "To Delete", FolderName: "Folder"}
	if err := d.SaveCategory(c); err != nil {
		t.Fatalf("SaveCategory() error = %v", err)
	}
	if err := d.DeleteCategory("cat-del"); err != nil {
		t.Fatalf("DeleteCategory() error = %v", err)
	}

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error = %v", err)
	}
	if len(cats) != 0 {
		t.Errorf("expected 0 categories after delete, got %d", len(cats))
	}
}
