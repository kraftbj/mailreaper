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

// categoryGuidelines provides category-specific classification rules for the LLM.
var categoryGuidelines = map[string]string{
	"receipt": `- Purchase receipts, order confirmations, payment confirmations → matches
- Monthly/annual billing statements, subscription renewals → matches
- Paid invoices, donation receipts, tax documents → matches
- Recurring payment/deposit notifications → matches
- Zelle/Venmo/PayPal payment received confirmations → matches
- Invoices requesting payment (unpaid, due, amount owed) → does NOT match (user needs to act on these)
- Invoices where payment status is unclear → does NOT match (assume unpaid)
- Shipping/delivery notifications → does NOT match (tracked separately)
- Marketing emails from stores → does NOT match
- Account alerts, password resets → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.`,

	"newsletter": `- Regular newsletters, digests, editorial emails → matches
- Substack, Buttondown, Mailchimp-style publications → matches
- Blog post notifications, content roundups → matches
- Organization newsletters (political parties, churches, alumni groups) → matches
- Marketing disguised as a newsletter (mostly promoting sales/products) → does NOT match (that's a promotion)
- One-off announcements or event invitations → does NOT match
- Transactional/system notifications → does NOT match
- Personal correspondence → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.`,

	"notification": `- Automated activity digests (Basecamp, GitHub, social media) → matches
- Shipping/delivery tracking updates → matches
- Low balance/credit warnings → matches
- Pharmacy pickup notifications → matches
- App status updates, build notifications → matches
- Dependabot/security alert summaries → matches
- "Your mail was delivered" / USPS Informed Delivery → matches
- Service status changes, subscription renewals that are informational → matches
- Travel status alerts (flight status, delays) → matches
- Messages requiring action (verify email, approve request, unpaid invoice) → does NOT match
- Marketing/promotional emails → does NOT match
- Newsletters → does NOT match
- Personal correspondence → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.`,

	"hobbies": `- Tabletop RPG content, miniature painting, board game updates → matches
- Kickstarter/crowdfunding updates for games or hobby projects → matches
- Patreon posts from game designers, hobby creators → matches
- Game store newsletters, new product releases → matches
- PDF/STL download links from hobby creators → matches
- Video game marketing or AAA game promos → does NOT match (that's a promotion)
- General tech/software newsletters → does NOT match
- Non-hobby Kickstarter campaigns → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.`,

	"promotion": `- Sales, discount offers, coupon codes → matches
- "We miss you" re-engagement emails → matches
- Product launch announcements → matches
- Points/rewards marketing ("turn your points into...") → matches
- Survey invitations from commercial services → matches
- Weekly deal roundups from stores → matches
- Newsletters that are primarily informational → does NOT match
- Purchase receipts/confirmations → does NOT match
- Account/service notifications → does NOT match
- Personal correspondence → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.`,
}

// MultiClassifyResponse holds the parsed result from a multi-category classification call.
type MultiClassifyResponse struct {
	Category   string  `json:"category"`
	Reason     string  `json:"reason"`
	Confidence float64 `json:"confidence"`
	Expired    bool    `json:"expired"`
	ExpiresAt  string  `json:"expiresAt"`
}

