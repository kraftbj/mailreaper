package db_test

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
)

func makeTrainingExample(category, subject string) db.TrainingExample {
	return db.TrainingExample{
		Category:    category,
		Subject:     subject,
		Sender:      "sender@example.com",
		BodySnippet: "body text",
		Label:       "positive",
		Source:      "dashboard",
	}
}

func TestAddAndGetTrainingExamples(t *testing.T) {
	d := openTestDB(t)

	ex := makeTrainingExample("receipt", "Your receipt from Acme")
	if err := d.AddTrainingExample(ex); err != nil {
		t.Fatalf("AddTrainingExample() error = %v", err)
	}

	examples, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples(\"\") error = %v", err)
	}
	if len(examples) != 1 {
		t.Fatalf("expected 1 example, got %d", len(examples))
	}
	if examples[0].Subject != ex.Subject {
		t.Errorf("Subject: got %q, want %q", examples[0].Subject, ex.Subject)
	}
}

func TestGetTrainingExamplesCategoryFilter(t *testing.T) {
	d := openTestDB(t)

	for _, ex := range []db.TrainingExample{
		makeTrainingExample("receipt", "Receipt 1"),
		makeTrainingExample("receipt", "Receipt 2"),
		makeTrainingExample("promo", "Promo 1"),
	} {
		if err := d.AddTrainingExample(ex); err != nil {
			t.Fatalf("AddTrainingExample() error = %v", err)
		}
	}

	receipts, err := d.GetTrainingExamples("receipt")
	if err != nil {
		t.Fatalf("GetTrainingExamples(\"receipt\") error = %v", err)
	}
	if len(receipts) != 2 {
		t.Errorf("expected 2 receipt examples, got %d", len(receipts))
	}
	for _, ex := range receipts {
		if ex.Category != "receipt" {
			t.Errorf("got non-receipt category: %q", ex.Category)
		}
	}

	all, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples(\"\") error = %v", err)
	}
	if len(all) != 3 {
		t.Errorf("expected 3 total examples, got %d", len(all))
	}
}

func TestDeleteTrainingExample(t *testing.T) {
	d := openTestDB(t)

	if err := d.AddTrainingExample(makeTrainingExample("receipt", "Receipt")); err != nil {
		t.Fatalf("AddTrainingExample() error = %v", err)
	}

	examples, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() error = %v", err)
	}
	if len(examples) != 1 {
		t.Fatalf("expected 1 example before delete, got %d", len(examples))
	}

	if err := d.DeleteTrainingExample(examples[0].ID); err != nil {
		t.Fatalf("DeleteTrainingExample() error = %v", err)
	}

	remaining, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() after delete error = %v", err)
	}
	if len(remaining) != 0 {
		t.Errorf("expected 0 examples after delete, got %d", len(remaining))
	}
}

func TestClearTrainingExamples(t *testing.T) {
	d := openTestDB(t)

	for _, ex := range []db.TrainingExample{
		makeTrainingExample("receipt", "R1"),
		makeTrainingExample("receipt", "R2"),
		makeTrainingExample("promo", "P1"),
	} {
		if err := d.AddTrainingExample(ex); err != nil {
			t.Fatalf("AddTrainingExample() error = %v", err)
		}
	}

	// Clear only receipts.
	if err := d.ClearTrainingExamples("receipt"); err != nil {
		t.Fatalf("ClearTrainingExamples(\"receipt\") error = %v", err)
	}

	remaining, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() error = %v", err)
	}
	if len(remaining) != 1 || remaining[0].Category != "promo" {
		t.Errorf("expected 1 promo example after clearing receipts, got %v", remaining)
	}

	// Clear all.
	if err := d.ClearTrainingExamples(""); err != nil {
		t.Fatalf("ClearTrainingExamples(\"\") error = %v", err)
	}
	all, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() error = %v", err)
	}
	if len(all) != 0 {
		t.Errorf("expected 0 examples after clear all, got %d", len(all))
	}
}

func TestAddTrainingExampleCap(t *testing.T) {
	d := openTestDB(t)

	// Insert 100 examples to reach the cap.
	for i := 0; i < 100; i++ {
		ex := makeTrainingExample("receipt", "old-subject")
		if err := d.AddTrainingExample(ex); err != nil {
			t.Fatalf("AddTrainingExample() error = %v at i=%d", err, i)
		}
	}

	// Add one more — should evict the oldest.
	if err := d.AddTrainingExample(makeTrainingExample("receipt", "new-subject")); err != nil {
		t.Fatalf("AddTrainingExample() over cap error = %v", err)
	}

	examples, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() error = %v", err)
	}
	if len(examples) != 100 {
		t.Errorf("expected 100 examples after cap eviction, got %d", len(examples))
	}

	// The newest should be present.
	newest := examples[len(examples)-1]
	if newest.Subject != "new-subject" {
		t.Errorf("expected newest subject %q, got %q", "new-subject", newest.Subject)
	}
}
