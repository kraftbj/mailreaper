package scanner

import (
	"fmt"
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

// TestSweepMessageMissingFromFolder verifies that a deferred verdict whose
// message is no longer in the folder it recorded is terminated rather than
// retried forever.
//
// This replaces a test that asserted the opposite: it documented "the same
// verdict is picked up again on the next sweep" as current behavior. That
// behavior is the reason the live database accumulated 191 past-due verdicts
// stuck for as long as four months -- every one of them re-listed its folder
// on every scan cycle and could never be satisfied.
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
		Subject:           "user moved this themselves",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	// The folder exists but does not contain the message.
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {{MessageID: "<someone-else@test>", UID: 9}},
		},
	}

	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}
	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s), want 0", len(client.movedMsgs))
	}

	v, err := d.GetVerdictByMessageID("<gone@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected the verdict to survive as a row, got nil")
	}
	if v.Status != orphanedStatus {
		t.Errorf("Status = %q, want %q", v.Status, orphanedStatus)
	}
	if v.ExpiresAt == nil {
		t.Error("ExpiresAt was cleared; it should be preserved for forensics")
	}

	// The whole point: it must not come back.
	again, err := d.GetDeferredExpiries("acct")
	if err != nil {
		t.Fatalf("GetDeferredExpiries: %v", err)
	}
	for _, e := range again {
		if e.MessageIDHeader == "<gone@test>" {
			t.Fatal("orphaned verdict is still returned by GetDeferredExpiries; the backlog cannot drain")
		}
	}
}

// TestSweepFetchesEachFolderOnce is the regression test for the defect that
// stalled the live sweep: GetMessagesInFolder was called inside the
// per-verdict loop, so N past-due verdicts meant N full SELECT + SEARCH ALL +
// FETCH ENVELOPE round trips against folders holding thousands of messages.
//
// Six verdicts across two folders must cost exactly two fetches.
func TestSweepFetchesEachFolderOnce(t *testing.T) {
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
	folders := []string{"Folders/AI-Triage/Promotions", "Folders/AI-Triage/Hobbies"}
	folderMessages := map[string][]imappkg.FetchedMessage{}

	uid := uint32(1)
	for _, folder := range folders {
		for i := 0; i < 3; i++ {
			id := fmt.Sprintf("<msg-%d@test>", uid)
			if err := d.SaveVerdict(db.Verdict{
				AccountID:         "acct",
				MessageIDHeader:   id,
				Subject:           id,
				SentAt:            time.Now().Add(-96 * time.Hour),
				Status:            "executed",
				DestinationFolder: folder,
				ExpiresAt:         &past,
				Confidence:        0.9,
				EvaluatedAt:       time.Now().UTC(),
			}); err != nil {
				t.Fatalf("SaveVerdict: %v", err)
			}
			folderMessages[folder] = append(folderMessages[folder], imappkg.FetchedMessage{
				MessageID: id, Folder: folder, UID: uid,
			})
			uid++
		}
	}

	client := &mockMailClient{folderMessages: folderMessages}
	if err := s.SweepDeferredExpiries(client, "acct"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.movedMsgs) != 6 {
		t.Errorf("moved %d message(s), want 6", len(client.movedMsgs))
	}
	for _, folder := range folders {
		if got := client.folderFetches[folder]; got != 1 {
			t.Errorf("GetMessagesInFolder(%q) called %d time(s), want exactly 1", folder, got)
		}
	}
}

// TestSweepIgnoresOtherAccounts verifies the sweep is scoped to the account
// whose IMAP client is connected. GetDeferredExpiries had no account filter,
// so in a multi-account setup every account's expiries were swept against
// whichever client happened to be connected. That was merely wasteful before;
// combined with orphaning it is destructive, because the other account's
// messages are guaranteed not to be found.
func TestSweepIgnoresOtherAccounts(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()
	s := New(d, cfg)

	if s.canonicalExpiredFolder() == "" {
		t.Fatal("no expired category resolved; this test would pass vacuously")
	}
	if err := d.UpsertAccount("acct-a", "Account A"); err != nil {
		t.Fatalf("upsert account a: %v", err)
	}
	if err := d.UpsertAccount("acct-b", "Account B"); err != nil {
		t.Fatalf("upsert account b: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct-b",
		MessageIDHeader:   "<other-account@test>",
		Subject:           "belongs to the other account",
		SentAt:            time.Now().Add(-96 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpiresAt:         &past,
		Confidence:        0.9,
		EvaluatedAt:       time.Now().UTC(),
	}); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	client := &mockMailClient{folderMessages: map[string][]imappkg.FetchedMessage{}}
	if err := s.SweepDeferredExpiries(client, "acct-a"); err != nil {
		t.Fatalf("SweepDeferredExpiries: %v", err)
	}

	if len(client.folderFetches) != 0 {
		t.Errorf("fetched %d folder(s) for an account with no expiries, want 0", len(client.folderFetches))
	}
	v, err := d.GetVerdictByMessageID("<other-account@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v.Status != "executed" {
		t.Errorf("Status = %q, want executed; the other account's verdict must not be touched", v.Status)
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
