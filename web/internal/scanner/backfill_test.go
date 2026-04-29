package scanner

import (
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
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
