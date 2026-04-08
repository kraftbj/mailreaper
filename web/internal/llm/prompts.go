package llm

import (
	"fmt"
	"strings"
	"time"
)

// MessageData holds the email metadata and content used to build LLM prompts.
type MessageData struct {
	Sender       string
	Subject      string
	SentDate     string
	BodySnippet  string
	CustomPrompt string
	Category     string
}

// TrainingRef is a user-confirmed example used for few-shot prompting.
type TrainingRef struct {
	Sender    string
	Subject   string
	SentDate  string
	ExpiresAt string
}

// BuildAnalysisPrompt returns a (systemPrompt, userContent) pair for determining
// whether an email has expired. It mirrors the JS buildAnalysisPrompt logic.
func BuildAnalysisPrompt(msg MessageData, examples []TrainingRef) (systemPrompt, userContent string) {
	now := time.Now().UTC()
	currentTime := now.Format(time.RFC3339)
	currentDateReadable := now.Format("Monday, January 2, 2006")

	var sentDateReadable string
	if t, err := time.Parse(time.RFC3339, msg.SentDate); err == nil {
		sentDateReadable = t.Format("Monday, January 2, 2006")
	} else {
		sentDateReadable = msg.SentDate
	}

	// Body section
	bodySection := "\n(Body content not provided — analyze based on metadata only)"
	if msg.BodySnippet != "" {
		bodySection = fmt.Sprintf("\nContent snippet:\n---\n%s\n---", msg.BodySnippet)
	}

	// Custom prompt section
	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = fmt.Sprintf("\n\nAdditional user-provided guidelines (these take priority):\n%s", msg.CustomPrompt)
	}

	// Examples section — up to 5 most recent
	examplesSection := ""
	if len(examples) > 0 {
		recent := examples
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
		var lines []string
		for i, ex := range recent {
			line := fmt.Sprintf("%d. From: %s | Subject: %s | Sent: %s", i+1, ex.Sender, ex.Subject, ex.SentDate)
			if ex.ExpiresAt != "" {
				line += fmt.Sprintf(" | Expired at: %s", ex.ExpiresAt)
			}
			lines = append(lines, line)
		}
		examplesSection = fmt.Sprintf(
			"\n\nThe user has confirmed these emails were time-sensitive and expired:\n%s\n\nUse these as reference — similar emails should be treated as time-sensitive.",
			strings.Join(lines, "\n"),
		)
	}

	systemPrompt = fmt.Sprintf(`Decide if this email has expired. Follow the steps below.

TODAY is %s (%s).
%s
STEP 1: Does the email contain a deadline, event date, or expiration?
Look for: dates, times, "ends tonight", "ends at midnight", "today only", "last chance", "expires", "final hours", appointment times, event times, check-in times, verification codes.

STEP 2: What is the expiration date/time?
- If "ends tonight", "ends at midnight", "today only", "last chance", "final hours" → midnight on the SEND DATE (%s).
- If a specific date/time is mentioned → use that date/time.
- If "appointment" or "event" with a date → the event date/time.
- If verification code, OTP, magic link → 1 hour after send date.
- If shipping "out for delivery", "arriving today" → 24 hours after send date.
- If transit alert, delay, disruption → 3 hours after send date.

STEP 3: Is the expiration date BEFORE today (%s)?
If yes → isTimeSensitive: true, and set expiresAt.

NOT time-sensitive (always set isTimeSensitive: false):
- Newsletters, digests, informational content.
- Personal correspondence.
- Receipts, order confirmations, billing statements.

Respond ONLY with JSON:
{
  "isTimeSensitive": true/false,
  "expiresAt": "ISO-8601 datetime or null",
  "reason": "brief explanation",
  "confidence": 0.0 to 1.0
}

When in doubt, prefer false negatives.%s`,
		currentDateReadable, currentTime,
		examplesSection,
		sentDateReadable,
		currentDateReadable,
		customSection,
	)

	sender := msg.Sender
	if sender == "" {
		sender = "unknown"
	}
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}

	userContent = fmt.Sprintf(`Email:
- From: %s
- Subject: %s
- Sent: %s (%s)
%s`,
		sender,
		subject,
		sentDateReadable,
		msg.SentDate,
		bodySection,
	)

	return systemPrompt, userContent
}

// categoryDescriptions maps known category keys to human-readable descriptions
// used in the classification prompt.
var categoryDescriptions = map[string]string{
	"receipt": "a purchase receipt, payment confirmation, order confirmation, paid invoice, billing statement, or financial transaction record",
}

// BuildClassificationPrompt returns a (systemPrompt, userContent) pair for
// determining whether an email matches a given category (e.g. receipt).
func BuildClassificationPrompt(msg MessageData, examples []TrainingRef) (systemPrompt, userContent string) {
	desc, ok := categoryDescriptions[msg.Category]
	if !ok {
		desc = msg.Category
	}

	// Custom prompt section
	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = fmt.Sprintf("\n\nAdditional user-provided guidelines (these take priority):\n%s", msg.CustomPrompt)
	}

	// Examples section — up to 5 most recent
	examplesSection := ""
	if len(examples) > 0 {
		recent := examples
		if len(recent) > 5 {
			recent = recent[len(recent)-5:]
		}
		var lines []string
		for i, ex := range recent {
			lines = append(lines, fmt.Sprintf("%d. From: %s | Subject: %s", i+1, ex.Sender, ex.Subject))
		}
		examplesSection = fmt.Sprintf(
			"\n\nThe user has confirmed these emails are %ss:\n%s\n\nUse these as reference when classifying the email below.",
			msg.Category,
			strings.Join(lines, "\n"),
		)
	}

	systemPrompt = fmt.Sprintf(`You are an email classifier. Determine if this email is %s.%s

Respond ONLY with a JSON object:
{
  "matches": true/false,
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0
}

Guidelines:
- Purchase receipts, order confirmations, payment confirmations → matches
- Monthly/annual billing statements, subscription renewals → matches
- Paid invoices, donation receipts, tax documents → matches
- Invoices requesting payment (unpaid, due, amount owed) → does NOT match (user needs to act on these)
- Invoices where payment status is unclear → does NOT match (assume unpaid)
- Shipping/delivery notifications → does NOT match (tracked separately)
- Marketing emails from stores → does NOT match
- Account alerts, password resets → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.%s`,
		desc,
		examplesSection,
		customSection,
	)

	sender := msg.Sender
	if sender == "" {
		sender = "unknown"
	}
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}

	bodySection := "\n(Body content not provided — analyze based on metadata only)"
	if msg.BodySnippet != "" {
		bodySection = fmt.Sprintf("\nContent snippet:\n---\n%s\n---", msg.BodySnippet)
	}

	userContent = fmt.Sprintf(`Email metadata:
- From: %s
- Subject: %s
- Sent: %s
%s`,
		sender,
		subject,
		msg.SentDate,
		bodySection,
	)

	return systemPrompt, userContent
}
