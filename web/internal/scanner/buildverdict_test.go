package scanner

import (
	"strings"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	"github.com/kraftbj/mailreaper/internal/llm"
)

// TestExtractValidExpiresAt covers the confidence guard and parse logic in
// the pure helper. The guard rejects low-confidence extractions per
// decision 3A.
func TestExtractValidExpiresAt(t *testing.T) {
	now := time.Now().UTC()
	past := now.Add(-24 * time.Hour).Format(time.RFC3339)
	future := now.Add(24 * time.Hour).Format(time.RFC3339)

	tests := []struct {
		name        string
		expiresAt   string
		confidence  float64
		wantValid   bool
		wantPast    bool
	}{
		{"empty string", "", 0.9, false, false},
		{"low confidence is rejected", past, 0.4, false, false},
		{"at threshold is accepted", past, minExpiryConfidence, true, true},
		{"high confidence past", past, 0.9, true, true},
		{"high confidence future", future, 0.9, true, false},
		{"unparseable", "next Tuesday", 0.9, false, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, gotPast, gotValid := extractValidExpiresAt(tt.expiresAt, tt.confidence)
			if gotValid != tt.wantValid {
				t.Errorf("valid: got %v, want %v", gotValid, tt.wantValid)
			}
			if tt.wantValid && gotPast != tt.wantPast {
				t.Errorf("past: got %v, want %v", gotPast, tt.wantPast)
			}
		})
	}
}

// TestParseExpiresAtLayouts covers the timestamp shapes the LLM actually
// returns. The prompt asks for RFC3339, but both gemini-2.5-flash and
// gemini-3.5-flash-lite intermittently drop the zone and return a bare
// wall-clock timestamp; those used to fail every layout and be discarded
// silently, throwing away a correctly-extracted deadline.
func TestParseExpiresAtLayouts(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want time.Time
	}{
		{
			name: "RFC3339 with offset",
			in:   "2026-06-26T09:00:00-06:00",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.FixedZone("", -6*60*60)),
		},
		{
			name: "RFC3339 UTC",
			in:   "2026-06-26T09:00:00Z",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		},
		{
			name: "zoneless wall clock is read as local",
			in:   "2026-06-26T09:00:00",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.Local),
		},
		{
			name: "zoneless without seconds",
			in:   "2026-06-26T09:00",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.Local),
		},
		{
			name: "zoneless with space separator",
			in:   "2026-06-26 09:00:00",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.Local),
		},
		{
			name: "date only is end of local day",
			in:   "2026-06-26",
			want: time.Date(2026, 6, 26, 23, 59, 59, 0, time.Local),
		},
		{
			name: "surrounding whitespace is tolerated",
			in:   "  2026-06-26T09:00:00Z  ",
			want: time.Date(2026, 6, 26, 9, 0, 0, 0, time.UTC),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, _ := parseExpiresAt(tt.in)
			if got == nil {
				t.Fatalf("parseExpiresAt(%q) = nil, want %s", tt.in, tt.want)
			}
			if !got.Equal(tt.want) {
				t.Errorf("parseExpiresAt(%q) = %s, want %s", tt.in, got, tt.want)
			}
		})
	}
}

// TestEndOfDayAcrossDST pins the day-advance arithmetic parseExpiresAt uses
// for date-only deadlines against real DST transitions. parseExpiresAt
// itself always reads time.Local, so the process zone can't be forced
// deterministically from within the test; instead this calls the endOfDay
// helper directly against explicitly-constructed America/Chicago times,
// which is deterministic regardless of the machine running the suite.
//
// A fixed Add(24*time.Hour - time.Second) is wrong here: on the 25-hour
// fall-back day it lands an hour before 23:59:59 (expiring a date-only
// deadline early — the exact failure direction this codebase exists to
// prevent), and on the 23-hour spring-forward day it overshoots onto the
// next calendar day. Confirmed by temporarily reverting endOfDay to that
// formula: this test fails both cases; with the AddDate implementation it
// passes both.
func TestEndOfDayAcrossDST(t *testing.T) {
	chicago, err := time.LoadLocation("America/Chicago")
	if err != nil {
		t.Skip("tzdata unavailable: " + err.Error())
	}

	tests := []struct {
		name     string
		midnight time.Time
		want     time.Time
	}{
		{
			name:     "25-hour fall-back day",
			midnight: time.Date(2026, 11, 1, 0, 0, 0, 0, chicago),
			want:     time.Date(2026, 11, 1, 23, 59, 59, 0, chicago),
		},
		{
			name:     "23-hour spring-forward day",
			midnight: time.Date(2026, 3, 8, 0, 0, 0, 0, chicago),
			want:     time.Date(2026, 3, 8, 23, 59, 59, 0, chicago),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := endOfDay(tt.midnight)
			if !got.Equal(tt.want) {
				t.Errorf("endOfDay(%s) = %s, want %s", tt.midnight, got, tt.want)
			}
		})
	}
}

