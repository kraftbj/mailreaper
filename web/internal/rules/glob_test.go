package rules

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		name    string
		str     string
		pattern string
		want    bool
	}{
		// Exact matches
		{name: "exact match", str: "hello", pattern: "hello", want: true},
		{name: "exact no match", str: "hello", pattern: "world", want: false},
		{name: "empty string empty pattern", str: "", pattern: "", want: true},
		{name: "empty string non-empty pattern", str: "", pattern: "hello", want: false},
		{name: "non-empty string empty pattern", str: "hello", pattern: "", want: false},

		// Star wildcard
		{name: "star prefix", str: "hello world", pattern: "*world", want: true},
		{name: "star suffix", str: "hello world", pattern: "hello*", want: true},
		{name: "star middle", str: "hello beautiful world", pattern: "hello*world", want: true},
		{name: "star alone matches all", str: "anything", pattern: "*", want: true},
		{name: "star alone matches empty", str: "", pattern: "*", want: true},
		{name: "star no match", str: "hello world", pattern: "foo*bar", want: false},
		{name: "double star", str: "abc", pattern: "**", want: true},

		// Question mark wildcard
		{name: "question matches single char", str: "hello", pattern: "hell?", want: true},
		{name: "question no match empty", str: "hell", pattern: "hell?", want: false},
		{name: "question no match multiple", str: "hellXY", pattern: "hell?", want: false},
		{name: "multiple questions", str: "hello", pattern: "h?ll?", want: true},

		// Case insensitivity
		{name: "case insensitive exact", str: "HELLO", pattern: "hello", want: true},
		{name: "case insensitive pattern", str: "hello", pattern: "HELLO", want: true},
		{name: "case insensitive glob", str: "Hello World", pattern: "*world", want: true},

		// Email-style patterns (common MailReaper use cases)
		{name: "email domain star", str: "user@example.com", pattern: "*@example.com", want: true},
		{name: "email domain star no match", str: "user@other.com", pattern: "*@example.com", want: false},
		{name: "email subdomain star", str: "noreply@mail.example.com", pattern: "*@mail.example.com", want: true},
		// A full author string with display name does match a pattern with trailing star
		// because the trailing * absorbs the ">". Use ExtractEmail first in MatchesRule.
		{name: "sender display name with trailing star", str: "john doe <john@example.com>", pattern: "*@example.com*", want: true},
		// But a bare email pattern (no trailing star) does NOT match the full author string
		{name: "sender display name no trailing star", str: "john doe <john@example.com>", pattern: "*@example.com", want: false},

		// Special regex chars that must be escaped
		{name: "dot in pattern is literal", str: "user@example.com", pattern: "user@example.com", want: true},
		{name: "dot in pattern not wildcard", str: "user@exampleXcom", pattern: "user@example.com", want: false},
		{name: "plus in pattern literal", str: "a+b", pattern: "a+b", want: true},
		{name: "caret in pattern literal", str: "a^b", pattern: "a^b", want: true},
		{name: "dollar in pattern literal", str: "a$b", pattern: "a$b", want: true},
		{name: "parens in pattern literal", str: "(test)", pattern: "(test)", want: true},
		{name: "brackets in pattern literal", str: "[test]", pattern: "[test]", want: true},
		{name: "pipe in pattern literal", str: "a|b", pattern: "a|b", want: true},

		// Subject-style patterns
		{name: "subject star prefix and suffix", str: "your verification code", pattern: "*verification code*", want: true},
		{name: "subject no match", str: "hello world", pattern: "*verification code*", want: false},
		{name: "OTP subject", str: "Your OTP is 123456", pattern: "*OTP*", want: true},

		// Anchoring: pattern must match entire string
		{name: "partial match fails", str: "hello world", pattern: "hello", want: false},
		{name: "partial suffix fails", str: "hello world", pattern: "world", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GlobMatch(tt.str, tt.pattern)
			if got != tt.want {
				t.Errorf("GlobMatch(%q, %q) = %v, want %v", tt.str, tt.pattern, got, tt.want)
			}
		})
	}
}

func TestExtractEmail(t *testing.T) {
	tests := []struct {
		name   string
		author string
		want   string
	}{
		{name: "display name with angle brackets", author: "John Doe <john@example.com>", want: "john@example.com"},
		{name: "bare email", author: "john@example.com", want: "john@example.com"},
		{name: "empty string", author: "", want: ""},
		{name: "angle brackets no display name", author: "<john@example.com>", want: "john@example.com"},
		{name: "complex display name", author: "Some Company Alerts <alerts@company.co.uk>", want: "alerts@company.co.uk"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ExtractEmail(tt.author)
			if got != tt.want {
				t.Errorf("ExtractEmail(%q) = %q, want %q", tt.author, got, tt.want)
			}
		})
	}
}
