package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/llm"
	"github.com/kraftbj/mailreaper/internal/rules"
)

// MailClient is the interface the Scanner uses to interact with an IMAP server.
// The real implementation is *imap.Client; tests use a mock.
type MailClient interface {
	FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]imappkg.FetchedMessage, error)
	MoveMessage(folder string, uid uint32, dest string) error
	EnsureFolder(name string) error
	GetMessageIDsInFolder(folder string) ([]string, error)
	GetMessagesInFolder(folder string) ([]imappkg.FetchedMessage, error)
	FetchBody(folder string, uid uint32) (string, error)
	Close() error
}

// Scanner orchestrates mail scanning across folders and accounts.
type Scanner struct {
	db           *db.DB
	cfg          *config.Config
	LookbackDays int // 0 = scan all messages
}

// New creates a new Scanner.
func New(database *db.DB, cfg *config.Config) *Scanner {
	return &Scanner{db: database, cfg: cfg, LookbackDays: 7}
}

// ScanAccount scans the given folders for an account, evaluating each message
// against enabled rules and executing or queuing verdicts.
func (s *Scanner) ScanAccount(ctx context.Context, client MailClient, accountID string, folders []string) error {
	enabledRules, err := s.db.GetEnabledRules()
	if err != nil {
		return fmt.Errorf("scanner: get enabled rules: %w", err)
	}

	expiredFolder := s.canonicalExpiredFolder()

	var since time.Time
	if s.LookbackDays > 0 {
		since = time.Now().Add(-time.Duration(s.LookbackDays) * 24 * time.Hour)
	} else {
		since = time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	minAge := time.Duration(s.cfg.Scan.MinMessageAgeMin) * time.Minute
	maxMessages := s.cfg.Scan.MaxMessagesPerScan
	if s.LookbackDays == 0 {
		maxMessages = 0 // no limit during catchup
	}

	totalProcessed := 0

	for _, folder := range folders {
		msgs, err := client.FetchNewMessages(folder, since, minAge, maxMessages)
		if err != nil {
			log.Printf("scanner: fetch messages from %q: %v", folder, err)
			continue
		}
		log.Printf("scanner: %q returned %d messages (since=%s, minAge=%s, limit=%d)",
			folder, len(msgs), since.Format("2006-01-02"), minAge, maxMessages)

		for i, msg := range msgs {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
			}

			if msg.MessageID == "" {
				continue
			}

			// Skip if verdict already exists.
			existing, err := s.db.GetVerdictByMessageID(msg.MessageID)
			if err != nil {
				log.Printf("scanner: check existing verdict for %q: %v", msg.MessageID, err)
				continue
			}
			if existing != nil {
				continue
			}

			log.Printf("scanner: [%d/%d] evaluating %q from %s", i+1, len(msgs), msg.Subject, msg.Sender)
			verdict, err := s.evaluateMessage(ctx, client, &msg, enabledRules, expiredFolder)
			if err != nil {
				log.Printf("scanner: evaluate message %q: %v", msg.MessageID, err)
				continue
			}
			if verdict == nil {
				continue
			}
			log.Printf("scanner: verdict for %q: rule=%q confidence=%.2f expired=%v classified=%v",
				msg.Subject, verdict.Rule.Name, verdict.Confidence, verdict.Expired, verdict.Classified)

			totalProcessed++

			ruleID := verdict.Rule.ID
			now := time.Now().UTC()

			v := db.Verdict{
				AccountID:       accountID,
				MessageIDHeader: msg.MessageID,
				Subject:         msg.Subject,
				Sender:          msg.Sender,
				SentAt:          msg.Date,
				RuleID:          &ruleID,
				DestinationFolder: verdict.Rule.DestinationFolder,
				Reason:          verdict.Reason,
				Confidence:      verdict.Confidence,
				EvaluatedAt:     now,
			}

			if verdict.ExpiresAt != nil {
				v.ExpiresAt = verdict.ExpiresAt
			}

			if verdict.Confidence >= 0.7 {
				// Auto-execute: move the message.
				dest := verdict.Rule.DestinationFolder
				if dest == "" {
					dest = "Expired"
				}

				if err := client.EnsureFolder(dest); err != nil {
					log.Printf("scanner: ensure folder %q: %v", dest, err)
				}

				if err := client.MoveMessage(msg.Folder, msg.UID, dest); err != nil {
					log.Printf("scanner: move message %q to %q: %v", msg.MessageID, dest, err)
				}

				actedAt := time.Now().UTC()
				v.Status = "executed"
				v.ActedAt = &actedAt
				v.DestinationFolder = dest

				if err := s.db.SaveVerdict(v); err != nil {
					log.Printf("scanner: save verdict for %q: %v", msg.MessageID, err)
					continue
				}

				activityType := "triaged"
				if verdict.Expired {
					activityType = "expired"
				}

				if err := s.db.LogActivity(db.ActivityEntry{
					Type:            activityType,
					AccountID:       accountID,
					MessageIDHeader: msg.MessageID,
					Subject:         msg.Subject,
					Sender:          msg.Sender,
					RuleName:        verdict.Rule.Name,
					Destination:     dest,
					Reason:          verdict.Reason,
					Confidence:      verdict.Confidence,
				}); err != nil {
					log.Printf("scanner: log activity for %q: %v", msg.MessageID, err)
				}
			} else {
				// Low confidence — queue as pending.
				v.Status = "pending"

				if err := s.db.SaveVerdict(v); err != nil {
					log.Printf("scanner: save pending verdict for %q: %v", msg.MessageID, err)
				}
			}
		}
	}

	if err := s.db.UpdateAccountScan(accountID, totalProcessed); err != nil {
		log.Printf("scanner: update account scan stats for %q: %v", accountID, err)
	}

	return nil
}

