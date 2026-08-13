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

	// Matches the key evaluateLLM builds via Scanner.llmCacheKey("analysis")
	// for the "ollama" provider / "test-model" configured below.
	const staleCacheKey = "ollama:test-model|analysis"
	if err := database.SetCachedVerdict("<msg-1@test>", staleCacheKey, db.CachedVerdict{
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

	cached, err := database.GetCachedVerdict("<msg-1@test>", staleCacheKey)
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

// TestBackfillNilVerdictFromLLMFailureDoesNotRescue proves that a real LLM
// outage during --backfill does not empty a triage folder into the rescue
// folder (typically INBOX). This reproduces the actual regression rather
// than constructing a nil *rules.RuleVerdict by hand: it seeds a real
// enabled "llm" rule and points the "ollama" provider at a local
// httptest.Server that answers every request with HTTP 500, so
// evaluateLLM's error path fires for real, evaluateMessage's "llm" case
// logs the error and falls through, and evaluateMessage returns a genuine
// nil verdict -- exactly what a Gemini/Ollama outage produces in
// production.
//
// "we learned nothing" must not be scored as "confidently belongs in the
// rescue folder": backfill previously assigned confidence 1.0 to a nil
// verdict, which cleared the < 0.7 gate and moved the message. The message
// must stay in its triage folder instead.
func TestBackfillNilVerdictFromLLMFailureDoesNotRescue(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := database.SaveCategory(db.Category{
		ID:         "promotions",
		Name:       "Promotions",
		FolderName: "Folders/AI-Triage/Promotions",
	}); err != nil {
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

	var llmCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer ts.Close()

	cfg := testConfig()
	cfg.LLM = config.LLMConfig{
		Provider: "ollama",
		Ollama:   config.OllamaConfig{Endpoint: ts.URL, Model: "test-model"},
	}

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<msg-outage@test>",
		Subject:   "unclassifiable during outage",
		Sender:    "someone@example.test",
		Date:      time.Now().Add(-72 * time.Hour),
		Folder:    "Folders/AI-Triage/Promotions",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Promotions": {msg},
		},
	}
	s := New(database, cfg)

	stats, err := s.BackfillFolders(context.Background(), client, "acct1", "INBOX")
	if err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	if got := atomic.LoadInt32(&llmCalls); got != 1 {
		t.Fatalf("LLM server called %d times, want 1 -- test did not actually exercise the outage path", got)
	}
	if stats.Moved != 0 {
		t.Errorf("Moved = %d, want 0: a nil verdict from an LLM outage must not move mail", stats.Moved)
	}
	if len(client.movedMsgs) != 0 {
		t.Errorf("moved %d message(s) on a nil verdict from an LLM outage", len(client.movedMsgs))
	}
}

// TestBackfillMovesReclassifiedMessage exercises the orchestration loop
// itself, not just the pure backfillDestination helper: a message sitting in
// one triage folder is re-evaluated against a "classify" rule (deterministic,
// confidence 1.0 -- see rules.EvaluateClassify) whose destination is a
// different folder, and the backfill must actually call EnsureFolder /
// MoveMessage and persist an "executed" verdict at the new destination.
func TestBackfillMovesReclassifiedMessage(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := database.SaveCategory(db.Category{
		ID:         "notifications",
		Name:       "Notifications",
		FolderName: "Folders/AI-Triage/Notifications",
	}); err != nil {
		t.Fatalf("save category: %v", err)
	}
	if err := database.SaveCategory(db.Category{
		ID:         "promotions",
		Name:       "Promotions",
		FolderName: "Folders/AI-Triage/Promotions",
	}); err != nil {
		t.Fatalf("save category: %v", err)
	}
	if err := database.SaveRule(db.Rule{
		ID:                "rule-classify-promo",
		Name:              "Promotions classify",
		Enabled:           true,
		Priority:          10,
		ExpirationConfig:  db.ExpirationConfig{Type: "classify", Category: "promotions"},
		Action:            "move",
		DestinationFolder: "Folders/AI-Triage/Promotions",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<backfill-move@test>",
		Subject:   "Big sale",
		Sender:    "deals@example.com",
		Date:      time.Now().Add(-48 * time.Hour),
		Folder:    "Folders/AI-Triage/Notifications",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Notifications": {msg},
		},
	}
	s := New(database, cfg)

	stats, err := s.BackfillFolders(context.Background(), client, "acct1", "INBOX")
	if err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	if stats.Moved != 1 {
		t.Errorf("stats.Moved = %d, want 1", stats.Moved)
	}
	if len(client.movedMsgs) != 1 || client.movedMsgs[0] != "Folders/AI-Triage/Promotions" {
		t.Errorf("movedMsgs = %v, want a single move to Folders/AI-Triage/Promotions", client.movedMsgs)
	}

	v, err := database.GetVerdictByMessageID("<backfill-move@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a saved verdict, got nil")
	}
	if v.Status != "executed" {
		t.Errorf("Status = %q, want executed", v.Status)
	}
	if v.DestinationFolder != "Folders/AI-Triage/Promotions" {
		t.Errorf("DestinationFolder = %q, want Folders/AI-Triage/Promotions", v.DestinationFolder)
	}
}

