package db_test

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
)

func makeEntry(subject string) db.ActivityEntry {
	return db.ActivityEntry{
		Type:            "move",
		AccountID:       "account-1",
		MessageIDHeader: "<" + subject + "@example.com>",
		Subject:         subject,
		Sender:          "sender@example.com",
		RuleName:        "Test rule",
		Destination:     "Expired",
		Reason:          "expired by TTL",
		Confidence:      1.0,
	}
}

func TestLogAndGetActivity(t *testing.T) {
	d := openTestDB(t)

	entries := []db.ActivityEntry{
		makeEntry("first"),
		makeEntry("second"),
		makeEntry("third"),
	}

	for _, e := range entries {
		if err := d.LogActivity(e); err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	log, err := d.GetActivityLog(0)
	if err != nil {
		t.Fatalf("GetActivityLog() error = %v", err)
	}
	if len(log) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(log))
	}

	// Most recent first — last logged should be at index 0.
	if log[0].Subject != "third" {
		t.Errorf("expected most recent entry first, got %q", log[0].Subject)
	}
}

func TestGetActivityLogWithLimit(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 5; i++ {
		if err := d.LogActivity(makeEntry("msg")); err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	log, err := d.GetActivityLog(3)
	if err != nil {
		t.Fatalf("GetActivityLog(3) error = %v", err)
	}
	if len(log) != 3 {
		t.Errorf("expected 3 entries with limit=3, got %d", len(log))
	}
}

func TestClearActivityLog(t *testing.T) {
	d := openTestDB(t)

	for i := 0; i < 3; i++ {
		if err := d.LogActivity(makeEntry("msg")); err != nil {
			t.Fatalf("LogActivity() error = %v", err)
		}
	}

	if err := d.ClearActivityLog(); err != nil {
		t.Fatalf("ClearActivityLog() error = %v", err)
	}

	log, err := d.GetActivityLog(0)
	if err != nil {
		t.Fatalf("GetActivityLog() after clear error = %v", err)
	}
	if len(log) != 0 {
		t.Errorf("expected 0 entries after clear, got %d", len(log))
	}
}
