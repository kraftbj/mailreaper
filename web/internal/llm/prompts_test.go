package llm

import (
	"strings"
	"testing"
)

// bannedPhrases must NOT appear in either prompt — they were the source of the
// over-aggressive expiry classification bug fixed in the web-rewrite redesign.
var bannedPhrases = []string{
	"any date/time",
	"BEFORE today, it is expired",
	"isTimeSensitive",
	"\"expired\"",
	"set expired=true",
}

// negativeExamples must appear in both prompts so the LLM knows these are NOT
// hard deadlines even when they contain past dates.
var negativeExamples = []string{
	"Newsletter",
	"Transaction",
	"LinkedIn",
	"Daily digests",
	"Memories from",
	"Credit-report",
}

func TestBuildAnalysisPrompt_NoExamples(t *testing.T) {
	msg := MessageData{
		Sender:      "deals@shop.com",
		Subject:     "Flash Sale Ends Tonight!",
		SentDate:    "2024-01-15T10:00:00Z",
		BodySnippet: "50% off everything, ends at midnight.",
	}

	sys, user := BuildAnalysisPrompt(msg, nil)

	checks := []string{
		"Extract any hard deadline",
		"Hard deadlines",
		"NOT hard deadlines",
		"expiresAt",
		"confidence",
		"Prefer null over a low-confidence extraction",
	}
	for _, c := range checks {
		if !strings.Contains(sys, c) {
			t.Errorf("systemPrompt missing %q", c)
		}
	}

	if !strings.Contains(user, "deals@shop.com") {
		t.Error("userContent missing sender")
	}
	if !strings.Contains(user, "Flash Sale Ends Tonight!") {
		t.Error("userContent missing subject")
	}
	if !strings.Contains(user, "50% off everything") {
		t.Error("userContent missing body snippet")
	}
}

func TestBuildAnalysisPrompt_BannedPhrases(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildAnalysisPrompt(msg, nil)

	for _, banned := range bannedPhrases {
		if strings.Contains(sys, banned) {
			t.Errorf("systemPrompt contains banned phrase %q (regression guard)", banned)
		}
	}
}

func TestBuildAnalysisPrompt_NegativeExamples(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildAnalysisPrompt(msg, nil)

	for _, ex := range negativeExamples {
		if !strings.Contains(sys, ex) {
			t.Errorf("systemPrompt missing negative example %q", ex)
		}
	}
}

func TestBuildAnalysisPrompt_WithExamples(t *testing.T) {
	msg := MessageData{
		Sender:   "noreply@airline.com",
		Subject:  "Your flight is tomorrow",
		SentDate: "2024-03-01T08:00:00Z",
	}
	examples := []TrainingRef{
		{Sender: "a@b.com", Subject: "Deal expires", SentDate: "2024-01-01", ExpiresAt: "2024-01-02T00:00:00Z"},
		{Sender: "c@d.com", Subject: "Event today", SentDate: "2024-02-01", ExpiresAt: ""},
	}

	sys, user := BuildAnalysisPrompt(msg, examples)

	if !strings.Contains(sys, "user has confirmed these emails contained hard deadlines") {
		t.Error("systemPrompt missing examples section header")
	}
	if !strings.Contains(sys, "a@b.com") {
		t.Error("systemPrompt missing example sender")
	}
	if !strings.Contains(sys, "Deal expires") {
		t.Error("systemPrompt missing example subject")
	}
	if !strings.Contains(sys, "2024-01-02T00:00:00Z") {
		t.Error("systemPrompt missing expiresAt from example")
	}

	if !strings.Contains(user, "noreply@airline.com") {
		t.Error("userContent missing sender")
	}
	if !strings.Contains(user, "Body content not provided") {
		t.Error("userContent missing body fallback")
	}
}

func TestBuildAnalysisPrompt_ExamplesSlicedToFive(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-06-01T00:00:00Z"}
	var examples []TrainingRef
	for i := 0; i < 8; i++ {
		examples = append(examples, TrainingRef{
			Sender:   "s@s.com",
			Subject:  "example",
			SentDate: "2024-01-01",
		})
	}

	sys, _ := BuildAnalysisPrompt(msg, examples)

	if strings.Contains(sys, "6.") {
		t.Error("systemPrompt included more than 5 examples")
	}
}

func TestBuildAnalysisPrompt_CustomPrompt(t *testing.T) {
	msg := MessageData{
		Sender:       "x@x.com",
		Subject:      "hi",
		SentDate:     "2024-06-01T00:00:00Z",
		CustomPrompt: "Treat anything from this domain as time-sensitive.",
	}

	sys, _ := BuildAnalysisPrompt(msg, nil)

	if !strings.Contains(sys, "Additional user-provided guidelines") {
		t.Error("systemPrompt missing custom prompt section")
	}
	if !strings.Contains(sys, "Treat anything from this domain as time-sensitive.") {
		t.Error("systemPrompt missing custom prompt content")
	}
}

func TestBuildMultiClassificationPrompt_BannedPhrases(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildMultiClassificationPrompt(msg, []string{"promotion"}, nil)

	for _, banned := range bannedPhrases {
		if strings.Contains(sys, banned) {
			t.Errorf("systemPrompt contains banned phrase %q (regression guard)", banned)
		}
	}
}

func TestBuildMultiClassificationPrompt_NegativeExamples(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildMultiClassificationPrompt(msg, []string{"promotion", "notification"}, nil)

	for _, ex := range negativeExamples {
		if !strings.Contains(sys, ex) {
			t.Errorf("systemPrompt missing negative example %q", ex)
		}
	}
}

func TestBuildMultiClassificationPrompt_SchemaShape(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildMultiClassificationPrompt(msg, []string{"promotion"}, nil)

	required := []string{
		"\"category\":",
		"\"expiresAt\":",
		"\"confidence\":",
		"\"reason\":",
	}
	for _, r := range required {
		if !strings.Contains(sys, r) {
			t.Errorf("schema example missing %q", r)
		}
	}
	if strings.Contains(sys, "\"expired\":") {
		t.Error("schema example still contains the dropped \"expired\" boolean field")
	}
}

func TestBuildMultiClassificationPrompt_SingleCategory(t *testing.T) {
	// Per decision 1A: scanner always uses this prompt even when only one
	// llm-classify rule is enabled. Verify it renders cleanly.
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	sys, _ := BuildMultiClassificationPrompt(msg, []string{"promotion"}, nil)

	if !strings.Contains(sys, "\"promotion\":") {
		t.Error("single-category prompt missing the category line")
	}
	if !strings.Contains(sys, "Hard deadlines") {
		t.Error("single-category prompt missing deadline-extraction section")
	}
}

func TestBuildMultiClassificationPrompt_WithExamples(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-01-01T00:00:00Z"}
	examples := []TrainingRef{
		{Sender: "shop@store.com", Subject: "Order confirmed"},
		{Sender: "pay@pay.com", Subject: "Payment receipt"},
	}

	sys, _ := BuildMultiClassificationPrompt(msg, []string{"receipt"}, examples)

	if !strings.Contains(sys, "user has confirmed these emails belong to various categories") {
		t.Error("systemPrompt missing examples header")
	}
	if !strings.Contains(sys, "shop@store.com") {
		t.Error("systemPrompt missing example sender")
	}
}