// TestBackfillLowConfidenceLeavesMessageInPlace proves the orchestration
// loop's own confidence < 0.7 gate (backfill.go, not the pure
// backfillDestination helper) is exercised: a real "llm-classify" call
// returns a matched category at confidence 0.5, which is enough for
// buildClassifyVerdict to return a non-nil Classified verdict (it only
// rejects Confidence <= 0), so the low-confidence rejection has to happen in
// BackfillFolders itself. A cached verdict cannot be pre-seeded for this,
// because BackfillFolders unconditionally calls RemoveCachedVerdict before
// evaluating (see backfill.go) -- so the LLM must actually be called, via
// the same httptest.Server pattern as the other backfill LLM tests.
func TestBackfillLowConfidenceLeavesMessageInPlace(t *testing.T) {
	database := openTestDB(t)

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}
	if err := database.SaveCategory(db.Category{
		ID:         "notifications",
		Name:       "Notifications",
		FolderName: "Folders/AI-Triage/Notifications",
	}); err != nil {
		t.Fatalf("save category: %v", err)
	}
	if err := database.SaveCategory(db.Category{
		ID:         "promotions",
		Name:       "Promotions",
		FolderName: "Folders/AI-Triage/Promotions",
	}); err != nil {
		t.Fatalf("save category: %v", err)
	}
	if err := database.SaveRule(db.Rule{
		ID:                "rule-llm-classify-promo",
		Name:              "Promotions llm-classify",
		Enabled:           true,
		Priority:          10,
		ExpirationConfig:  db.ExpirationConfig{Type: "llm-classify", Category: "promotions"},
		Action:            "move",
		DestinationFolder: "Folders/AI-Triage/Promotions",
	}); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	var llmCalls int32
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&llmCalls, 1)
		content, err := json.Marshal(struct {
			Category   string  `json:"category"`
			Reason     string  `json:"reason"`
			Confidence float64 `json:"confidence"`
		}{
			Category:   "promotions",
			Reason:     "looks promotional but not sure",
			Confidence: 0.5,
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
		MessageID: "<backfill-lowconf@test>",
		Subject:   "Maybe a sale?",
		Sender:    "deals@example.com",
		Date:      time.Now().Add(-48 * time.Hour),
		Folder:    "Folders/AI-Triage/Notifications",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Notifications": {msg},
		},
	}
	s := New(database, cfg)

	stats, err := s.BackfillFolders(context.Background(), client, "acct1", "INBOX")
	if err != nil {
		t.Fatalf("BackfillFolders: %v", err)
	}

	if got := atomic.LoadInt32(&llmCalls); got != 1 {
		t.Fatalf("LLM server called %d times, want 1 -- test did not exercise the real classify path", got)
	}
	if stats.Skipped != 1 {
		t.Errorf("stats.Skipped = %d, want 1", stats.Skipped)
	}
	if stats.Moved != 0 {
		t.Errorf("stats.Moved = %d, want 0 -- confidence 0.5 is below the 0.7 auto-execute threshold", stats.Moved)
	}
	if len(client.movedMsgs) != 0 {
		t.Errorf("moved %d message(s) despite low confidence", len(client.movedMsgs))
	}

	v, err := database.GetVerdictByMessageID("<backfill-lowconf@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("expected a saved verdict, got nil")
	}
	if v.Status != "pending" {
		t.Errorf("Status = %q, want pending", v.Status)
	}
	if v.DestinationFolder != "Folders/AI-Triage/Notifications" {
		t.Errorf("DestinationFolder = %q, want unchanged Folders/AI-Triage/Notifications", v.DestinationFolder)
	}
}
