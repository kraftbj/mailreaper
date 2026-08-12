package rules

import (
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// helper to build a rule with the given match and expiration config.
func makeRule(mc db.MatchConfig, ec db.ExpirationConfig) db.Rule {
	return db.Rule{
		ID:               "test-rule",
		Name:             "Test Rule",
		Enabled:          true,
		Priority:         10,
		MatchConfig:      mc,
		ExpirationConfig: ec,
	}
}

// ---------- MatchesRule ----------

func TestMatchesRule_SenderPattern(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		SenderPatterns: []string{"*@example.com"},
	}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	msg := &Message{Author: "user@example.com", Subject: "Hello"}
	if !MatchesRule(msg, rule) {
		t.Error("expected sender pattern to match bare email")
	}

	msg2 := &Message{Author: "user@other.com", Subject: "Hello"}
	if MatchesRule(msg2, rule) {
		t.Error("expected sender pattern to not match different domain")
	}
}

func TestMatchesRule_SubjectPattern(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		SubjectPatterns: []string{"*verification code*"},
	}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	msg := &Message{Author: "noreply@example.com", Subject: "Your verification code is 12345"}
	if !MatchesRule(msg, rule) {
		t.Error("expected subject pattern to match")
	}

	msg2 := &Message{Author: "noreply@example.com", Subject: "Welcome to our service"}
	if MatchesRule(msg2, rule) {
		t.Error("expected subject pattern to not match unrelated subject")
	}
}

func TestMatchesRule_DisplayNameSender(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		SenderPatterns: []string{"*@example.com"},
	}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	// Author in "Display Name <email>" format
	msg := &Message{Author: "Example Company <noreply@example.com>", Subject: "Hello"}
	if !MatchesRule(msg, rule) {
		t.Error("expected display name sender to match via extracted email")
	}
}

func TestMatchesRule_NoPatterns(t *testing.T) {
	// Rule with no patterns should match all messages (like the Expires header rule)
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "header"})

	msg := &Message{Author: "anyone@anywhere.com", Subject: "Random email"}
	if !MatchesRule(msg, rule) {
		t.Error("expected rule with no patterns to match all messages")
	}
}

func TestMatchesRule_FolderFilter(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		Folders: []string{"Inbox/Promotions"},
	}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	msg := &Message{Author: "user@example.com", Subject: "Sale!", Folder: "Inbox/Promotions"}
	if !MatchesRule(msg, rule) {
		t.Error("expected folder match")
	}

	msg2 := &Message{Author: "user@example.com", Subject: "Sale!", Folder: "Inbox"}
	if MatchesRule(msg2, rule) {
		t.Error("expected non-matching folder to fail")
	}
}

func TestMatchesRule_SenderAndSubjectBothRequired(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		SenderPatterns:  []string{"*@example.com"},
		SubjectPatterns: []string{"*sale*"},
	}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	// Both match
	msg := &Message{Author: "promo@example.com", Subject: "Big sale today"}
	if !MatchesRule(msg, rule) {
		t.Error("expected both sender and subject to match")
	}

	// Sender matches but not subject
	msg2 := &Message{Author: "promo@example.com", Subject: "Welcome"}
	if MatchesRule(msg2, rule) {
		t.Error("expected mismatch when subject doesn't match")
	}

	// Subject matches but not sender
	msg3 := &Message{Author: "promo@other.com", Subject: "Big sale today"}
	if MatchesRule(msg3, rule) {
		t.Error("expected mismatch when sender doesn't match")
	}
}

// ---------- EvaluateTTL ----------

func TestEvaluateTTL_Expired(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	// Message sent 2 hours ago — should be expired (TTL is 1h)
	msg := &Message{Date: time.Now().Add(-2 * time.Hour)}
	verdict := EvaluateTTL(msg, rule)
	if verdict == nil {
		t.Fatal("expected non-nil verdict for expired message")
	}
	if !verdict.Expired {
		t.Error("expected Expired=true")
	}
	if verdict.ExpiresAt == nil {
		t.Error("expected ExpiresAt to be set")
	}
	if verdict.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %v", verdict.Confidence)
	}
}

func TestEvaluateTTL_NotExpired(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "ttl", Hours: 1})

	// Message sent 30 minutes ago — should NOT be expired (TTL is 1h)
	msg := &Message{Date: time.Now().Add(-30 * time.Minute)}
	verdict := EvaluateTTL(msg, rule)
	if verdict != nil {
		t.Errorf("expected nil verdict for unexpired message, got %+v", verdict)
	}
}

// ---------- EvaluateHeader ----------