// A date-only deadline for today must not read as already expired while
// that day is still in progress.
func TestParseExpiresAtDateOnlyTodayIsNotPast(t *testing.T) {
	today := time.Now().Format("2006-01-02")
	_, isPast := parseExpiresAt(today)
	if isPast {
		t.Errorf("date-only deadline of today (%s) reported as already past", today)
	}
}

// TestParseExpiresAtRejects covers input that must not yield a deadline.
// Anything accepted here would expire real mail on a fabricated date.
func TestParseExpiresAtRejects(t *testing.T) {
	for _, in := range []string{
		"",
		"   ",
		"next Tuesday",
		"sale ends Friday",
		"2026-13-45T99:99:99",
		"06/26/2026",
	} {
		t.Run(in, func(t *testing.T) {
			if got, _ := parseExpiresAt(in); got != nil {
				t.Errorf("parseExpiresAt(%q) = %s, want nil", in, got)
			}
		})
	}
}

// TestParseExpiresAtPastFlag verifies the past/future flag independently of
// layout, since that flag is what routes a message to Expired.
func TestParseExpiresAtPastFlag(t *testing.T) {
	past := time.Now().Add(-48 * time.Hour).Format("2006-01-02T15:04:05")
	future := time.Now().Add(48 * time.Hour).Format("2006-01-02T15:04:05")

	if _, isPast := parseExpiresAt(past); !isPast {
		t.Errorf("zoneless past timestamp %q: got past=false, want true", past)
	}
	if _, isPast := parseExpiresAt(future); isPast {
		t.Errorf("zoneless future timestamp %q: got past=true, want false", future)
	}
}

// fakeLLMClassifyRules returns a rule set covering two categories with
// distinct destination folders so we can verify routing.
func fakeLLMClassifyRules() (currentRule db.Rule, byCategory map[string]db.Rule) {
	promotion := db.Rule{
		ID:                "rule-promo",
		Name:              "Promotions",
		DestinationFolder: "Folders/AI-Triage/Promotions",
		ExpirationConfig:  db.ExpirationConfig{Type: "llm-classify", Category: "promotion"},
	}
	notification := db.Rule{
		ID:                "rule-notify",
		Name:              "Notifications",
		DestinationFolder: "Folders/AI-Triage/Notifications",
		ExpirationConfig:  db.ExpirationConfig{Type: "llm-classify", Category: "notification"},
	}
	return promotion, map[string]db.Rule{
		"promotion":    promotion,
		"notification": notification,
	}
}

// TestBuildClassifyVerdict_PastDeadlineRoutesToCanonicalExpired covers the
// scanner.go:317 routing bug fix: a past-deadline verdict must always land
// in the canonical Expired folder, not the iterated rule's destination.
func TestBuildClassifyVerdict_PastDeadlineRoutesToCanonicalExpired(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()
	const expiredFolder = "Folders/AI-Triage/Expired"

	past := time.Now().UTC().Add(-2 * 24 * time.Hour).Format(time.RFC3339)
	result := &llm.LLMResponse{
		Category:   "promotion",
		ExpiresAt:  past,
		Confidence: 0.9,
		Reason:     "sale ended",
	}

	v := buildClassifyVerdict(result, current, byCategory, expiredFolder)
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if !v.Expired {
		t.Error("expected Expired=true")
	}
	if v.Rule.DestinationFolder != expiredFolder {
		t.Errorf("destination: got %q, want %q (canonical Expired folder)", v.Rule.DestinationFolder, expiredFolder)
	}
}

// TestBuildClassifyVerdict_NotificationWithNoDeadlineLandsInCategoryFolder
// is the user's central complaint: a notification with no real deadline must
// stay in Notifications, not migrate to Expired. (USPS digest, LinkedIn
// invite, Basecamp activity, etc.)
func TestBuildClassifyVerdict_NotificationWithNoDeadlineLandsInCategoryFolder(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()

	result := &llm.LLMResponse{
		Category:   "notification",
		ExpiresAt:  "", // LLM correctly returns null per the new prompt
		Confidence: 0.85,
		Reason:     "USPS daily digest, no deadline",
	}

	v := buildClassifyVerdict(result, current, byCategory, "Folders/AI-Triage/Expired")
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Expired {
		t.Error("notification with no deadline must NOT be Expired")
	}
	if !v.Classified {
		t.Error("expected Classified=true")
	}
	if v.Rule.DestinationFolder != "Folders/AI-Triage/Notifications" {
		t.Errorf("destination: got %q, want Notifications folder", v.Rule.DestinationFolder)
	}
	if v.ExpiresAt != nil {
		t.Errorf("expected nil ExpiresAt, got %v", *v.ExpiresAt)
	}
}

