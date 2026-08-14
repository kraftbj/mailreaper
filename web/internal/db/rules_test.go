package db_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// openTestDB creates a temporary SQLite DB for use in a single test.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("openTestDB: %v", err)
	}
	t.Cleanup(func() {
		database.Close()
		os.Remove(path)
	})
	return database
}

func TestGetRulesEmpty(t *testing.T) {
	d := openTestDB(t)
	rules, err := d.GetRules()
	if err != nil {
		t.Fatalf("GetRules() error = %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}

func TestSaveAndGetRule(t *testing.T) {
	d := openTestDB(t)

	rule := db.Rule{
		ID:       "user-test-1",
		Name:     "Test rule",
		Enabled:  true,
		Priority: 50,
		Builtin:  false,
		MatchConfig: db.MatchConfig{
			SenderPatterns:  []string{"*@example.com"},
			SubjectPatterns: []string{"*test*"},
		},
		ExpirationConfig: db.ExpirationConfig{
			Type:  "ttl",
			Hours: 24,
		},
		Action:          "move",
		GracePeriodDays: 2,
	}

	if err := d.SaveRule(rule); err != nil {
		t.Fatalf("SaveRule() error = %v", err)
	}

	got, err := d.GetRule("user-test-1")
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if got == nil {
		t.Fatal("GetRule() returned nil")
	}
	if got.ID != rule.ID {
		t.Errorf("ID: got %q, want %q", got.ID, rule.ID)
	}
	if got.Name != rule.Name {
		t.Errorf("Name: got %q, want %q", got.Name, rule.Name)
	}
	if !got.Enabled {
		t.Error("Enabled: got false, want true")
	}
	if got.Priority != rule.Priority {
		t.Errorf("Priority: got %d, want %d", got.Priority, rule.Priority)
	}
	if got.ExpirationConfig.Type != "ttl" {
		t.Errorf("ExpirationConfig.Type: got %q, want %q", got.ExpirationConfig.Type, "ttl")
	}
	if got.ExpirationConfig.Hours != 24 {
		t.Errorf("ExpirationConfig.Hours: got %d, want 24", got.ExpirationConfig.Hours)
	}
	if len(got.MatchConfig.SenderPatterns) != 1 || got.MatchConfig.SenderPatterns[0] != "*@example.com" {
		t.Errorf("MatchConfig.SenderPatterns unexpected: %v", got.MatchConfig.SenderPatterns)
	}
}

func TestGetRuleNotFound(t *testing.T) {
	d := openTestDB(t)
	got, err := d.GetRule("nonexistent")
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestGetRulesSortedByPriority(t *testing.T) {
	d := openTestDB(t)

	for _, r := range []db.Rule{
		{ID: "r-30", Name: "C", Priority: 30, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
		{ID: "r-10", Name: "A", Priority: 10, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
		{ID: "r-20", Name: "B", Priority: 20, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
	} {
		if err := d.SaveRule(r); err != nil {
			t.Fatalf("SaveRule(%q) error = %v", r.ID, err)
		}
	}

	rules, err := d.GetRules()
	if err != nil {
		t.Fatalf("GetRules() error = %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	priorities := []int{rules[0].Priority, rules[1].Priority, rules[2].Priority}
	if priorities[0] != 10 || priorities[1] != 20 || priorities[2] != 30 {
		t.Errorf("rules not sorted by priority: %v", priorities)
	}
}

func TestDeleteRule(t *testing.T) {
	d := openTestDB(t)

	r := db.Rule{
		ID:               "user-del",
		Name:             "To delete",
		ExpirationConfig: db.ExpirationConfig{Type: "ttl"},
	}
	if err := d.SaveRule(r); err != nil {
		t.Fatalf("SaveRule() error = %v", err)
	}

	if err := d.DeleteRule("user-del"); err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}

	got, err := d.GetRule("user-del")
	if err != nil {
		t.Fatalf("GetRule() after delete error = %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

// TestDeleteRuleWithReferencingVerdict is the Critical regression from the
// whole-branch review: foreign_keys(1) was enabled in the DSN (Open) for the
// first time, but verdicts.rule_id has no ON DELETE clause, so it defaults
// to NO ACTION. Deleting a rule that any verdict references used to fail
// with "FOREIGN KEY constraint failed" -- breaking the Delete button for any
// rule that had ever matched a message. DeleteRule must clear the reference
// instead of leaving it to the database default.
func TestDeleteRuleWithReferencingVerdict(t *testing.T) {
	d := openTestDB(t)

	r := db.Rule{
		ID:               "user-referenced",
		Name:             "Referenced by a verdict",
		Enabled:          true,
		ExpirationConfig: db.ExpirationConfig{Type: "ttl", Hours: 24},
	}
	if err := d.SaveRule(r); err != nil {
		t.Fatalf("SaveRule() error = %v", err)
	}

	if err := d.UpsertAccount("acct", "Test Account"); err != nil {
		t.Fatalf("UpsertAccount() error = %v", err)
	}

	ruleID := r.ID
	now := time.Now().UTC()
	v := db.Verdict{
		AccountID:       "acct",
		MessageIDHeader: "<referencing@example.com>",
		Subject:         "Referencing verdict",
		Sender:          "sender@example.com",
		SentAt:          now,
		RuleID:          &ruleID,
		Status:          "executed",
		EvaluatedAt:     now,
		ActedAt:         &now,
	}
	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error = %v", err)
	}

	if err := d.DeleteRule(r.ID); err != nil {
		t.Fatalf("DeleteRule() error = %v", err)
	}

	got, err := d.GetRule(r.ID)
	if err != nil {
		t.Fatalf("GetRule() after delete error = %v", err)
	}
	if got != nil {
		t.Error("expected rule to be gone after delete")
	}

	gotVerdict, err := d.GetVerdictByMessageID(v.MessageIDHeader)
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error = %v", err)
	}
	if gotVerdict == nil {
		t.Fatal("expected verdict to survive rule deletion")
	}
	if gotVerdict.RuleID != nil {
		t.Errorf("expected verdict.RuleID to be nil after rule deletion, got %q", *gotVerdict.RuleID)
	}
}

func TestUpdateRuleUpsert(t *testing.T) {
	d := openTestDB(t)

	r := db.Rule{
		ID:               "user-upsert",
		Name:             "Original",
		Enabled:          false,
		Priority:         50,
		ExpirationConfig: db.ExpirationConfig{Type: "ttl", Hours: 1},
	}
	if err := d.SaveRule(r); err != nil {
		t.Fatalf("initial SaveRule() error = %v", err)
	}

	r.Name = "Updated"
	r.Enabled = true
	r.Priority = 99
	if err := d.SaveRule(r); err != nil {
		t.Fatalf("update SaveRule() error = %v", err)
	}

	got, err := d.GetRule("user-upsert")
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if got.Name != "Updated" {
		t.Errorf("Name: got %q, want %q", got.Name, "Updated")
	}
	if !got.Enabled {
		t.Error("Enabled: got false, want true")
	}
	if got.Priority != 99 {
		t.Errorf("Priority: got %d, want 99", got.Priority)
	}
}

func TestSeedDefaultsIdempotent(t *testing.T) {
	d := openTestDB(t)

	defaults := []db.Rule{
		{
			ID:               "builtin-test-1",
			Name:             "Builtin 1",
			Enabled:          true,
			Priority:         1,
			Builtin:          true,
			ExpirationConfig: db.ExpirationConfig{Type: "header"},
		},
		{
			ID:               "builtin-test-2",
			Name:             "Builtin 2",
			Enabled:          true,
			Priority:         10,
			Builtin:          true,
			ExpirationConfig: db.ExpirationConfig{Type: "ttl", Hours: 2},
		},
	}

	// Seed once.
	if err := d.SeedDefaults(defaults); err != nil {
		t.Fatalf("first SeedDefaults() error = %v", err)
	}

	// Seed again — should not error or duplicate.
	if err := d.SeedDefaults(defaults); err != nil {
		t.Fatalf("second SeedDefaults() error = %v", err)
	}

	rules, err := d.GetRules()
	if err != nil {
		t.Fatalf("GetRules() error = %v", err)
	}
	if len(rules) != 2 {
		t.Errorf("expected 2 rules after double seed, got %d", len(rules))
	}
}

func TestSeedDefaultsDoesNotOverwrite(t *testing.T) {
	d := openTestDB(t)

	// Manually insert a rule.
	existing := db.Rule{
		ID:               "builtin-overlap",
		Name:             "Original name",
		Enabled:          true,
		Priority:         5,
		Builtin:          true,
		ExpirationConfig: db.ExpirationConfig{Type: "ttl", Hours: 1},
	}
	if err := d.SaveRule(existing); err != nil {
		t.Fatalf("SaveRule() error = %v", err)
	}

	// SeedDefaults with a modified version of the same ID.
	seeded := existing
	seeded.Name = "Should not overwrite"
	if err := d.SeedDefaults([]db.Rule{seeded}); err != nil {
		t.Fatalf("SeedDefaults() error = %v", err)
	}

	got, err := d.GetRule("builtin-overlap")
	if err != nil {
		t.Fatalf("GetRule() error = %v", err)
	}
	if got.Name != "Original name" {
		t.Errorf("SeedDefaults overwrote existing rule: got %q", got.Name)
	}
}

func TestGetEnabledRules(t *testing.T) {
	d := openTestDB(t)

	rules := []db.Rule{
		{ID: "enabled-1", Name: "E1", Enabled: true, Priority: 1, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
		{ID: "disabled-1", Name: "D1", Enabled: false, Priority: 2, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
		{ID: "enabled-2", Name: "E2", Enabled: true, Priority: 3, ExpirationConfig: db.ExpirationConfig{Type: "ttl"}},
	}
	for _, r := range rules {
		if err := d.SaveRule(r); err != nil {
			t.Fatalf("SaveRule(%q) error = %v", r.ID, err)
		}
	}

	enabled, err := d.GetEnabledRules()
	if err != nil {
		t.Fatalf("GetEnabledRules() error = %v", err)
	}
	if len(enabled) != 2 {
		t.Errorf("expected 2 enabled rules, got %d", len(enabled))
	}
	for _, r := range enabled {
		if !r.Enabled {
			t.Errorf("got disabled rule %q in enabled list", r.ID)
		}
	}
}
