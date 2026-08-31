package scanner

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
)

/*
deadlineTestScanner wires a Scanner to a local httptest.Server standing in
for an Ollama model, using the same fake-response shape as the backfill LLM
tests (see fakeOllamaResponse in backfill_test.go). It returns the scanner
and a pointer to the call counter, so a test can assert not only what the
extraction decided but whether it spent a call at all -- the cost argument
is the entire reason the check_deadlines gate exists.
*/
func deadlineTestScanner(t *testing.T, d *db.DB, expiresAt, reason string, confidence float64) (*Scanner, *int32) {
	t.Helper()

	var calls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		content, err := json.Marshal(struct {
			ExpiresAt  string  `json:"expiresAt"`
			Reason     string  `json:"reason"`
			Confidence float64 `json:"confidence"`
		}{ExpiresAt: expiresAt, Reason: reason, Confidence: confidence})
		if err != nil {
			t.Errorf("marshal fake LLM content: %v", err)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(fakeOllamaResponse{
			Message: fakeOllamaMessage{Role: "assistant", Content: string(content)},
		}); err != nil {
			t.Errorf("encode fake ollama response: %v", err)
		}
	}))
	t.Cleanup(ts.Close)

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "ollama",
		Ollama:   config.OllamaConfig{Endpoint: ts.URL, Model: "test-model"},
	}
	return New(d, cfg), &calls
}

// seedDeadlineCategories creates one perishable and one non-perishable
// category, mirroring the live shape (plural ids, nested folder names).
func seedDeadlineCategories(t *testing.T, d *db.DB) {
	t.Helper()
	if err := d.SaveCategory(db.Category{
		ID: "promotions", Name: "Promotions",
		FolderName: "Folders/AI-Triage/Promotions", CheckDeadlines: true,
	}); err != nil {
		t.Fatalf("save promotions category: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "newsletters", Name: "Newsletters",
		FolderName: "Folders/AI-Triage/Newsletters", CheckDeadlines: false,
	}); err != nil {
		t.Fatalf("save newsletters category: %v", err)
	}
}

func deadlineTestMessage() *imappkg.FetchedMessage {
	return &imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<promo@test>",
		Subject:   "Sale ends Friday",
		Sender:    "shop@example.test",
		Date:      time.Now().Add(-96 * time.Hour),
		Folder:    "INBOX",
	}
}

// TestDeadlineCheckedFoldersUsesLowercaseKeys pins the key convention every
// caller depends on. A map keyed by the raw folder name would silently miss
// on any case difference between the category row and the rule's
// destination, and a missed lookup reads as "not perishable" -- a false
// negative that is invisible.
func TestDeadlineCheckedFoldersUsesLowercaseKeys(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)
	s := New(d, testConfig())

	checked := s.deadlineCheckedFolders()
	if !checked["folders/ai-triage/promotions"] {
		t.Error("perishable folder missing from the set under its lowercased name")
	}
	if checked["folders/ai-triage/newsletters"] {
		t.Error("non-perishable folder is in the set")
	}
}

// TestApplyDeadlineCheckPastDeadlineRoutesToExpired is the user-facing
// behavior this whole change exists for: a promo email filed into a
// perishable folder, whose body says the sale is over, must end up in
// Expired instead of the folder the routing rule chose.
func TestApplyDeadlineCheckPastDeadlineRoutesToExpired(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour) // after the send date, before now
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0, Reason: "pattern rule"}
	dest, expiresAt, reason := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Expired" {
		t.Errorf("dest = %q, want the canonical Expired folder", dest)
	}
	if expiresAt == nil {
		t.Fatal("expiresAt = nil, want the extracted deadline")
	}
	if reason != "sale ended" {
		t.Errorf("reason = %q, want the model's reason", reason)
	}
	if atomic.LoadInt32(calls) != 1 {
		t.Errorf("made %d LLM call(s), want 1", atomic.LoadInt32(calls))
	}
}

// TestApplyDeadlineCheckFutureDeadlineKeepsFolder verifies the deferred case:
// the message stays where the routing rule put it, but carries a deadline so
// SweepDeferredExpiries can act on the day.
func TestApplyDeadlineCheckFutureDeadlineKeepsFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	future := time.Now().Add(72 * time.Hour)
	s, _ := deadlineTestScanner(t, d, future.Format(time.RFC3339), "sale ends Friday", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0, Reason: "pattern rule"}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q, want the routing rule's folder", dest)
	}
	if expiresAt == nil {
		t.Fatal("expiresAt = nil; the deadline must be persisted for the sweep")
	}
	if !expiresAt.After(time.Now()) {
		t.Error("expiresAt is not in the future")
	}
}

// TestApplyDeadlineCheckSkipsNonPerishableFolder asserts on the call count,
// not just the return value. Returning the folder unchanged while still
// having spent a call would pass a weaker test and defeat the only reason
// the flag exists: Newsletters alone is over half the classify-routed volume.
func TestApplyDeadlineCheckSkipsNonPerishableFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Newsletters",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Newsletters" {
		t.Errorf("dest = %q, want the folder unchanged", dest)
	}
	if expiresAt != nil {
		t.Error("expiresAt was set for a non-perishable folder")
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for a non-perishable folder, want 0", got)
	}
}