// TestBuildClassifyVerdict_FutureDeadlinePersistsForLaterSweep verifies the
// promotional-email-with-future-sale-end case: the message lands in
// Promotions now, with ExpiresAt persisted on the verdict so
// SweepDeferredExpiries can move it to Expired when the deadline passes.
func TestBuildClassifyVerdict_FutureDeadlinePersistsForLaterSweep(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()

	future := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	result := &llm.LLMResponse{
		Category:   "promotion",
		ExpiresAt:  future,
		Confidence: 0.9,
		Reason:     "sale ends 4/28",
	}

	v := buildClassifyVerdict(result, current, byCategory, "Folders/AI-Triage/Expired")
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Expired {
		t.Error("future deadline must NOT mark Expired now")
	}
	if !v.Classified {
		t.Error("expected Classified=true")
	}
	if v.Rule.DestinationFolder != "Folders/AI-Triage/Promotions" {
		t.Errorf("destination: got %q, want Promotions", v.Rule.DestinationFolder)
	}
	if v.ExpiresAt == nil {
		t.Fatal("expected ExpiresAt to be persisted for later sweep")
	}
	if v.ExpiresAt.Before(time.Now().UTC()) {
		t.Error("persisted ExpiresAt must be in the future")
	}
}

// TestBuildClassifyVerdict_LowConfidenceExpiresAtRejected verifies the 3A
// confidence guard: an LLM-extracted deadline below the threshold is
// dropped, and the verdict falls through to plain classification.
func TestBuildClassifyVerdict_LowConfidenceExpiresAtRejected(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()

	future := time.Now().UTC().Add(7 * 24 * time.Hour).Format(time.RFC3339)
	result := &llm.LLMResponse{
		Category:   "promotion",
		ExpiresAt:  future,
		Confidence: 0.4, // below minExpiryConfidence (0.7)
		Reason:     "low confidence",
	}

	v := buildClassifyVerdict(result, current, byCategory, "Folders/AI-Triage/Expired")
	if v == nil {
		t.Fatal("expected classification verdict, got nil")
	}
	if v.Expired {
		t.Error("low-confidence deadline must not produce Expired verdict")
	}
	if v.ExpiresAt != nil {
		t.Error("low-confidence deadline must be dropped, ExpiresAt should be nil")
	}
	if v.Rule.DestinationFolder != "Folders/AI-Triage/Promotions" {
		t.Errorf("destination: got %q, want Promotions", v.Rule.DestinationFolder)
	}
}

// TestBuildClassifyVerdict_NoCategoryMatchReturnsNil verifies that when the
// LLM returns category="none" or an unmapped category, the verdict is nil
// so other rules can have a chance.
func TestBuildClassifyVerdict_NoCategoryMatchReturnsNil(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()

	for _, cat := range []string{"", "none", "unknown-cat"} {
		t.Run("category="+cat, func(t *testing.T) {
			result := &llm.LLMResponse{
				Category:   cat,
				ExpiresAt:  "",
				Confidence: 0.9,
			}
			v := buildClassifyVerdict(result, current, byCategory, "Folders/AI-Triage/Expired")
			if v != nil {
				t.Errorf("expected nil verdict for category=%q, got %+v", cat, v)
			}
		})
	}
}

// TestBuildClassifyVerdict_PastDeadlineFallsBackWhenNoExpiredFolder verifies
// graceful behavior when the canonical Expired folder is missing from
// configuration: the verdict still routes to the iterated rule's
// destination (best effort, with a logged warning at scanner.go:canonicalExpiredFolder()).
func TestBuildClassifyVerdict_PastDeadlineFallsBackWhenNoExpiredFolder(t *testing.T) {
	current, byCategory := fakeLLMClassifyRules()

	past := time.Now().UTC().Add(-24 * time.Hour).Format(time.RFC3339)
	result := &llm.LLMResponse{
		Category:   "promotion",
		ExpiresAt:  past,
		Confidence: 0.9,
	}

	v := buildClassifyVerdict(result, current, byCategory, "") // no canonical folder
	if v == nil {
		t.Fatal("expected verdict")
	}
	if !v.Expired {
		t.Error("expected Expired=true")
	}
	// Falls back to current rule's destination.
	if !strings.HasPrefix(v.Rule.DestinationFolder, "Folders/AI-Triage/") {
		t.Errorf("expected fallback to iterated rule's destination, got %q", v.Rule.DestinationFolder)
	}
}
