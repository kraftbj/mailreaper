package db_test

import (
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

func makeVerdict(msgID string) db.Verdict {
	return db.Verdict{
		AccountID:       "account-1",
		MessageIDHeader: msgID,
		Subject:         "Test subject",
		Sender:          "sender@example.com",
		SentAt:          time.Now().Add(-1 * time.Hour),
		Status:          "pending",
		Reason:          "test reason",
		Confidence:      0.9,
		EvaluatedAt:     time.Now(),
	}
}

func TestSaveAndGetVerdict(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v := makeVerdict("<test-msg-1@example.com>")
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error = %v", err)
	}

	got, err := d.GetVerdictByMessageID("<test-msg-1@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetVerdictByMessageID() returned nil")
	}
	if got.MessageIDHeader != v.MessageIDHeader {
		t.Errorf("MessageIDHeader: got %q, want %q", got.MessageIDHeader, v.MessageIDHeader)
	}
	if got.Subject != v.Subject {
		t.Errorf("Subject: got %q, want %q", got.Subject, v.Subject)
	}
	if got.Status != "pending" {
		t.Errorf("Status: got %q, want pending", got.Status)
	}
}

func TestGetVerdictByMessageIDMiss(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetVerdictByMessageID("<nonexistent@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestGetPendingVerdicts(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v1 := makeVerdict("<pending-1@example.com>")
	v2 := makeVerdict("<pending-2@example.com>")
	v3 := makeVerdict("<acted-1@example.com>")
	v3.Status = "approved"

	for _, v := range []db.Verdict{v1, v2, v3} {
		if err := d.SaveVerdict(v); err != nil {
			t.Fatalf("SaveVerdict() error = %v", err)
		}
	}

	pending, err := d.GetPendingVerdicts()
	if err != nil {
		t.Fatalf("GetPendingVerdicts() error = %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending verdicts, got %d", len(pending))
	}
	for _, v := range pending {
		if v.Status != "pending" {
			t.Errorf("non-pending verdict in results: %+v", v)
		}
	}
}

func TestUpdateVerdictStatus(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v := makeVerdict("<update-status@example.com>")
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error = %v", err)
	}

	if err := d.UpdateVerdictStatus("<update-status@example.com>", "approved"); err != nil {
		t.Fatalf("UpdateVerdictStatus() error = %v", err)
	}

	got, err := d.GetVerdictByMessageID("<update-status@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got.Status != "approved" {
		t.Errorf("Status: got %q, want approved", got.Status)
	}
	if got.ActedAt == nil {
		t.Error("ActedAt: expected non-nil for approved status")
	}
}

func TestUpdateVerdictStatusPendingNoActedAt(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v := makeVerdict("<pending-no-acted@example.com>")
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error = %v", err)
	}

	// Setting to a non-terminal status should not set acted_at.
	if err := d.UpdateVerdictStatus("<pending-no-acted@example.com>", "pending"); err != nil {
		t.Fatalf("UpdateVerdictStatus() error = %v", err)
	}

	got, err := d.GetVerdictByMessageID("<pending-no-acted@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got.ActedAt != nil {
		t.Error("ActedAt: expected nil for pending status")
	}
}

func TestVerdictUpsert(t *testing.T) {
	d := openTestDB(t)
	if err := d.UpsertAccount("account-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v := makeVerdict("<upsert@example.com>")
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("first SaveVerdict() error = %v", err)
	}

	v.Subject = "Updated subject"
	v.Confidence = 0.5
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("second SaveVerdict() error = %v", err)
	}

	got, err := d.GetVerdictByMessageID("<upsert@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if got.Subject != "Updated subject" {
		t.Errorf("Subject: got %q, want %q", got.Subject, "Updated subject")
	}

	// Should still be one row.
	pending, err := d.GetPendingVerdicts()
	if err != nil {
		t.Fatalf("GetPendingVerdicts() error = %v", err)
	}
	if len(pending) != 1 {
		t.Errorf("expected 1 verdict after upsert, got %d", len(pending))
	}
}

func TestGetExecutedMessageIDs(t *testing.T) {
	d := openTestDB(t)

	// Insert account first to satisfy the FK.
	if err := d.UpsertAccount("acct-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	v1 := makeVerdict("<exec-1@example.com>")
	v1.AccountID = "acct-1"
	v2 := makeVerdict("<exec-2@example.com>")
	v2.AccountID = "acct-1"
	v3 := makeVerdict("<pending-exec@example.com>")
	v3.AccountID = "acct-1"

	for _, v := range []db.Verdict{v1, v2, v3} {
		if err := d.SaveVerdict(v); err != nil {
			t.Fatalf("SaveVerdict() error = %v", err)
		}
	}

	if err := d.UpdateVerdictStatus("<exec-1@example.com>", "approved"); err != nil {
		t.Fatalf("UpdateVerdictStatus() error = %v", err)
	}
	if err := d.UpdateVerdictStatus("<exec-2@example.com>", "rejected"); err != nil {
		t.Fatalf("UpdateVerdictStatus() error = %v", err)
	}

	ids, err := d.GetExecutedMessageIDs("acct-1")
	if err != nil {
		t.Fatalf("GetExecutedMessageIDs() error = %v", err)
	}
	if len(ids) != 2 {
		t.Errorf("expected 2 executed IDs, got %d: %v", len(ids), ids)
	}
}