// originalSenderHeaders lists headers checked (in priority order) for the real
// sender behind forwarding/alias services like SimpleLogin.
var originalSenderHeaders = []string{
	"x-simplelogin-original-from",
	"x-original-from",
	"x-forwarded-from",
}

// extractOriginalSender returns the original sender from forwarding service
// headers, or empty string if none found.
func extractOriginalSender(headers map[string][]string) string {
	for _, h := range originalSenderHeaders {
		if vals, ok := headers[h]; ok && len(vals) > 0 && vals[0] != "" {
			return vals[0]
		}
	}
	return ""
}

// minExpiryConfidence is the threshold below which an LLM-extracted expiresAt
// is rejected as likely hallucinated. Matches the auto-execute confidence
// threshold for verdicts (scanner.go ScanAccount: confidence >= 0.7).
const minExpiryConfidence = 0.7

// canonicalExpiredFolder returns the IMAP folder name configured as the
// canonical "Expired" destination, or empty string if none is configured.
// Looked up from db.GetCategories() (the "expired" category id).
func (s *Scanner) canonicalExpiredFolder() string {
	cats, err := s.db.GetCategories()
	if err != nil {
		log.Printf("scanner: get categories for expired folder lookup: %v", err)
		return ""
	}
	for _, c := range cats {
		if c.ID == "expired" {
			return c.FolderName
		}
	}
	return ""
}

