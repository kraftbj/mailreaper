package scanner

import (
	"strings"
	"testing"
)

// TestDistillDoesNotWidenToConsumerDomain pins the evidence-to-rule
// relationship: three messages from one address justify a rule about that
// address, not about every user of their mail provider.
func TestDistillDoesNotWidenToConsumerDomain(t *testing.T) {
	// Signature is buildAutoRule(email string) (ruleID, senderPattern, name string)
	// -- note the return order.
	_, senderPattern, _ := buildAutoRule("friend@gmail.com")
	if senderPattern == "*@gmail.com" {
		t.Fatalf("distilled a provider-wide rule %q from a single sender", senderPattern)
	}
	if senderPattern != "friend@gmail.com" {
		t.Errorf("senderPattern = %q, want the exact sender address", senderPattern)
	}
}

// The SimpleLogin branch is per-sender by construction and must keep working.
func TestDistillSimpleLoginAliasUnchanged(t *testing.T) {
	_, senderPattern, _ := buildAutoRule("news_at_example_com_abc123@simplelogin.co")
	if !strings.HasSuffix(senderPattern, "@simplelogin.co") {
		t.Errorf("senderPattern = %q, want a simplelogin-scoped pattern", senderPattern)
	}
}
