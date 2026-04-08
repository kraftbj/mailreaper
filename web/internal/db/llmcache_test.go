package db_test

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
)

func TestSetAndGetCachedVerdict(t *testing.T) {
	d := openTestDB(t)

	cv := db.CachedVerdict{
		IsTimeSensitive: true,
		Expired:         true,
		Reason:          "deadline mentioned",
		Confidence:      0.95,
	}

	msgID := "<cache-test@example.com>"
	if err := d.SetCachedVerdict(msgID, cv); err != nil {
		t.Fatalf("SetCachedVerdict() error = %v", err)
	}

	got, err := d.GetCachedVerdict(msgID)
	if err != nil {
		t.Fatalf("GetCachedVerdict() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetCachedVerdict() returned nil")
	}
	if got.Expired != cv.Expired {
		t.Errorf("Expired: got %v, want %v", got.Expired, cv.Expired)
	}
	if got.Reason != cv.Reason {
		t.Errorf("Reason: got %q, want %q", got.Reason, cv.Reason)
	}
	if got.Confidence != cv.Confidence {
		t.Errorf("Confidence: got %v, want %v", got.Confidence, cv.Confidence)
	}
}

func TestGetCachedVerdictMiss(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetCachedVerdict("<nonexistent@example.com>")
	if err != nil {
		t.Fatalf("GetCachedVerdict() error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil for miss, got %+v", got)
	}
}

func TestSetCachedVerdictUpsert(t *testing.T) {
	d := openTestDB(t)

	msgID := "<upsert-cache@example.com>"

	cv1 := db.CachedVerdict{Expired: true, Reason: "original"}
	if err := d.SetCachedVerdict(msgID, cv1); err != nil {
		t.Fatalf("first SetCachedVerdict() error = %v", err)
	}

	cv2 := db.CachedVerdict{Expired: false, Reason: "updated"}
	if err := d.SetCachedVerdict(msgID, cv2); err != nil {
		t.Fatalf("second SetCachedVerdict() error = %v", err)
	}

	got, err := d.GetCachedVerdict(msgID)
	if err != nil {
		t.Fatalf("GetCachedVerdict() error = %v", err)
	}
	if got.Reason != "updated" {
		t.Errorf("Reason after upsert: got %q, want %q", got.Reason, "updated")
	}
}

func TestRemoveCachedVerdict(t *testing.T) {
	d := openTestDB(t)

	msgID := "<remove-cache@example.com>"
	cv := db.CachedVerdict{Expired: true}

	if err := d.SetCachedVerdict(msgID, cv); err != nil {
		t.Fatalf("SetCachedVerdict() error = %v", err)
	}
	if err := d.RemoveCachedVerdict(msgID); err != nil {
		t.Fatalf("RemoveCachedVerdict() error = %v", err)
	}

	got, err := d.GetCachedVerdict(msgID)
	if err != nil {
		t.Fatalf("GetCachedVerdict() after remove error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil after remove, got %+v", got)
	}
}

func TestClearLLMCache(t *testing.T) {
	d := openTestDB(t)

	for _, id := range []string{"<a@x.com>", "<b@x.com>", "<c@x.com>"} {
		if err := d.SetCachedVerdict(id, db.CachedVerdict{Expired: true}); err != nil {
			t.Fatalf("SetCachedVerdict() error = %v", err)
		}
	}

	if err := d.ClearLLMCache(); err != nil {
		t.Fatalf("ClearLLMCache() error = %v", err)
	}

	// All three should now miss.
	for _, id := range []string{"<a@x.com>", "<b@x.com>", "<c@x.com>"} {
		got, err := d.GetCachedVerdict(id)
		if err != nil {
			t.Fatalf("GetCachedVerdict(%q) after clear error = %v", id, err)
		}
		if got != nil {
			t.Errorf("expected nil after clear for %q", id)
		}
	}
}
