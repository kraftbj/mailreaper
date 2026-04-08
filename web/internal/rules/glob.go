package rules

import (
	"regexp"
	"strings"
)

// GlobMatch checks if str matches a glob pattern (* = any chars, ? = single char).
// Case-insensitive, anchored (must match entire string).
// Mirrors the globMatch function from the Thunderbird extension's rules/engine.js.
func GlobMatch(str, pattern string) bool {
	// Work case-insensitively by lowercasing both sides.
	s := strings.ToLower(str)
	p := strings.ToLower(pattern)

	// Build a regex from the glob pattern:
	//   1. Escape all regex special chars except * and ?
	//   2. Replace * with .*
	//   3. Replace ? with .
	//   4. Anchor with ^ and $
	var b strings.Builder
	b.WriteString("^")
	for _, ch := range p {
		switch ch {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '.', '+', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\':
			b.WriteRune('\\')
			b.WriteRune(ch)
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(s)
}

// ExtractEmail extracts the email address from a "Display Name <email>" string.
// If no angle-bracket notation is found, the original string is returned as-is.
// Mirrors extractEmail from the Thunderbird extension's rules/engine.js.
func ExtractEmail(author string) string {
	start := strings.Index(author, "<")
	end := strings.Index(author, ">")
	if start != -1 && end != -1 && end > start {
		return author[start+1 : end]
	}
	return author
}
