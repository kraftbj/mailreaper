package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
)

func TestBackfillDestination_NilVerdict(t *testing.T) {
	got := backfillDestination(nil, "Expired", "INBOX")
	if got != "INBOX" {
		t.Errorf("nil verdict should rescue to INBOX, got %q", got)
	}
}

func TestBackfillDestination_ExpiredVerdict_PrefersCanonicalExpired(t *testing.T) {
	v := &rules.RuleVerdict{
		Expired: true,
		Rule:    db.Rule{DestinationFolder: "Folders/AI-Triage/Promotions"},
	}
	got := backfillDestination(v, "Folders/AI-Triage/Expired", "INBOX")
	if got != "Folders/AI-Triage/Expired" {
		t.Errorf("expired verdict must route to canonical Expired, got %q", got)
	}
}

func TestBackfillDestination_ExpiredVerdict_FallsBackToRuleDestination(t *testing.T) {
	v := &rules.RuleVerdict{
		Expired: true,
		Rule:    db.Rule{DestinationFolder: "Custom/Expired"},
	}
	got := backfillDestination(v, "", "INBOX") // no canonical Expired configured
	if got != "Custom/Expired" {
		t.Errorf("fallback should use rule's destination, got %q", got)
	}
}

func TestBackfillDestination_ClassifiedVerdict(t *testing.T) {
	v := &rules.RuleVerdict{
		Classified: true,
		Rule:       db.Rule{DestinationFolder: "Folders/AI-Triage/Notifications"},
	}
	got := backfillDestination(v, "Folders/AI-Triage/Expired", "INBOX")
	if got != "Folders/AI-Triage/Notifications" {
		t.Errorf("classified verdict should route to rule's folder, got %q", got)
	}
}

func TestBackfillDestination_ClassifiedNoDestRescues(t *testing.T) {
	v := &rules.RuleVerdict{
		Classified: true,
		Rule:       db.Rule{DestinationFolder: ""},
	}
	got := backfillDestination(v, "Folders/AI-Triage/Expired", "INBOX")
	if got != "INBOX" {
		t.Errorf("classified verdict with no destination should rescue to INBOX, got %q", got)
	}
}

// TestBackfillClearsCachedVerdict proves --backfill actually re-asks the
// LLM. The cache is keyed on Message-ID with no model component, so
// without an explicit removal, backfill replays the stale verdict it was
// run to refresh.
func TestBackfillClearsCachedVerdict(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := database.SaveCategory(db.Category{ID: "expired", Name: "Expired", FolderName: "Expired"}); err != nil {
		t.Fatalf("save category: %v", err)
	}

	if err := database.SetCachedVerdict("<msg-1@test>", db.CachedVerdict{
		Reason:     "stale verdict from the previous model",
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SetCachedVerdict: %v", err)
	}

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<msg-1@test>",
		Subject:   "Some sale",
		Sender:    "promo@example.com",
		Date:      time.Now(),
		Folder:    "Expired",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Expired": {msg},
		},
	}
	s := New(database, cfg)

	if _, err := s.BackfillFolders(context.Background(), client, "acct1", "INBOX"); err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	cached, err := database.GetCachedVerdict("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetCachedVerdict: %v", err)
	}
	if cached != nil && cached.Reason == "stale verdict from the previous model" {
		t.Error("backfill reused the stale cached verdict instead of re-evaluating")
	}
}
