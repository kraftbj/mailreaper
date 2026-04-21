package scanner

import (
	"fmt"
	"log"
	"strings"

	"github.com/kraftbj/mailreaper/internal/db"
	"github.com/kraftbj/mailreaper/internal/rules"
)

const (
	minManualCount   = 3  // require 3+ manual classifications to auto-create a rule
	autoRulePriority = 27 // before user rules (28-29), after static builtins
)

// DistillRules analyzes manual verdict patterns and creates static classify
// rules for senders that have been consistently classified to the same folder.
func (s *Scanner) DistillRules() error {
	patterns, err := s.db.GetManualSenderPatterns(minManualCount)
	if err != nil {
		return fmt.Errorf("distill: get patterns: %w", err)
	}

	if len(patterns) == 0 {
		return nil
	}

	// Load existing rules to check for overlaps.
	existingRules, err := s.db.GetEnabledRules()
	if err != nil {
		return fmt.Errorf("distill: get enabled rules: %w", err)
	}

	// Load declined auto-rules so we don't re-propose them.
	declined, err := s.db.GetDeclinedAutoRuleIDs()
	if err != nil {
		return fmt.Errorf("distill: get declined rules: %w", err)
	}

	created := 0
	for _, p := range patterns {
		ruleID, senderPattern, name := buildAutoRule(p.SenderEmail)

		// Skip if user previously deleted this auto-rule.
		if declined[ruleID] {
			continue
		}

		// Skip if rule already exists.
		existing, err := s.db.GetRule(ruleID)
		if err != nil {
			log.Printf("distill: check existing rule %q: %v", ruleID, err)
			continue
		}
		if existing != nil {
			continue
		}

		// Skip if the sender already matches an existing rule.
		if senderMatchesExistingRule(p.SenderEmail, existingRules) {
			continue
		}

		folderShort := p.DestinationFolder
		if idx := strings.LastIndex(folderShort, "/"); idx >= 0 {
			folderShort = folderShort[idx+1:]
		}

		rule := db.Rule{
			ID:       ruleID,
			Name:     fmt.Sprintf("%s → %s", name, folderShort),
			Enabled:  true,
			Priority: autoRulePriority,
			Builtin:  false,
			MatchConfig: db.MatchConfig{
				SenderPatterns: []string{senderPattern},
			},
			ExpirationConfig: db.ExpirationConfig{
				Type: "classify",
			},
			Action:            "move",
			DestinationFolder: p.DestinationFolder,
			GracePeriodDays:   0,
		}

		if err := s.db.SaveRule(rule); err != nil {
			log.Printf("distill: save rule %q: %v", ruleID, err)
			continue
		}

		log.Printf("distill: created rule %q (%s → %s, %d examples)",
			ruleID, senderPattern, p.DestinationFolder, p.Count)

		if err := s.db.LogActivity(db.ActivityEntry{
			Type:        "rule_created",
			RuleName:    rule.Name,
			Destination: p.DestinationFolder,
			Reason:      fmt.Sprintf("Auto-generated from %d manual classifications", p.Count),
			Confidence:  1.0,
		}); err != nil {
			log.Printf("distill: log activity: %v", err)
		}

		created++
	}

	if created > 0 {
		log.Printf("distill: created %d new rules from manual classification patterns", created)
	}

	return nil
}

// buildAutoRule returns (ruleID, senderPattern, displayName) for a given sender email.
func buildAutoRule(email string) (ruleID, senderPattern, name string) {
	email = strings.ToLower(email)

	// SimpleLogin aliases: extract the original domain from the local part.
	// e.g. "emails_at_emails_cinemark_com_ymnkxguhp@simplelogin.co"
	// → original domain is "cinemark.com" (best effort)
	if strings.HasSuffix(email, "@simplelogin.co") {
		local := strings.TrimSuffix(email, "@simplelogin.co")
		// Remove the random suffix (last _xxx segment)
		if idx := strings.LastIndex(local, "_"); idx > 0 {
			local = local[:idx]
		}
		// Replace _at_ with @ to find the original address structure
		if strings.Contains(local, "_at_") {
			parts := strings.SplitN(local, "_at_", 2)
			if len(parts) == 2 {
				// Convert underscores back to dots for domain
				origDomain := strings.ReplaceAll(parts[1], "_", ".")
				// Use the part before _at_ as a prefix pattern
				origLocal := strings.ReplaceAll(parts[0], "_", ".")
				name = origLocal + "@" + origDomain
				// Match the SimpleLogin alias pattern (partial match on local part)
				senderPattern = fmt.Sprintf("*%s*@simplelogin.co", strings.ReplaceAll(parts[0], "_", "_"))
				ruleID = fmt.Sprintf("auto-sl-%s", sanitizeID(origDomain))
				return
			}
		}
		// Fallback: use the whole local part
		name = local + "@simplelogin.co"
		senderPattern = fmt.Sprintf("*%s*@simplelogin.co", local)
		ruleID = fmt.Sprintf("auto-sl-%s", sanitizeID(local))
		return
	}

	// Regular email: match by domain.
	parts := strings.SplitN(email, "@", 2)
	if len(parts) == 2 {
		domain := parts[1]
		name = domain
		senderPattern = fmt.Sprintf("*@%s", domain)
		ruleID = fmt.Sprintf("auto-%s", sanitizeID(domain))
		return
	}

	// Fallback: exact match.
	name = email
	senderPattern = email
	ruleID = fmt.Sprintf("auto-%s", sanitizeID(email))
	return
}

// sanitizeID converts a string into a safe rule ID component.
func sanitizeID(s string) string {
	s = strings.ToLower(s)
	s = strings.ReplaceAll(s, "@", "-")
	s = strings.ReplaceAll(s, ".", "-")
	s = strings.ReplaceAll(s, " ", "-")
	return s
}

// senderMatchesExistingRule checks if the given sender email would match any
// existing enabled rule's sender patterns.
func senderMatchesExistingRule(email string, existingRules []db.Rule) bool {
	msg := &rules.Message{Author: email}
	for _, r := range existingRules {
		if len(r.MatchConfig.SenderPatterns) == 0 {
			continue // skip catch-all rules
		}
		if rules.MatchesRule(msg, r) {
			return true
		}
	}
	return false
}