// evaluateMessage runs a message against all enabled rules (sorted by priority)
// and returns the first matching verdict, or nil if no rule matches.
//
// expiredFolder is the canonical Expired destination resolved at scan-cycle
// start. When an LLM-extracted deadline is in the past, the verdict routes
// here regardless of which rule was iterated — fixes the prior bug where a
// past-deadline notification ended up in the Notifications folder.
func (s *Scanner) evaluateMessage(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, enabledRules []db.Rule, expiredFolder string) (*rules.RuleVerdict, error) {
	ruleMsg := &rules.Message{
		Author:         msg.Sender,
		OriginalSender: extractOriginalSender(msg.Headers),
		Subject:        msg.Subject,
		Date:           msg.Date,
		Folder:         msg.Folder,
		MessageID:      msg.MessageID,
	}

	var bodyCache *string // lazily fetched

	fetchBody := func() string {
		if bodyCache != nil {
			return *bodyCache
		}
		body, err := client.FetchBody(msg.Folder, msg.UID)
		if err != nil {
			log.Printf("scanner: fetch body for uid=%d: %v", msg.UID, err)
			empty := ""
			bodyCache = &empty
			return ""
		}
		bodyCache = &body
		return body
	}

	// Collect llm-classify rules that match this message so we can batch them
	// into a single LLM call instead of N separate calls.
	var classifyRules []db.Rule
	classifyRulesByCategory := map[string]db.Rule{}
	for _, rule := range enabledRules {
		if rule.ExpirationConfig.Type == "llm-classify" && rules.MatchesRule(ruleMsg, rule) {
			cat := rule.ExpirationConfig.Category
			if cat != "" {
				classifyRules = append(classifyRules, rule)
				classifyRulesByCategory[cat] = rule
			}
		}
	}

	// multiClassifyDone tracks whether we've already run the batch classify.
	var multiClassifyDone bool
	var multiClassifyResult *llm.LLMResponse

	for _, rule := range enabledRules {
		if !rules.MatchesRule(ruleMsg, rule) {
			continue
		}

		switch rule.ExpirationConfig.Type {
		case "ttl":
			if v := rules.EvaluateTTL(ruleMsg, rule); v != nil {
				return v, nil
			}

		case "header":
			if v := rules.EvaluateHeader(msg.Headers, rule); v != nil {
				return v, nil
			}

		case "content-regex":
			body := fetchBody()
			if v := rules.EvaluateContentRegex(body, rule); v != nil {
				return v, nil
			}

		case "classify":
			return rules.EvaluateClassify(rule), nil

		case "llm":
			if strings.ToLower(s.cfg.LLM.Provider) == "none" {
				continue
			}
			v, err := s.evaluateLLM(ctx, client, msg, rule, expiredFolder)
			if err != nil {
				log.Printf("scanner: llm eval for %q rule %q: %v", msg.MessageID, rule.Name, err)
				continue
			}
			if v != nil {
				return v, nil
			}

		case "llm-classify":
			if strings.ToLower(s.cfg.LLM.Provider) == "none" {
				continue
			}

			// On the first matching llm-classify rule, run ONE multi-classify
			// call covering every llm-classify category configured. Batches
			// even the single-category case (per design decision 1A — keeps
			// the deadline-extraction logic in a single prompt path).
			if !multiClassifyDone && len(classifyRules) >= 1 {
				multiClassifyDone = true
				result, err := s.evaluateMultiClassify(ctx, client, msg, classifyRules)
				if err != nil {
					log.Printf("scanner: multi-classify for %q: %v", msg.MessageID, err)
				} else {
					multiClassifyResult = result
				}
			}

			if multiClassifyResult == nil {
				continue
			}

			v := buildClassifyVerdict(multiClassifyResult, rule, classifyRulesByCategory, expiredFolder)
			if v != nil {
				return v, nil
			}
			continue // batch already answered for this message
		}
	}

	return nil, nil
}

// zonedExpiryLayouts carry an explicit UTC offset, so they are parsed as-is.
var zonedExpiryLayouts = []string{time.RFC3339}

// zonelessExpiryLayouts omit any zone. The prompt asks for RFC3339, but the
// model does not always comply and intermittently returns a bare wall-clock
// timestamp such as "2026-06-26T09:00:00". Those previously matched no layout
// and were discarded silently, throwing away a correctly-extracted deadline.
var zonelessExpiryLayouts = []string{
	"2006-01-02T15:04:05",
	"2006-01-02T15:04",
	"2006-01-02 15:04:05",
	"2006-01-02 15:04",
}

// dateOnlyExpiryLayout is handled separately from the datetime layouts: a
// bare date means the deadline lapses at the END of that day. prompts.go
// asks the model for 23:59:59 semantics, and reading a bare date as
// midnight expires the message a full day early.
const dateOnlyExpiryLayout = "2006-01-02"

// parseExpiresAt parses an expiry date string and returns the time and whether
// it's already in the past. Returns nil if the string is empty or unparseable.
//
// Zoneless input is interpreted in the server's local zone rather than UTC.
// For a personal mail server that is the user's own zone, and so the closest
// reading of the wall-clock time the sender wrote. Reading it as UTC instead
// would place the deadline earlier than intended for anyone behind UTC, and
// expiring a message early is the failure mode this codebase is built to
// avoid. Date-only values are handled separately below and advanced to the
// end of the local day; see dateOnlyExpiryLayout.
func parseExpiresAt(s string) (*time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, false
	}

	for _, layout := range zonedExpiryLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return &t, time.Now().After(t)
		}
	}

	for _, layout := range zonelessExpiryLayouts {
		if t, err := time.ParseInLocation(layout, s, time.Local); err == nil {
			return &t, time.Now().After(t)
		}
	}

	if t, err := time.ParseInLocation(dateOnlyExpiryLayout, s, time.Local); err == nil {
		t = t.Add(24*time.Hour - time.Second)
		return &t, time.Now().After(t)
	}

	return nil, false
}

