package scanner

import (
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
)

// TestSweepMovesPastDeadlineToExpired verifies that a verdict whose
// expires_at has passed gets its message moved from its current triage
// folder to the canonical Expired folder, and the verdict/activity/placement
// records are updated to reflect it.
//
// Task 8 seeds the canonical "expired" category via a one-shot migration
// that runs on every db.Open() (including openTestDB's :memory: database),
// so the guard below is load-bearing: without a resolved expired folder,
// SweepDeferredExpiries returns early at the "expiredFolder == \"\"" check
// in sweep.go, and every assertion after it would pass vacuously with zero
// work done.
func TestSweepMovesPastDeadlineToExpired(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	s := New(d, cfg)
	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<deferred@test>",
		Subject:           "sale ends yesterday",
		Sender:            "shop@example.test",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{
				MessageID: "<deferred@test>",
				Folder:    "Folders/AI-Triage/Promotions",
				UID:       1,
			}},
		},
	}

	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 1 {
		t.Fatalf("recorded %d move(s), want 1", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != expiredFolder {
		t.Errorf("moved to %q, want the canonical Expired folder %q", client.movedMsgs[0], expiredFolder)
	}

	v, err := d.GetVerdictByMessageID("<deferred@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Status != "executed" {
		t.Errorf("Status = %q, want executed", v.Status)
	}
	if v.DestinationFolder != expiredFolder {
		t.Errorf("DestinationFolder = %q, want %q", v.DestinationFolder, expiredFolder)
	}
	if v.ActedAt == nil {
		t.Error("expected ActedAt to be set after the sweep moved the message")
	}

	has, err := d.HasPlacement("<deferred@test>", expiredFolder)
	if err != nil {
		t.Fatalf("HasPlacement: %v", err)
	}
	if !has {
		t.Error("expected a placement record for the swept move")
	}

	actLog, err := d.GetActivityLog(10)
	if err != nil {
		t.Fatalf("GetActivityLog: %v", err)
	}
	if len(actLog) != 1 {
		t.Fatalf("expected 1 activity entry, got %d", len(actLog))
	}
	if actLog[0].Type != "expired" {
		t.Errorf("activity Type = %q, want expired", actLog[0].Type)
	}
}

// TestSweepMessageMissingFromFolder verifies that when the deferred verdict's
// destination folder no longer contains the message (e.g. the user moved it
// themselves), the sweep does not panic and does not record a move. Because
// GetDeferredExpiries re-derives its result set from the verdict row alone
// (no cross-check against what actually got moved), the current behavior is
// that the same verdict is picked up again on the next sweep -- documented
// here rather than asserted as ideal.
func TestSweepMessageMissingFromFolder(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	s := New(d, cfg)
	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<gone@test>",
		Subject:           "sale ends yesterday",
		Sender:            "shop@example.test",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	/* The mock's folder listing for Promotions is empty -- the message is
	not there (client.folderMessages has no entry, so GetMessagesInFolder
	returns nil). */
	client := &mockMailClient{}

	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s), want 0 -- the message isn't in the folder", len(client.movedMsgs))
	}

	v, err := d.GetVerdictByMessageID("<gone@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	// Known current behavior: nothing about the verdict changes, so
	// GetDeferredExpiries will return this same row again on the next sweep.
	if v.Status != "executed" {
		t.Errorf("Status = %q, want unchanged executed", v.Status)
	}
	if v.DestinationFolder != "Folders/AI-Triage/Promotions" {
		t.Errorf("DestinationFolder = %q, want unchanged %q", v.DestinationFolder, "Folders/AI-Triage/Promotions")
	}

	again, err := d.GetDeferredExpiries()
	if err != nil {
		t.Fatalf("GetDeferredExpiries: %v", err)
	}
	found := false
	for _, dv := range again {
		if dv.MessageIDHeader == "<gone@test>" {
			found = true
		}
	}
	if !found {
		t.Error("expected the untouched verdict to still surface as a deferred expiry (documented retry-forever behavior)")
	}
}

// TestSweepNoCategoryReturnsEarly verifies that with no "expired" category
// configured, the sweep returns without error and without attempting any
// move. Task 8's migration seeds an "expired" category on every db.Open(),
// so this test must explicitly delete it to reproduce the no-category state.
func TestSweepNoCategoryReturnsEarly(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	if err := d.DeleteCategory("expired"); err != nil {
		t.Fatalf("DeleteCategory: %v", err)
	}

	s := New(d, cfg)
	if got := s.canonicalExpiredFolder(); got != "" {
		t.Fatalf("canonicalExpiredFolder() = %q, want empty after deleting the category", got)
	}

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<nocategory@test>",
		Subject:           "sale ends yesterday",
		Sender:            "shop@example.test",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{
				MessageID: "<nocategory@test>",
				Folder:    "Folders/AI-Triage/Promotions",
				UID:       1,
			}},
		},
	}

	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s), want 0 with no expired category configured", len(client.movedMsgs))
	}
}
