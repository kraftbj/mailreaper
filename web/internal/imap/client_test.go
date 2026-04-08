package imap

import (
	"testing"
)

func TestSanitizeFolderName(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{
			name:  "no whitespace",
			input: "INBOX",
			want:  "INBOX",
		},
		{
			name:  "leading spaces",
			input: "  INBOX",
			want:  "INBOX",
		},
		{
			name:  "trailing spaces",
			input: "INBOX  ",
			want:  "INBOX",
		},
		{
			name:  "leading and trailing spaces",
			input: "  INBOX  ",
			want:  "INBOX",
		},
		{
			name:  "internal spaces preserved",
			input: "  My Folder  ",
			want:  "My Folder",
		},
		{
			name:  "tabs trimmed",
			input: "\tInbox\t",
			want:  "Inbox",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "whitespace only",
			input: "   ",
			want:  "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := SanitizeFolderName(tc.input)
			if got != tc.want {
				t.Errorf("SanitizeFolderName(%q) = %q; want %q", tc.input, got, tc.want)
			}
		})
	}
}