// extractValidExpiresAt parses the LLM-returned ExpiresAt and applies the
// confidence guard (decision 3A — reject low-confidence extractions as
// likely hallucinated). Returns the parsed time, whether it is in the past,
// and a "valid" flag that is false when the input was empty, unparseable,
// or rejected by the confidence threshold.
func extractValidExpiresAt(expiresAt string, confidence float64) (parsed *time.Time, past bool, valid bool) {
	if expiresAt == "" {
		return nil, false, false
	}
	if confidence < minExpiryConfidence {
		log.Printf("scanner: dropping LLM expiresAt=%q with confidence=%.2f below %.2f threshold (likely hallucination)",
			expiresAt, confidence, minExpiryConfidence)
		return nil, false, false
	}
	t, isPast := parseExpiresAt(expiresAt)
	if t == nil {
		return nil, false, false
	}
	return t, isPast, true
}

// buildClassifyVerdict turns a multi-classify LLM response into a rule
// verdict, applying the routing rules from the expiry redesign:
//
//   - Past extracted deadline → expired verdict, routed to canonical Expired
//     folder (NOT the iterated rule's destination — fixes the prior bug).
//   - Future extracted deadline + matched category → classified verdict
//     for that category's rule, with ExpiresAt persisted so
//     SweepDeferredExpiries can pick it up later.
//   - No extracted deadline + matched category → plain classified verdict.
//   - No category match → returns nil so other rules can have a chance.
//
// The currentRule is used only for logging/reason context; routing always
// uses either the matched category's rule or the canonical Expired folder.
func buildClassifyVerdict(result *llm.LLMResponse, currentRule db.Rule, byCategory map[string]db.Rule, expiredFolder string) *rules.RuleVerdict {
	expiresAt, past, hasDeadline := extractValidExpiresAt(result.ExpiresAt, result.Confidence)

	if hasDeadline && past {
		expiredRule := currentRule
		if expiredFolder != "" {
			expiredRule.DestinationFolder = expiredFolder
		}
		return &rules.RuleVerdict{
			Expired:    true,
			ExpiresAt:  expiresAt,
			Rule:       expiredRule,
			Reason:     result.Reason,
			Confidence: result.Confidence,
		}
	}

	matchedCat := strings.ToLower(result.Category)
	if matchedCat == "" || matchedCat == "none" || result.Confidence <= 0 {
		return nil
	}
	matchedRule, ok := byCategory[matchedCat]
	if !ok {
		return nil
	}

	v := &rules.RuleVerdict{
		Classified: true,
		Rule:       matchedRule,
		Reason:     result.Reason,
		Confidence: result.Confidence,
	}
	if hasDeadline && !past {
		v.ExpiresAt = expiresAt
	}
	return v
}

// evaluateMultiClassify runs a single LLM call that classifies a message into
// one of several categories. This replaces N separate llm-classify calls with one.
func (s *Scanner) evaluateMultiClassify(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, classifyRules []db.Rule) (*llm.LLMResponse, error) {
	body, fetchErr := client.FetchBody(msg.Folder, msg.UID)
	if fetchErr != nil {
		log.Printf("scanner: multi-classify fetch body uid=%d: %v", msg.UID, fetchErr)
		body = ""
	}
	if len(body) > 2000 {
		body = body[:2000]
	}

	var categories []string
	var customPrompts []string
	seenPrompt := map[string]bool{}
	for _, r := range classifyRules {
		categories = append(categories, r.ExpirationConfig.Category)
		// Gather distinct non-empty custom prompts across all classify rules.
		// Per-rule prompts are merged into a shared "Additional user-provided
		// guidelines" section since multi-classify makes one LLM call for all
		// categories together. The single-rule case (one entry in classifyRules)
		// preserves that rule's CustomPrompt verbatim.
		if p := r.ExpirationConfig.Prompt; p != "" && !seenPrompt[p] {
			customPrompts = append(customPrompts, p)
			seenPrompt[p] = true
		}
	}
	customPrompt := strings.Join(customPrompts, "\n\n")

	// Gather training examples across all categories.
	var allExamples []db.TrainingExample
	for _, cat := range categories {
		examples, err := s.db.GetTrainingExamples(cat)
		if err != nil {
			log.Printf("scanner: get training examples for %q: %v", cat, err)
			continue
		}
		allExamples = append(allExamples, examples...)
	}

	refs := make([]llm.TrainingRef, 0, len(allExamples))
	for _, ex := range allExamples {
		refs = append(refs, llm.TrainingRef{Sender: ex.Sender, Subject: ex.Subject})
	}

	msgData := llm.MessageData{
		Sender:       msg.Sender,
		Subject:      msg.Subject,
		SentDate:     msg.Date.Format(time.RFC3339),
		BodySnippet:  body,
		CustomPrompt: customPrompt,
	}

	systemPrompt, userContent := llm.BuildMultiClassificationPrompt(msgData, categories, refs)
	result, err := llm.CallLLM(ctx, &s.cfg.LLM, systemPrompt, userContent)
	if err != nil {
		return nil, fmt.Errorf("call llm: %w", err)
	}

	return result, nil
}