// TestApplyDeadlineCheckSkipsVerdictThatAlreadyHasOne guards against paying
// twice: llm-classify verdicts already carry a deadline from their combined
// call, and re-asking would double the spend on the largest population.
func TestApplyDeadlineCheckSkipsVerdictThatAlreadyHasOne(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	other := time.Now().Add(48 * time.Hour)
	s, calls := deadlineTestScanner(t, d, other.Format(time.RFC3339), "from the model", 0.95)

	existing := time.Now().Add(24 * time.Hour)
	verdict := &rules.RuleVerdict{
		Classified: true, Confidence: 1.0,
		ExpiresAt: &existing, Reason: "from the classify call",
	}
	dest, expiresAt, reason := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for a verdict that already had a deadline, want 0", got)
	}
	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q, want unchanged", dest)
	}
	if expiresAt == nil || !expiresAt.Equal(existing) {
		t.Error("the existing deadline was not preserved")
	}
	if reason != "from the classify call" {
		t.Errorf("reason = %q, want the original verdict's reason", reason)
	}
}

// TestApplyDeadlineCheckDropsLowConfidence confirms the shared guard in
// extractValidExpiresAt still applies on this path. A 0.4-confidence
// extraction is treated as a hallucination and must not move mail: the
// standing bias of this codebase is false negatives over false positives.
func TestApplyDeadlineCheckDropsLowConfidence(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, _ := deadlineTestScanner(t, d, past.Format(time.RFC3339), "maybe expired?", 0.4)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, expiresAt, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/AI-Triage/Promotions",
		s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/AI-Triage/Promotions" {
		t.Errorf("dest = %q; a low-confidence extraction must not move mail", dest)
	}
	if expiresAt != nil {
		t.Error("expiresAt was set from a below-threshold extraction")
	}
}

// TestApplyDeadlineCheckSkipsUnknownFolder covers a destination that maps to
// no category at all -- for instance a rule pointing at a folder the user
// later removed from Settings. Unknown means not perishable.
func TestApplyDeadlineCheckSkipsUnknownFolder(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	past := time.Now().Add(-24 * time.Hour)
	s, calls := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended", 0.95)

	verdict := &rules.RuleVerdict{Classified: true, Confidence: 1.0}
	dest, _, _ := s.applyDeadlineCheck(
		context.Background(), &mockMailClient{}, deadlineTestMessage(),
		verdict, "Folders/Some/Unmapped", s.deadlineCheckedFolders(), "Folders/AI-Triage/Expired",
	)

	if dest != "Folders/Some/Unmapped" {
		t.Errorf("dest = %q, want unchanged", dest)
	}
	if got := atomic.LoadInt32(calls); got != 0 {
		t.Errorf("made %d LLM call(s) for an unmapped folder, want 0", got)
	}
}

// TestScanCycleClassifyRuleGetsDeadlineCheck is the end-to-end proof, run
// through ScanAccount rather than applyDeadlineCheck directly: a priority-27
// classify rule of the kind DistillRules generates routes a message to
// Promotions, and the past deadline in its body must redirect it to Expired.
//
// This is the exact shape of the reported bug -- a dmsguild or Cinemark
// sender rule filing a dated sale email into a triage folder it never leaves.
func TestScanCycleClassifyRuleGetsDeadlineCheck(t *testing.T) {
	d := openTestDB(t)
	seedDeadlineCategories(t, d)

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := d.SaveCategory(db.Category{
		ID: "expired", Name: "Expired", FolderName: "Folders/AI-Triage/Expired",
	}); err != nil {
		t.Fatalf("save expired category: %v", err)
	}
	if err := d.SaveRule(db.Rule{
		ID:                "auto-shop-example-test",
		Name:              "shop@example.test → Promotions",
		Enabled:           true,
		Priority:          27,
		MatchConfig:       db.MatchConfig{SenderPatterns: []string{"*@example.test"}},
		ExpirationConfig:  db.ExpirationConfig{Type: "classify"},
		DestinationFolder: "Folders/AI-Triage/Promotions",
		Action:            "move",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	past := time.Now().Add(-24 * time.Hour)
	s, _ := deadlineTestScanner(t, d, past.Format(time.RFC3339), "sale ended Friday", 0.95)

	msg := deadlineTestMessage()
	client := &mockMailClient{messages: []imappkg.FetchedMessage{*msg}}

	if err := s.ScanAccount(context.Background(), client, "acct", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	if len(client.movedMsgs) != 1 {
		t.Fatalf("recorded %d move(s), want 1", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != "Folders/AI-Triage/Expired" {
		t.Errorf("moved to %q, want Folders/AI-Triage/Expired", client.movedMsgs[0])
	}

	v, err := d.GetVerdictByMessageID(msg.MessageID)
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a verdict, got nil")
	}
	if v.DestinationFolder != "Folders/AI-Triage/Expired" {
		t.Errorf("DestinationFolder = %q, want Folders/AI-Triage/Expired", v.DestinationFolder)
	}
	if v.ExpiresAt == nil {
		t.Error("ExpiresAt was not persisted")
	}

	actLog, err := d.GetActivityLog(10)
	if err != nil {
		t.Fatalf("GetActivityLog: %v", err)
	}
	if len(actLog) != 1 {
		t.Fatalf("expected 1 activity entry, got %d", len(actLog))
	}
	if actLog[0].Type != "expired" {
		t.Errorf("activity Type = %q, want expired; a redirect to Expired is an expiry, not a triage", actLog[0].Type)
	}
}
