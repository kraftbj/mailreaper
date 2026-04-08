package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// Message holds the header-level information needed for rule evaluation.
// It mirrors the subset of Thunderbird's MessageHeader used by the JS engine.
type Message struct {
	Author    string
	Subject   string
	Date      time.Time
	Folder    string
	MessageID string
}

// RuleVerdict is the result of evaluating a message against a single rule.
type RuleVerdict struct {
	Expired    bool
	Classified bool
	Rule       db.Rule
	ExpiresAt  *time.Time
	Reason     string
	Confidence float64
}

// MatchesRule checks if a message matches a rule's static criteria (sender,
// subject, folder). A rule with no patterns matches every message.
//
// Sender matching tries both the raw Author string and the extracted email so
// that patterns like "*@example.com" work whether or not a display name is
// present. All comparisons are case-insensitive.
func MatchesRule(msg *Message, rule db.Rule) bool {
	mc := rule.MatchConfig

	// Folder filter
	if len(mc.Folders) > 0 {
		folderLower := strings.ToLower(msg.Folder)
		matched := false
		for _, f := range mc.Folders {
			if strings.ToLower(f) == folderLower {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Sender patterns — try raw author and extracted email
	if len(mc.SenderPatterns) > 0 {
		authorRaw := strings.ToLower(msg.Author)
		emailOnly := strings.ToLower(ExtractEmail(msg.Author))
		matched := false
		for _, pattern := range mc.SenderPatterns {
			p := strings.ToLower(pattern)
			if GlobMatch(authorRaw, p) || (emailOnly != "" && GlobMatch(emailOnly, p)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	// Subject patterns
	if len(mc.SubjectPatterns) > 0 {
		subject := strings.ToLower(msg.Subject)
		matched := false
		for _, pattern := range mc.SubjectPatterns {
			if GlobMatch(subject, strings.ToLower(pattern)) {
				matched = true
				break
			}
		}
		if !matched {
			return false
		}
	}

	return true
}

// EvaluateTTL checks whether the message has exceeded the TTL defined in the
// rule (hours after send date). Returns nil if the message has not yet expired.
func EvaluateTTL(msg *Message, rule db.Rule) *RuleVerdict {
	ttl := time.Duration(rule.ExpirationConfig.Hours) * time.Hour
	expiresAt := msg.Date.Add(ttl)
	if time.Now().After(expiresAt) {
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &expiresAt,
			Reason:     fmt.Sprintf("TTL expired: %dh after send date", rule.ExpirationConfig.Hours),
			Confidence: 1.0,
		}
	}
	return nil
}

// headerDateFormats lists the date formats tried when parsing the Expires
// header, in preference order. Mirrors the JS behavior of using new Date()
// which accepts RFC1123Z, RFC1123, RFC850, and RFC3339.
var headerDateFormats = []string{
	time.RFC1123Z, // "Mon, 02 Jan 2006 15:04:05 -0700"
	time.RFC1123,  // "Mon, 02 Jan 2006 15:04:05 MST"
	time.RFC850,   // "Monday, 02-Jan-06 15:04:05 MST"
	time.RFC3339,  // "2006-01-02T15:04:05Z07:00"
}

// EvaluateHeader checks the Expires header in the provided headers map.
// Returns nil if the header is absent, unparseable, or in the future.
func EvaluateHeader(headers map[string][]string, rule db.Rule) *RuleVerdict {
	values, ok := headers["Expires"]
	if !ok || len(values) == 0 || values[0] == "" {
		return nil
	}
	raw := values[0]

	var expiresAt time.Time
	var parseErr error
	for _, layout := range headerDateFormats {
		expiresAt, parseErr = time.Parse(layout, raw)
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return nil
	}

	if time.Now().After(expiresAt) {
		t := expiresAt
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &t,
			Reason:     fmt.Sprintf("Expires header: %s", raw),
			Confidence: 1.0,
		}
	}
	return nil
}

// contentDateFormats lists formats tried when parsing a date captured by the
// content regex, in preference order.
var contentDateFormats = []string{
	time.RFC3339,                // "2006-01-02T15:04:05Z07:00"
	"2006-01-02T15:04:05Z",     // RFC3339 UTC shorthand
	"2006-01-02",               // date only
	"January 2, 2006",          // US long form
	"Jan 2, 2006",              // US short form
	"01/02/2006",               // US numeric
	"02 Jan 2006",              // EU short
	time.RFC1123Z,
	time.RFC1123,
}

// EvaluateContentRegex matches the body text against the regex in the rule's
// ExpirationConfig.Pattern. The first capture group must yield a parseable
// date. Returns nil if there is no match, the date is unparseable, or the date
// is in the future. Body is truncated to 50,000 chars before matching.
func EvaluateContentRegex(body string, rule db.Rule) *RuleVerdict {
	if body == "" || rule.ExpirationConfig.Pattern == "" {
		return nil
	}

	if len(body) > 50000 {
		body = body[:50000]
	}

	re, err := regexp.Compile(rule.ExpirationConfig.Pattern)
	if err != nil {
		return nil
	}

	match := re.FindStringSubmatch(body)
	if len(match) < 2 || match[1] == "" {
		return nil
	}
	dateStr := match[1]

	var expiresAt time.Time
	var parseErr error
	for _, layout := range contentDateFormats {
		expiresAt, parseErr = time.Parse(layout, dateStr)
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return nil
	}

	if time.Now().After(expiresAt) {
		t := expiresAt
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &t,
			Reason:     fmt.Sprintf("Content regex matched: %s", match[0]),
			Confidence: 0.9,
		}
	}
	return nil
}

// EvaluateClassify returns a classification verdict for rules of type
// "classify". The verdict is always returned (classification is non-expiry).
func EvaluateClassify(rule db.Rule) *RuleVerdict {
	return &RuleVerdict{
		Classified: true,
		Rule:       rule,
		Reason:     rule.Name,
		Confidence: 1.0,
	}
}
