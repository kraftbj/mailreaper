package llm

import (
	"strings"
	"testing"
)

func TestBuildAnalysisPrompt_NoExamples(t *testing.T) {
	msg := MessageData{
		Sender:      "deals@shop.com",
		Subject:     "Flash Sale Ends Tonight!",
		SentDate:    "2024-01-15T10:00:00Z",
		BodySnippet: "50% off everything, ends at midnight.",
	}

	sys, user := BuildAnalysisPrompt(msg, nil)

	checks := []string{
		"Decide if this email has expired",
		"STEP 1",
		"STEP 2",
		"STEP 3",
		"isTimeSensitive",
		"expiresAt",
		"confidence",
		"When in doubt, prefer false negatives",
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

	if !strings.Contains(sys, "user has confirmed these emails were time-sensitive") {
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

	// userContent has sender/subject
	if !strings.Contains(user, "noreply@airline.com") {
		t.Error("userContent missing sender")
	}
	// No body snippet → fallback message
	if !strings.Contains(user, "Body content not provided") {
		t.Error("userContent missing body fallback")
	}
}

func TestBuildAnalysisPrompt_ExamplesSlicedToFive(t *testing.T) {
	msg := MessageData{Sender: "x@x.com", Subject: "hi", SentDate: "2024-06-01T00:00:00Z"}
	var examples []TrainingRef
	for i := 0; i < 8; i++ {
		examples = append(examples, TrainingRef{
			Sender:  "s@s.com",
			Subject: "example",
			SentDate: "2024-01-01",
		})
	}

	sys, _ := BuildAnalysisPrompt(msg, examples)

	// Should reference at most 5 numbered examples
	if strings.Contains(sys, "6.") {
		t.Error("systemPrompt included more than 5 examples")
	}
}

func TestBuildAnalysisPrompt_CustomPrompt(t *testing.T) {
	msg := MessageData{
		Sender:       "x@x.com",
		Subject:      "hi",
		SentDate:     "2024-06-01T00:00:00Z",
		CustomPrompt: "Always mark as expired if from this domain.",
	}

	sys, _ := BuildAnalysisPrompt(msg, nil)

	if !strings.Contains(sys, "Additional user-provided guidelines") {
		t.Error("systemPrompt missing custom prompt section")
	}
	if !strings.Contains(sys, "Always mark as expired if from this domain.") {
		t.Error("systemPrompt missing custom prompt content")
	}
}

func TestBuildClassificationPrompt_NoExamples(t *testing.T) {
	msg := MessageData{
		Sender:   "receipts@amazon.com",
		Subject:  "Your order has been placed",
		SentDate: "2024-04-10T12:00:00Z",
		Category: "receipt",
	}

	sys, user := BuildClassificationPrompt(msg, nil)

	checks := []string{
		"email classifier",
		"matches",
		"confidence",
		"Prefer false negatives",
	}
	for _, c := range checks {
		if !strings.Contains(sys, c) {
			t.Errorf("systemPrompt missing %q", c)
		}
	}
	if !strings.Contains(sys, "receipt") {
		t.Error("systemPrompt missing category description")
	}

	if !strings.Contains(user, "receipts@amazon.com") {
		t.Error("userContent missing sender")
	}
	if !strings.Contains(user, "Your order has been placed") {
		t.Error("userContent missing subject")
	}
}

func TestBuildClassificationPrompt_WithExamples(t *testing.T) {
	msg := MessageData{
		Sender:   "billing@service.com",
		Subject:  "Invoice #1234",
		SentDate: "2024-05-01T00:00:00Z",
		Category: "receipt",
	}
	examples := []TrainingRef{
		{Sender: "shop@store.com", Subject: "Order confirmed"},
		{Sender: "pay@pay.com", Subject: "Payment receipt"},
	}

	sys, _ := BuildClassificationPrompt(msg, examples)

	if !strings.Contains(sys, "user has confirmed these emails are receipts") {
		t.Error("systemPrompt missing classification examples header")
	}
	if !strings.Contains(sys, "shop@store.com") {
		t.Error("systemPrompt missing example sender")
	}
}

func TestBuildClassificationPrompt_UnknownCategory(t *testing.T) {
	msg := MessageData{
		Sender:   "x@x.com",
		Subject:  "hi",
		SentDate: "2024-01-01T00:00:00Z",
		Category: "newsletter",
	}

	sys, _ := BuildClassificationPrompt(msg, nil)

	// Unknown category falls back to the raw category string in the description
	if !strings.Contains(sys, "newsletter") {
		t.Error("systemPrompt missing unknown category fallback")
	}
}