// BuildMultiClassificationPrompt builds a single prompt that asks the LLM to
// classify an email into one of several categories (or "none"). This replaces
// N separate classify calls with a single one.
func BuildMultiClassificationPrompt(msg MessageData, categories []string, examples []TrainingRef) (systemPrompt, userContent string) {
	now := time.Now().UTC()
	currentDate := now.Format("Monday, January 2, 2006")
	currentTime := now.Format(time.RFC3339)

	var sentDateReadable string
	if t, err := time.Parse(time.RFC3339, msg.SentDate); err == nil {
		sentDateReadable = t.Format("Monday, January 2, 2006")
	} else {
		sentDateReadable = msg.SentDate
	}

	var catLines []string
	for _, cat := range categories {
		desc, ok := categoryDescriptions[cat]
		if !ok {
			desc = cat
		}
		guidelines := categoryGuidelines[cat]
		catLines = append(catLines, fmt.Sprintf("- \"%s\": %s", cat, desc))
		if guidelines != "" {
			catLines = append(catLines, fmt.Sprintf("  Rules for \"%s\":\n  %s",
				cat, strings.ReplaceAll(guidelines, "\n", "\n  ")))
		}
	}

	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = fmt.Sprintf("\n\nAdditional user-provided guidelines (these take priority):\n%s", msg.CustomPrompt)
	}

	examplesSection := ""
	if len(examples) > 0 {
		recent := examples
		if len(recent) > 10 {
			recent = recent[len(recent)-10:]
		}
		var lines []string
		for i, ex := range recent {
			lines = append(lines, fmt.Sprintf("%d. From: %s | Subject: %s", i+1, ex.Sender, ex.Subject))
		}
		examplesSection = fmt.Sprintf(
			"\n\nThe user has confirmed these emails belong to various categories:\n%s",
			strings.Join(lines, "\n"),
		)
	}

	systemPrompt = fmt.Sprintf(`You are an email classifier. Do TWO things:

1. CLASSIFY this email into exactly ONE of the following categories, or "none":

Categories:
%s

2. CHECK if this email has EXPIRED. TODAY is %s (%s). The email was sent on %s.
Look for: event dates, RSVP deadlines, meeting times, "ends tonight", "today only", "last chance", sale end dates, appointment times. If ANY date/time in the email is BEFORE today, it is expired.
%s
Respond ONLY with a JSON object:
{
  "category": "category_name or none",
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0,
  "expired": true/false,
  "expiresAt": "ISO-8601 datetime or null"
}

Important:
- Pick the single BEST category. Do not force a match.
- If the email requires immediate user action (e.g. unpaid invoice, verify account, approve request), return category "none".
- Personal correspondence is always category "none".
- A message can be BOTH classified (e.g. "notification") AND expired. Set both fields independently.
- Prefer "none" over a low-confidence category match.
- For expiry: if a date like "Sun 4/12" or "March 30" appears and is before today (%s), set expired=true.%s%s`,
		strings.Join(catLines, "\n"),
		currentDate, currentTime, sentDateReadable,
		examplesSection,
		currentDate,
		customSection,
		"",
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

// categoryDescriptions maps known category keys to human-readable descriptions
// used in the classification prompt.
var categoryDescriptions = map[string]string{
	"receipt":      "a purchase receipt, payment confirmation, order confirmation, paid invoice, billing statement, or financial transaction record",
	"newsletter":   "a newsletter, digest, editorial, blog post, content roundup, or regularly-scheduled informational email from a publication, organization, or individual writer",
	"notification": "an automated notification, status update, activity alert, system message, or transactional update that informs but requires no immediate action (e.g. shipping updates, app activity digests, service alerts, low-balance warnings, pharmacy notifications, social media activity summaries)",
	"promotion":    "a marketing email, promotional offer, advertisement, sales pitch, discount offer, product announcement, or re-engagement email designed to get the recipient to buy something or re-engage with a service",
	"hobbies":      "an email related to tabletop gaming, RPGs, miniatures, board games, Kickstarter/crowdfunding campaigns for games, painting, hobby crafting, or hobby-focused community content (e.g. Patreon posts from game creators, game store newsletters, RPG product releases)",
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

	guidelines := categoryGuidelines[msg.Category]
	if guidelines == "" {
		guidelines = "- Prefer false negatives over false positives — when in doubt, say false."
	}

	systemPrompt = fmt.Sprintf(`You are an email classifier. Determine if this email is %s.%s

Respond ONLY with a JSON object:
{
  "matches": true/false,
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0
}

Guidelines:
%s%s`,
		desc,
		examplesSection,
		guidelines,
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
