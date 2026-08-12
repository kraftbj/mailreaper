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
	if len(cats) != 1 {
		t.Fatalf("expected 1 category, got %d", len(cats))
	}
	if cats[0].Name != "Paper-Trail" {
		t.Errorf("expected name %q, got %q", "Paper-Trail", cats[0].Name)
	}
}

// Ensure tests don't require an actual UI directory.
var _ = os.DevNull
