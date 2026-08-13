package server_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	"github.com/kraftbj/mailreaper/internal/server"
)

func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	d, err := db.Open(path)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func newTestServer(t *testing.T) (*server.Server, *db.DB) {
	t.Helper()
	d := openTestDB(t)
	s := server.NewServer(d, "127.0.0.1", 0)
	return s, d
}

func TestGetRules(t *testing.T) {
	s, d := newTestServer(t)

	rule := db.Rule{
		ID:       "user-test-1",
		Name:     "Test Rule",
		Enabled:  true,
		Priority: 10,
		Action:   "move",
		ExpirationConfig: db.ExpirationConfig{
			Type:  "ttl",
			Hours: 24,
		},
	}
	if err := d.SaveRule(rule); err != nil {
		t.Fatalf("SaveRule: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var rules []db.Rule
	if err := json.NewDecoder(rec.Body).Decode(&rules); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(rules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(rules))
	}
	if rules[0].ID != "user-test-1" {
		t.Errorf("expected rule ID %q, got %q", "user-test-1", rules[0].ID)
	}
}

func TestGetRulesEmptyArray(t *testing.T) {
	s, _ := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/api/rules", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	// Must be [] not null
	if body != "[]\n" && body != "[]" {
		t.Errorf("expected empty JSON array, got %q", body)
	}
}

func TestGetActivity(t *testing.T) {
	s, d := newTestServer(t)

	if err := d.UpsertAccount("acc-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	entry := db.ActivityEntry{
		Type:            "move",
		AccountID:       "acc-1",
		MessageIDHeader: "<test@example.com>",
		Subject:         "Test email",
		Sender:          "sender@example.com",
		RuleName:        "TTL rule",
		Destination:     "Expired",
		Reason:          "expired by TTL",
		Confidence:      1.0,
	}
	if err := d.LogActivity(entry); err != nil {
		t.Fatalf("LogActivity: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/activity", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var entries []db.ActivityEntry
	if err := json.NewDecoder(rec.Body).Decode(&entries); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Subject != "Test email" {
		t.Errorf("expected subject %q, got %q", "Test email", entries[0].Subject)
	}
}

func TestGetPendingVerdicts(t *testing.T) {
	s, d := newTestServer(t)

	if err := d.UpsertAccount("acc-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	verdict := db.Verdict{
		AccountID:       "acc-1",
		MessageIDHeader: "<pending@example.com>",
		Subject:         "Pending verdict email",
		Sender:          "sender@example.com",
		SentAt:          time.Now().UTC(),
		Status:          "pending",
		EvaluatedAt:     time.Now().UTC(),
	}
	if err := d.SaveVerdict(verdict); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/verdicts/pending", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var verdicts []db.Verdict
	if err := json.NewDecoder(rec.Body).Decode(&verdicts); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(verdicts) != 1 {
		t.Fatalf("expected 1 verdict, got %d", len(verdicts))
	}
	if verdicts[0].Subject != "Pending verdict email" {
		t.Errorf("expected subject %q, got %q", "Pending verdict email", verdicts[0].Subject)
	}
}

func TestUpdateVerdictStatus(t *testing.T) {
	s, d := newTestServer(t)

	if err := d.UpsertAccount("acc-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	verdict := db.Verdict{
		AccountID:       "acc-1",
		MessageIDHeader: "<update-me@example.com>",
		Subject:         "Update status test",
		Sender:          "sender@example.com",
		SentAt:          time.Now().UTC(),
		Status:          "pending",
		EvaluatedAt:     time.Now().UTC(),
	}
	if err := d.SaveVerdict(verdict); err != nil {
		t.Fatalf("SaveVerdict: %v", err)
	}

	body := bytes.NewBufferString(`{"status":"approved"}`)
	req := httptest.NewRequest(http.MethodPut, "/api/verdicts/%3Cupdate-me%40example.com%3E/status", body)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	// Verify in DB
	v, err := d.GetVerdictByMessageID("<update-me@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("verdict not found after update")
	}
	if v.Status != "approved" {
		t.Errorf("expected status %q, got %q", "approved", v.Status)
	}
}

func TestGetCategories(t *testing.T) {
	s, d := newTestServer(t)

	cat := db.Category{
		ID:         "cat-1",
		Name:       "Paper-Trail",
		FolderName: "Paper-Trail",
		Icon:       "📄",
		Color:      "#blue",
	}
	if err := d.SaveCategory(cat); err != nil {
		t.Fatalf("SaveCategory: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/api/categories", nil)
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	var cats []db.Category
	if err := json.NewDecoder(rec.Body).Decode(&cats); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	/* The database also carries the seeded "expired" category (see the
	v3_seed_expired_category one-shot migration), so the saved "cat-1" plus
	that seed makes exactly 2 rows. Assert the exact count so a duplicate
	or leaked third category would still fail this test, then look up
	"cat-1" by ID to check its shape. */
	if len(cats) != 2 {
		t.Fatalf("expected 2 categories (cat-1 + seeded expired), got %d", len(cats))
	}
	var found *db.Category
	for i := range cats {
		if cats[i].ID == "cat-1" {
			found = &cats[i]
			break
		}
	}
	if found == nil {
		t.Fatalf("expected category %q in response, got %d categories", "cat-1", len(cats))
	}
	if found.Name != "Paper-Trail" {
		t.Errorf("expected name %q, got %q", "Paper-Trail", found.Name)
	}
}

// TestRescanClearsEverything verifies POST /api/rescan clears both executed
// and pending verdicts -- it must keep its full-clear semantics so a rescan
// can re-evaluate messages it already acted on.
func TestRescanClearsEverything(t *testing.T) {
	s, d := newTestServer(t)

	if err := d.UpsertAccount("acc-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	now := time.Now().UTC()
	executed := db.Verdict{
		AccountID:         "acc-1",
		MessageIDHeader:   "<executed@example.com>",
		Subject:           "Executed verdict",
		Sender:            "sender@example.com",
		SentAt:            now,
		Status:            "executed",
		DestinationFolder: "Expired",
		EvaluatedAt:       now,
		ActedAt:           &now,
	}
	pending := db.Verdict{
		AccountID:       "acc-1",
		MessageIDHeader: "<pending@example.com>",
		Subject:         "Pending verdict",
		Sender:          "sender@example.com",
		SentAt:          now,
		Status:          "pending",
		EvaluatedAt:     now,
	}
	if err := d.SaveVerdict(executed); err != nil {
		t.Fatalf("SaveVerdict(executed): %v", err)
	}
	if err := d.SaveVerdict(pending); err != nil {
		t.Fatalf("SaveVerdict(pending): %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/rescan", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	if got, err := d.GetVerdictByMessageID("<executed@example.com>"); err != nil {
		t.Fatalf("GetVerdictByMessageID(executed): %v", err)
	} else if got != nil {
		t.Errorf("expected executed verdict to be cleared, got %+v", got)
	}
	if got, err := d.GetVerdictByMessageID("<pending@example.com>"); err != nil {
		t.Fatalf("GetVerdictByMessageID(pending): %v", err)
	} else if got != nil {
		t.Errorf("expected pending verdict to be cleared, got %+v", got)
	}
}

// TestRefreshPreservesExecutedVerdicts verifies POST /api/refresh clears only
// pending verdicts, leaving executed ones (and the messages they represent)
// alone -- the cheap path that does not force every already-acted-on message
// back through the LLM.
func TestRefreshPreservesExecutedVerdicts(t *testing.T) {
	s, d := newTestServer(t)

	if err := d.UpsertAccount("acc-1", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount: %v", err)
	}

	now := time.Now().UTC()
	executed := db.Verdict{
		AccountID:         "acc-1",
		MessageIDHeader:   "<executed@example.com>",
		Subject:           "Executed verdict",
		Sender:            "sender@example.com",
		SentAt:            now,
		Status:            "executed",
		DestinationFolder: "Expired",
		EvaluatedAt:       now,
		ActedAt:           &now,
	}
	pending := db.Verdict{
		AccountID:       "acc-1",
		MessageIDHeader: "<pending@example.com>",
		Subject:         "Pending verdict",
		Sender:          "sender@example.com",
		SentAt:          now,
		Status:          "pending",
		EvaluatedAt:     now,
	}
	if err := d.SaveVerdict(executed); err != nil {
		t.Fatalf("SaveVerdict(executed): %v", err)
	}
	if err := d.SaveVerdict(pending); err != nil {
		t.Fatalf("SaveVerdict(pending): %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/api/refresh", nil)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	s.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	got, err := d.GetVerdictByMessageID("<executed@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID(executed): %v", err)
	}
	if got == nil {
		t.Fatal("expected executed verdict to survive /api/refresh, got nil")
	}
	if got.Status != "executed" {
		t.Errorf("expected executed verdict status to remain %q, got %q", "executed", got.Status)
	}

	if got, err := d.GetVerdictByMessageID("<pending@example.com>"); err != nil {
		t.Fatalf("GetVerdictByMessageID(pending): %v", err)
	} else if got != nil {
		t.Errorf("expected pending verdict to be cleared, got %+v", got)
	}
}

// Ensure tests don't require an actual UI directory.
var _ = os.DevNull