// evaluateLLM handles "llm" rule types (deadline-only extraction). It uses
// the cache to avoid re-asking about previously-seen messages, then applies
// the same past/future + confidence-guard logic as the multi-classify path.
//
// Returns:
//   - past extracted deadline → expired verdict routed to canonical Expired folder
//   - future extracted deadline → nil (the `llm` rule type has no category to
//     park the message under; the next scan with a then-past deadline will
//     re-evaluate from cache and produce the verdict)
//   - no deadline → nil
func (s *Scanner) evaluateLLM(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, rule db.Rule, expiredFolder string) (*rules.RuleVerdict, error) {
	cached, err := s.db.GetCachedVerdict(msg.MessageID)
	if err != nil {
		return nil, fmt.Errorf("get cached verdict: %w", err)
	}

	var result *llm.LLMResponse
	if cached != nil {
		result = &llm.LLMResponse{
			ExpiresAt:  cached.ExpiresAt,
			Reason:     cached.Reason,
			Confidence: cached.Confidence,
			Matches:    cached.Classified,
		}
	} else {
		body, fetchErr := client.FetchBody(msg.Folder, msg.UID)
		if fetchErr != nil {
			log.Printf("scanner: llm fetch body uid=%d: %v", msg.UID, fetchErr)
			body = ""
		}
		if len(body) > 2000 {
			body = body[:2000]
		}

		category := rule.ExpirationConfig.Category
		if category == "" {
			category = "expiry"
		}
		trainingExamples, err := s.db.GetTrainingExamples(category)
		if err != nil {
			log.Printf("scanner: get training examples: %v", err)
		}

		refs := make([]llm.TrainingRef, 0, len(trainingExamples))
		for _, ex := range trainingExamples {
			refs = append(refs, llm.TrainingRef{
				Sender:  ex.Sender,
				Subject: ex.Subject,
			})
		}

		msgData := llm.MessageData{
			Sender:       msg.Sender,
			Subject:      msg.Subject,
			SentDate:     msg.Date.Format(time.RFC3339),
			BodySnippet:  body,
			CustomPrompt: rule.ExpirationConfig.Prompt,
			Category:     rule.ExpirationConfig.Category,
		}

		systemPrompt, userContent := llm.BuildAnalysisPrompt(msgData, refs)

		result, err = llm.CallLLM(ctx, &s.cfg.LLM, systemPrompt, userContent)
		if err != nil {
			_ = s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{Error: err.Error()})
			return nil, fmt.Errorf("call llm: %w", err)
		}

		cv := db.CachedVerdict{
			ExpiresAt:  result.ExpiresAt,
			Reason:     result.Reason,
			Confidence: result.Confidence,
		}
		if err := s.db.SetCachedVerdict(msg.MessageID, cv); err != nil {
			log.Printf("scanner: cache verdict for %q: %v", msg.MessageID, err)
		}
	}

	expiresAt, past, hasDeadline := extractValidExpiresAt(result.ExpiresAt, result.Confidence)
	if !hasDeadline || !past {
		return nil, nil
	}

	expiredRule := rule
	if expiredFolder != "" {
		expiredRule.DestinationFolder = expiredFolder
	}
	return &rules.RuleVerdict{
		Expired:    true,
		Rule:       expiredRule,
		ExpiresAt:  expiresAt,
		Reason:     result.Reason,
		Confidence: result.Confidence,
	}, nil
}
