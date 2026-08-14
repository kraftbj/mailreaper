package scanner

import (
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
)

// TestRescanDoesNotLaunderOwnMoves is the real bug: after a full rescan
// clears verdicts, messages MailReaper itself moved are sitting in triage
// folders with no verdict row. Inferring "the user filed these" from that
// absence turns MailReaper's own decisions into training data, which
// DistillRules then promotes to permanent rules.
func TestRescanDoesNotLaunderOwnMoves(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	cat := db.Category{
		ID:         "promotion",
		Name:       "Promotions",
		FolderName: "Folders/AI-Triage/Promotions",
	}
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("save category: %v", err)
	}

	// Record that MailReaper placed <auto@test> in the promotions folder,
	// then clear all verdicts the way handleRescan does.
	if err := d.RecordPlacement("<auto@test>", "Folders/AI-Triage/Promotions"); err != nil {
		t.Fatalf("record placement: %v", err)
	}
	if _, err := d.ClearAllVerdicts(); err != nil {
		t.Fatalf("clear all verdicts: %v", err)
	}

	s := New(d, cfg)
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {
				{MessageID: "<auto@test>", UID: 1, Subject: "auto-moved message"},
			},
		},
	}
	if err := s.DetectManualClassifications(client, "acct"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	examples, err := d.GetTrainingExamples("promotion")
	if err != nil {
		t.Fatalf("GetTrainingExamples: %v", err)
	}
	for _, ex := range examples {
		if ex.Subject == "auto-moved message" {
			t.Error("MailReaper's own move was recorded as a user classification")
		}
	}
}

// TestDetectManualClassificationsSkipsExpiredFolder is Finding 2 from the
// whole-branch review: v3_seed_expired_category (db.go) seeds a category
// with ID "expired" for the Expired folder, and DetectManualClassifications
// iterates every category folder. A message sitting in Expired with no
// verdict and no placement is this pipeline's own terminal state, not a
// user classification -- it must not be recorded as a manual filing or
// train DistillRules toward a rule that expires that sender's mail
// wholesale.
func TestDetectManualClassificationsSkipsExpiredFolder(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// db.Open's v3_seed_expired_category migration already seeded an
	// "expired" category pointing at "Expired"; use that folder name
	// directly rather than re-declaring the category.
	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories: %v", err)
	}
	var expiredFolder string
	for _, c := range cats {
		if c.ID == "expired" {
			expiredFolder = c.FolderName
		}
	}
	if expiredFolder == "" {
		t.Fatal("expected seeded \"expired\" category to have a folder name")
	}

	s := New(d, cfg)
	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			expiredFolder: {
				{MessageID: "<sitting-in-expired@example.com>", UID: 1, Subject: "past its deadline"},
			},
		},
	}
	if err := s.DetectManualClassifications(client, "acct"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	v, err := d.GetVerdictByMessageID("<sitting-in-expired@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v != nil {
		t.Errorf("expected no verdict recorded for a message already in Expired, got %+v", v)
	}

	examples, err := d.GetTrainingExamples("expired")
	if err != nil {
		t.Fatalf("GetTrainingExamples: %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("expected no training example from the Expired folder, got %d", len(examples))
	}
}

// TestDetectFeedbackDeletesPlacement verifies that when the user reverses a
// move MailReaper made -- the message reappears in the inbox and its
// verdict flips to "corrected" -- the placement record for that
// (message, folder) pair is deleted. The user has revoked MailReaper's
// claim on that destination for this message.
func TestDetectFeedbackDeletesPlacement(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	now := time.Now().UTC()
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<reversed@example.com>",
		Subject:           "Reversed message",
		Sender:            "sender@example.com",
		SentAt:            now.Add(-24 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		EvaluatedAt:       now,
		ActedAt:           &now,
	}); err != nil {
		t.Fatalf("save executed verdict: %v", err)
	}
	if err := d.RecordPlacement("<reversed@example.com>", "Folders/AI-Triage/Promotions"); err != nil {
		t.Fatalf("record placement: %v", err)
	}

	s := New(d, cfg)
	client := &mockMailClient{inboxMsgIDs: []string{"<reversed@example.com>"}}

	if err := s.DetectFeedback(client, "acct", "INBOX"); err != nil {
		t.Fatalf("DetectFeedback: %v", err)
	}

	v, err := d.GetVerdictByMessageID("<reversed@example.com>")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v == nil || v.Status != "corrected" {
		t.Fatalf("expected corrected verdict, got %+v", v)
	}

	has, err := d.HasPlacement("<reversed@example.com>", "Folders/AI-Triage/Promotions")
	if err != nil {
		t.Fatalf("HasPlacement: %v", err)
	}
	if has {
		t.Error("expected placement to be deleted after correction")
	}
}

// TestCorrectionAllowsLaterGenuineRefiling is the full Finding 2 scenario:
// MailReaper moves a message into a folder (placement recorded), the user
// corrects that move, a later full rescan clears every verdict including
// the corrected one (ClearAllVerdicts), and the user genuinely files the
// same message into the same folder again. Without deleting the placement
// at correction time, that stale placement would survive the rescan and
// cause DetectManualClassifications to silently swallow the real manual
// filing -- no training example, no signal to DistillRules.
func TestCorrectionAllowsLaterGenuineRefiling(t *testing.T) {
	d := openTestDB(t)
	cfg := testConfig()

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	cat := db.Category{
		ID:         "promotion",
		Name:       "Promotions",
		FolderName: "Folders/AI-Triage/Promotions",
	}
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("save category: %v", err)
	}

	now := time.Now().UTC()
	if err := d.SaveVerdict(db.Verdict{
		AccountID:         "acct",
		MessageIDHeader:   "<refiled@example.com>",
		Subject:           "genuinely re-filed",
		Sender:            "sender@example.com",
		SentAt:            now.Add(-24 * time.Hour),
		Status:            "executed",
		DestinationFolder: cat.FolderName,
		EvaluatedAt:       now,
		ActedAt:           &now,
	}); err != nil {
		t.Fatalf("save executed verdict: %v", err)
	}
	if err := d.RecordPlacement("<refiled@example.com>", cat.FolderName); err != nil {
		t.Fatalf("record placement: %v", err)
	}

	s := New(d, cfg)

	// The user reverses the move.
	feedbackClient := &mockMailClient{inboxMsgIDs: []string{"<refiled@example.com>"}}
	if err := s.DetectFeedback(feedbackClient, "acct", "INBOX"); err != nil {
		t.Fatalf("DetectFeedback: %v", err)
	}

	// A later full rescan clears every verdict, including the corrected one.
	if _, err := d.ClearAllVerdicts(); err != nil {
		t.Fatalf("clear all verdicts: %v", err)
	}

	// The user genuinely files the message into the same folder again.
	classifyClient := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			cat.FolderName: {
				{MessageID: "<refiled@example.com>", UID: 1, Subject: "genuinely re-filed"},
			},
		},
	}
	if err := s.DetectManualClassifications(classifyClient, "acct"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	examples, err := d.GetTrainingExamples(cat.ID)
	if err != nil {
		t.Fatalf("GetTrainingExamples: %v", err)
	}
	found := false
	for _, ex := range examples {
		if ex.Subject == "genuinely re-filed" {
			found = true
		}
	}
	if !found {
		t.Error("genuine manual re-filing after a correction was silently swallowed by a stale placement")
	}
}
