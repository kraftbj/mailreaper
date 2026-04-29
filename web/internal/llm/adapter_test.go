package llm

import (
	"testing"
)

func TestParseJSONResponse_Standard(t *testing.T) {
	input := `{"expiresAt": "2024-01-15T00:00:00Z", "reason": "sale ended", "confidence": 0.9}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt != "2024-01-15T00:00:00Z" {
		t.Errorf("unexpected ExpiresAt: %q", resp.ExpiresAt)
	}
	if resp.Reason != "sale ended" {
		t.Errorf("unexpected Reason: %q", resp.Reason)
	}
	if resp.Confidence != 0.9 {
		t.Errorf("unexpected Confidence: %v", resp.Confidence)
	}
}

func TestParseJSONResponse_SnakeCaseKeys(t *testing.T) {
	input := `{"expires_at": "2024-03-01T12:00:00Z", "explanation": "event passed", "score": 0.75}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt != "2024-03-01T12:00:00Z" {
		t.Errorf("unexpected ExpiresAt: %q", resp.ExpiresAt)
	}
	if resp.Reason != "event passed" {
		t.Errorf("unexpected Reason from explanation: %q", resp.Reason)
	}
	if resp.Confidence != 0.75 {
		t.Errorf("unexpected Confidence from score: %v", resp.Confidence)
	}
}

func TestParseJSONResponse_NullExpiresAt(t *testing.T) {
	input := `{"expiresAt": null, "reason": "no deadline", "confidence": 0.9}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt != "" {
		t.Errorf("expected empty ExpiresAt for null, got %q", resp.ExpiresAt)
	}
}

func TestParseJSONResponse_LegacyFieldsIgnored(t *testing.T) {
	// Old cached responses or stale prompt outputs may include "expired" or
	// "isTimeSensitive". They must parse without panic and without affecting
	// the new fields.
	input := `{"isTimeSensitive": true, "expired": true, "expiresAt": "2024-01-15T00:00:00Z", "reason": "x", "confidence": 0.9}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt != "2024-01-15T00:00:00Z" {
		t.Errorf("legacy fields broke ExpiresAt parsing: %q", resp.ExpiresAt)
	}
}

func TestParseJSONResponse_MarkdownFences(t *testing.T) {
	input := "```json\n{\"expiresAt\": null, \"reason\": \"newsletter\", \"confidence\": 0.95}\n```"
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Reason != "newsletter" {
		t.Errorf("unexpected Reason: %q", resp.Reason)
	}
}

func TestParseJSONResponse_MarkdownFencesNoLang(t *testing.T) {
	input := "```\n{\"expiresAt\": \"2024-06-01T00:00:00Z\", \"reason\": \"event\", \"confidence\": 0.8}\n```"
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.ExpiresAt != "2024-06-01T00:00:00Z" {
		t.Errorf("unexpected ExpiresAt: %q", resp.ExpiresAt)
	}
}

func TestParseJSONResponse_ClassificationResponse(t *testing.T) {
	input := `{"matches": true, "reason": "order confirmation", "confidence": 0.88}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !resp.Matches {
		t.Error("expected Matches true")
	}
	if resp.Reason != "order confirmation" {
		t.Errorf("unexpected Reason: %q", resp.Reason)
	}
	if resp.Confidence != 0.88 {
		t.Errorf("unexpected Confidence: %v", resp.Confidence)
	}
}

func TestParseJSONResponse_MultiClassify(t *testing.T) {
	input := `{"category": "promotion", "expiresAt": "2024-04-28T23:59:59Z", "reason": "sale ends 4/28", "confidence": 0.85}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Category != "promotion" {
		t.Errorf("unexpected Category: %q", resp.Category)
	}
	if resp.ExpiresAt != "2024-04-28T23:59:59Z" {
		t.Errorf("unexpected ExpiresAt: %q", resp.ExpiresAt)
	}
}

func TestParseJSONResponse_ConfidenceClampHigh(t *testing.T) {
	input := `{"expiresAt": null, "reason": "oops", "confidence": 1.5}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Confidence != 1.0 {
		t.Errorf("expected confidence clamped to 1.0, got %v", resp.Confidence)
	}
}

func TestParseJSONResponse_ConfidenceClampLow(t *testing.T) {
	input := `{"expiresAt": null, "reason": "safe", "confidence": -0.3}`
	resp, err := ParseJSONResponse(input)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Confidence != 0.0 {
		t.Errorf("expected confidence clamped to 0.0, got %v", resp.Confidence)
	}
}

func TestParseJSONResponse_InvalidJSON(t *testing.T) {
	_, err := ParseJSONResponse("not json at all")
	if err == nil {
		t.Error("expected error for invalid JSON")
	}
}
