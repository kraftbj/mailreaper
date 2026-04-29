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

// BuildAnalysisPrompt returns a (systemPrompt, userContent) pair for extracting
// any explicit hard deadline from an email. The LLM does NOT decide whether
// the deadline has passed — that comparison is done in Go after the response.
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

	bodySection := "\n(Body content not provided — analyze based on metadata only)"
	if msg.BodySnippet != "" {
		bodySection = fmt.Sprintf("\nContent snippet:\n---\n%s\n---", msg.BodySnippet)
	}

	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = fmt.Sprintf("\n\nAdditional user-provided guidelines (these take priority):\n%s", msg.CustomPrompt)
	}

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
			"\n\nThe user has confirmed these emails contained hard deadlines:\n%s\n\nUse these as reference — similar emails likely contain hard deadlines.",
			strings.Join(lines, "\n"),
		)
	}

	systemPrompt = fmt.Sprintf(`Extract any hard deadline from this email.

TODAY is %s (%s). The email was sent on %s.
%s
A hard deadline is an explicit date/time in the email after which the
message has zero remaining value.

Hard deadlines (extract these as expiresAt):
- Explicit dated sale-ends ("sale ends Friday", "ends 4/28", "valid
  through April 30") — use that specific calendar date at 23:59:59 if no
  time is given. Compute the actual date relative to the SEND DATE.
- Send-date-only phrases ("today only", "ends tonight", "ends at
  midnight", "final hours") — use midnight at the END of the SEND DATE.
- "Last chance" without an explicit date and within a marketing context
  — use midnight at the end of the SEND DATE.
- RSVP-by deadlines for events.
- Specific appointment or event start times.
- OTP / verification code validity windows — use 1 hour after send.
- Transit / flight delay alerts — use 3 hours after send.
- "Out for delivery" / "arriving today" — use 24 hours after send.

NOT hard deadlines (always return expiresAt: null):
- Newsletter or article publication dates.
- Transaction, purchase, receipt, or billing timestamps.
- "Delivered on" / "shipped on" / "your package arrived on" stamps.
- "Added you on LinkedIn on …" or other social-network activity timestamps.
- "Member request" / "approval needed" notifications (still actionable
  regardless of age).
- Daily digests (USPS Informed Delivery, GitHub digests, Basecamp
  digests) — even when older.
- "Memories from N years ago" emails.
- Credit-report or account-status change notifications.
- Software release / changelog announcements (e.g. tz database releases).
- Personal correspondence.

Do NOT decide whether the deadline has already passed — that comparison
happens elsewhere. Only extract the date string.

Respond ONLY with JSON:
{
  "expiresAt": "ISO-8601 datetime or null",
  "reason": "brief explanation",
  "confidence": 0.0 to 1.0
}

Prefer null over a low-confidence extraction.%s`,
		currentDateReadable, currentTime,
		sentDateReadable,
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

// BuildMultiClassificationPrompt builds a single prompt that asks the LLM to
// (1) classify an email into one of several categories (or "none") and
// (2) extract any hard deadline string. The LLM does NOT decide whether the
// deadline has passed — that comparison is done in Go after the response.
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

	systemPrompt = fmt.Sprintf(`You are an email classifier. Do TWO independent tasks:

1. CLASSIFY this email into exactly ONE of the following categories, or "none":

Categories:
%s

2. EXTRACT any hard deadline. TODAY is %s (%s). The email was sent on %s.

A hard deadline is an explicit date/time in the email after which the
message has zero remaining value.

Hard deadlines (extract these as expiresAt):
- Explicit dated sale-ends ("sale ends Friday", "ends 4/28", "valid
  through April 30") — use that specific calendar date at 23:59:59 if no
  time is given. Compute the actual date relative to the SEND DATE.
- Send-date-only phrases ("today only", "ends tonight", "ends at
  midnight", "final hours") — use midnight at the END of the SEND DATE.
- "Last chance" without an explicit date and within a marketing context
  — use midnight at the end of the SEND DATE.
- RSVP-by deadlines for events.
- Specific appointment or event start times.
- OTP / verification code validity windows — use 1 hour after send.
- Transit / flight delay alerts — use 3 hours after send.
- "Out for delivery" / "arriving today" — use 24 hours after send.

NOT hard deadlines (always return expiresAt: null even if dates appear):
- Newsletter or article publication dates.
- Transaction, purchase, receipt, or billing timestamps.
- "Delivered on" / "shipped on" / "your package arrived on" stamps.
- "Added you on LinkedIn on …" or other social-network activity timestamps.
- "Member request" / "approval needed" notifications (still actionable
  regardless of age).
- Daily digests (USPS Informed Delivery, GitHub digests, Basecamp
  digests) — even when older.
- "Memories from N years ago" emails.
- Credit-report or account-status change notifications.
- Software release / changelog announcements (e.g. tz database releases).
- Personal correspondence.

Do NOT decide whether the deadline has already passed — that comparison
happens elsewhere. Only extract the date string.
%s
Respond ONLY with a JSON object:
{
  "category": "category_name or none",
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0,
  "expiresAt": "ISO-8601 datetime or null"
}

Important:
- Pick the single BEST category. Do not force a match.
- If the email requires immediate user action (e.g. unpaid invoice, verify account, approve request), return category "none".
- Personal correspondence is always category "none".
- Category and expiresAt are independent. A "promotion" can have an expiresAt; a "notification" usually does not.
- Prefer "none" over a low-confidence category match.
- Prefer null over a low-confidence expiresAt extraction.%s`,
		strings.Join(catLines, "\n"),
		currentDate, currentTime, sentDateReadable,
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

// categoryDescriptions maps known category keys to human-readable descriptions
// used in the classification prompt.
var categoryDescriptions = map[string]string{
	"receipt":      "a purchase receipt, payment confirmation, order confirmation, paid invoice, billing statement, or financial transaction record",
	"newsletter":   "a newsletter, digest, editorial, blog post, content roundup, or regularly-scheduled informational email from a publication, organization, or individual writer",
	"notification": "an automated notification, status update, activity alert, system message, or transactional update that informs but requires no immediate action (e.g. shipping updates, app activity digests, service alerts, low-balance warnings, pharmacy notifications, social media activity summaries)",
	"promotion":    "a marketing email, promotional offer, advertisement, sales pitch, discount offer, product announcement, or re-engagement email designed to get the recipient to buy something or re-engage with a service",
	"hobbies":      "an email related to tabletop gaming, RPGs, miniatures, board games, Kickstarter/crowdfunding campaigns for games, painting, hobby crafting, or hobby-focused community content (e.g. Patreon posts from game creators, game store newsletters, RPG product releases)",
}