func TestEvaluateHeader_PastHeader(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "header"})

	// RFC1123Z format, past date
	past := time.Now().Add(-24 * time.Hour).Format(time.RFC1123Z)
	headers := map[string][]string{
		"Expires": {past},
	}

	verdict := EvaluateHeader(headers, rule)
	if verdict == nil {
		t.Fatal("expected non-nil verdict for past Expires header")
	}
	if !verdict.Expired {
		t.Error("expected Expired=true")
	}
}

func TestEvaluateHeader_FutureHeader(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "header"})

	future := time.Now().Add(24 * time.Hour).Format(time.RFC1123Z)
	headers := map[string][]string{
		"Expires": {future},
	}

	verdict := EvaluateHeader(headers, rule)
	if verdict != nil {
		t.Errorf("expected nil verdict for future Expires header, got %+v", verdict)
	}
}

func TestEvaluateHeader_NoHeader(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "header"})

	verdict := EvaluateHeader(map[string][]string{}, rule)
	if verdict != nil {
		t.Errorf("expected nil when no Expires header present, got %+v", verdict)
	}
}

func TestEvaluateHeader_RFC1123Format(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{Type: "header"})

	// RFC1123 (no timezone offset)
	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC1123)
	headers := map[string][]string{
		"Expires": {past},
	}

	verdict := EvaluateHeader(headers, rule)
	if verdict == nil {
		t.Fatal("expected non-nil verdict for RFC1123-formatted past Expires header")
	}
	if !verdict.Expired {
		t.Error("expected Expired=true")
	}
}

// TestEvaluateHeaderUsesLowercaseKeys builds the headers map the way
// imap.FetchNewMessages builds it (client.go lowercases every key). The
// pre-existing tests in this file use a capital "Expires", which is why a
// rule that could never fire in production passed CI.
func TestEvaluateHeaderUsesLowercaseKeys(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour).Format(time.RFC1123Z)
	headers := map[string][]string{"expires": {past}}

	verdict := EvaluateHeader(headers, db.Rule{ID: "builtin-expires"})
	if verdict == nil {
		t.Fatal("expected a verdict for a past Expires header with a lowercase key")
	}
	if !verdict.Expired {
		t.Error("expected Expired = true")
	}
}

// ---------- EvaluateContentRegex ----------

func TestEvaluateContentRegex_Match(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{
		Type:    "content-regex",
		Pattern: `(?i)expires[:\s]+(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`,
	})

	past := time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339)
	body := "This offer expires: " + past + " after that it's gone."
	verdict := EvaluateContentRegex(body, rule)
	if verdict == nil {
		t.Fatal("expected non-nil verdict when regex matches a past date")
	}
	if !verdict.Expired {
		t.Error("expected Expired=true")
	}
	if verdict.Confidence != 0.9 {
		t.Errorf("expected confidence 0.9, got %v", verdict.Confidence)
	}
}

func TestEvaluateContentRegex_NoMatch(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{
		Type:    "content-regex",
		Pattern: `(?i)expires[:\s]+(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`,
	})

	body := "There is no expiration date in this email."
	verdict := EvaluateContentRegex(body, rule)
	if verdict != nil {
		t.Errorf("expected nil verdict when no match, got %+v", verdict)
	}
}

func TestEvaluateContentRegex_FutureDate(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{
		Type:    "content-regex",
		Pattern: `(?i)expires[:\s]+(\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}Z)`,
	})

	future := time.Now().Add(24 * time.Hour).UTC().Format(time.RFC3339)
	body := "This offer expires: " + future
	verdict := EvaluateContentRegex(body, rule)
	if verdict != nil {
		t.Errorf("expected nil for future date, got %+v", verdict)
	}
}

func TestEvaluateContentRegex_EmptyBody(t *testing.T) {
	rule := makeRule(db.MatchConfig{}, db.ExpirationConfig{
		Type:    "content-regex",
		Pattern: `(?i)expires[:\s]+(\d{4}-\d{2}-\d{2})`,
	})

	verdict := EvaluateContentRegex("", rule)
	if verdict != nil {
		t.Errorf("expected nil for empty body, got %+v", verdict)
	}
}

// ---------- EvaluateClassify ----------

func TestEvaluateClassify(t *testing.T) {
	rule := makeRule(db.MatchConfig{
		SenderPatterns: []string{"*@paypal.com"},
	}, db.ExpirationConfig{Type: "classify"})
	rule.Name = "Receipts → Paper-Trail"

	verdict := EvaluateClassify(rule)
	if verdict == nil {
		t.Fatal("expected non-nil verdict from EvaluateClassify")
	}
	if !verdict.Classified {
		t.Error("expected Classified=true")
	}
	if verdict.Expired {
		t.Error("expected Expired=false for classify verdict")
	}
	if verdict.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %v", verdict.Confidence)
	}
	if verdict.Reason != rule.Name {
		t.Errorf("expected Reason=%q, got %q", rule.Name, verdict.Reason)
	}
}
