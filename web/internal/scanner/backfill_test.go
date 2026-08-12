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

// fakeOllamaMessage and fakeOllamaResponse mirror the shape of Ollama's
// /api/chat response (see internal/llm/adapter.go's unexported ollama*
// types, which cannot be imported from here). Only the fields the adapter
// actually reads -- message.content, holding the model's raw JSON text --
// need to be present.
type fakeOllamaMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type fakeOllamaResponse struct {
	Message fakeOllamaMessage `json:"message"`
}

// TestBackfillClearsCachedVerdict proves --backfill actually re-asks the
// LLM rather than replaying a stale cached answer. A prior version of this
// test only asserted that GetCachedVerdict returned nil afterward, which
// passes even when the "llm" rule branch is never reached (testConfig's
// Provider is "none", and no rule was seeded) -- it was indistinguishable
// from a test of RemoveCachedVerdict alone. This version seeds a real
// enabled "llm" rule and points the "ollama" provider at a local
// httptest.Server standing in for the model, so evaluateLLM's cache-miss
// path actually executes.
//
// Mechanism: the seeded cache entry and the server's canned response carry
// different Reason text and different (but both past, both plausible)
// ExpiresAt values. If the stale cache survives backfill, evaluateLLM's
// cache-hit branch fires: the server is never called (call count stays 0)
// and the saved verdict's Reason is the stale text. Only if the cache was
// actually removed does evaluateLLM fall through to its cache-miss branch,
// call the server, and save the fresh Reason. This reproduces the exact
// regression: rerunning against the pre-fix ClearVerdictByMessageID code
// (which deletes the verdict row but leaves the cache untouched) fails
// with the stale Reason and a zero call count.
func TestBackfillClearsCachedVerdict(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := database.SaveCategory(db.Category{ID: "expired", Name: "Expired", FolderName: "Expired"}); err != nil {
		t.Fatalf("save category: %v", err)
	}
	if err := database.SaveRule(db.Rule{
		ID:               "rule-llm-test",
		Name:             "LLM deadline rule",
		Enabled:          true,
		Priority:         10,
		ExpirationConfig: db.ExpirationConfig{Type: "llm"},
		Action:           "move",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	sentAt := time.Now().Add(-72 * time.Hour)
	staleExpiresAt := time.Now().Add(-48 * time.Hour) // past, after sentAt
	freshExpiresAt := time.Now().Add(-24 * time.Hour) // past, after sentAt, distinct from stale
	const staleReason = "stale verdict from the previous model"
	const freshReason = "fresh verdict from the local test server"

	if err := database.SetCachedVerdict("<msg-1@test>", db.CachedVerdict{
		ExpiresAt:  staleExpiresAt.Format(time.RFC3339),
		Reason:     staleReason,
		Confidence: 0.9,
	}); err != nil {
		t.Fatalf("SetCachedVerdict: %v", err)
	}

	var llmCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
		content, err := json.Marshal(struct {
			ExpiresAt  string  `json:"expiresAt"`
			Reason     string  `json:"reason"`
			Confidence float64 `json:"confidence"`
		}{
			ExpiresAt:  freshExpiresAt.Format(time.RFC3339),
			Reason:     freshReason,
			Confidence: 0.95,
		})
		if err != nil {
			t.Fatalf("marshal fake LLM content: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(fakeOllamaResponse{
			Message: fakeOllamaMessage{Role: "assistant", Content: string(content)},
		}); err != nil {
			t.Fatalf("encode fake ollama response: %v", err)
		}
	}))
	defer ts.Close()

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "ollama",
		Ollama:   config.OllamaConfig{Endpoint: ts.URL, Model: "test-model"},
	}

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<msg-1@test>",
		Subject:   "Some sale",
		Sender:    "promo@example.com",
		Date:      sentAt,
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

	if got := atomic.LoadInt32(&llmCalls); got != 1 {
		t.Errorf("LLM server called %d times, want 1 -- backfill did not re-ask the LLM (stale cache was likely reused)", got)
	}

	v, err := database.GetVerdictByMessageID("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a saved verdict after backfill, got nil")
	}
	if v.Reason == staleReason {
		t.Error("backfill reused the stale cached verdict instead of re-evaluating")
	}
	if v.Reason != freshReason {
		t.Errorf("Reason = %q, want the fresh evaluation's reason %q", v.Reason, freshReason)
	}

	cached, err := database.GetCachedVerdict("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetCachedVerdict: %v", err)
	}
	if cached == nil {
		t.Fatal("expected the fresh answer to be re-cached, got nil")
	}
	if cached.Reason != freshReason {
		t.Errorf("cached Reason = %q, want %q", cached.Reason, freshReason)
	}
}
