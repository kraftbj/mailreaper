package scanner

import (
	"testing"

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
