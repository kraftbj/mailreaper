package scanner

import (
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
// This pins the actual shape the branch produces -- an alias-scoped glob,
// not merely "ends with @simplelogin.co" -- so a regression that widened it
// to the provider-wide "*@simplelogin.co" would still be caught.
func TestDistillSimpleLoginAliasUnchanged(t *testing.T) {
	_, senderPattern, _ := buildAutoRule("news_at_example_com_abc123@simplelogin.co")
	if senderPattern == "*@simplelogin.co" {
		t.Fatalf("distilled a provider-wide rule %q from a single SimpleLogin alias", senderPattern)
	}
	want := "*news*@simplelogin.co"
	if senderPattern != want {
		t.Errorf("senderPattern = %q, want %q", senderPattern, want)
	}
}

// TestBuildAutoRuleDoesNotCollideDotVsHyphen pins the sanitizeID fix: two
// distinct local-part naming conventions (dot-separated vs hyphen-separated)
// must not sanitize down to the same rule ID, or the second sender's own
// evidence silently never produces a rule (DistillRules sees the first
// sender's rule as "already exists" and skips).
func TestBuildAutoRuleDoesNotCollideDotVsHyphen(t *testing.T) {
	idA, _, _ := buildAutoRule("john.doe@example.com")
	idB, _, _ := buildAutoRule("john-doe@example.com")
	if idA == idB {
		t.Fatalf("buildAutoRule collided: john.doe@example.com and john-doe@example.com both produced rule ID %q", idA)
	}
}
