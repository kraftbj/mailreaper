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

		for _, msg := range msgs {
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

			verdict, err := s.evaluateMessage(ctx, client, &msg, enabledRules)
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

				if err := s.db.LogActivity(db.ActivityEntry{
					Type:            "expired",
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

// evaluateMessage runs a message against all enabled rules (sorted by priority)
// and returns the first matching verdict, or nil if no rule matches.
func (s *Scanner) evaluateMessage(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, enabledRules []db.Rule) (*rules.RuleVerdict, error) {
	ruleMsg := &rules.Message{
		Author:    msg.Sender,
		Subject:   msg.Subject,
		Date:      msg.Date,
		Folder:    msg.Folder,
		MessageID: msg.MessageID,
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
			v, err := s.evaluateLLM(ctx, client, msg, rule, false)
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
			v, err := s.evaluateLLM(ctx, client, msg, rule, true)
			if err != nil {
				log.Printf("scanner: llm-classify eval for %q rule %q: %v", msg.MessageID, rule.Name, err)
				continue
			}
			if v != nil {
				return v, nil
			}
		}
	}

	return nil, nil
}

// evaluateLLM handles both "llm" and "llm-classify" rule types using the cache.
// classify=true means llm-classify; classify=false means llm (expiry).
func (s *Scanner) evaluateLLM(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, rule db.Rule, classify bool) (*rules.RuleVerdict, error) {
	// Check cache first.
	cached, err := s.db.GetCachedVerdict(msg.MessageID)
	if err != nil {
		return nil, fmt.Errorf("get cached verdict: %w", err)
	}

	var result *llm.LLMResponse
	if cached != nil {
		// Reconstruct an LLMResponse from the cache.
		result = &llm.LLMResponse{
			IsTimeSensitive: cached.IsTimeSensitive,
			ExpiresAt:       cached.ExpiresAt,
			Reason:          cached.Reason,
			Confidence:      cached.Confidence,
			Matches:         cached.Classified,
		}
	} else {
		// Cache miss — fetch body (truncated to 2000 chars), build prompt, call LLM.
		body, fetchErr := client.FetchBody(msg.Folder, msg.UID)
		if fetchErr != nil {
			log.Printf("scanner: llm fetch body uid=%d: %v", msg.UID, fetchErr)
			body = ""
		}
		if len(body) > 2000 {
			body = body[:2000]
		}

		var trainingExamples []db.TrainingExample
		category := rule.ExpirationConfig.Category
		if category == "" {
			category = "expiry"
		}
		trainingExamples, err = s.db.GetTrainingExamples(category)
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

		var systemPrompt, userContent string
		if classify {
			systemPrompt, userContent = llm.BuildClassificationPrompt(msgData, refs)
		} else {
			systemPrompt, userContent = llm.BuildAnalysisPrompt(msgData, refs)
		}

		result, err = llm.CallLLM(ctx, &s.cfg.LLM, systemPrompt, userContent)
		if err != nil {
			// Cache error result and return.
			_ = s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{Error: err.Error()})
			return nil, fmt.Errorf("call llm: %w", err)
		}

		// Cache successful result.
		cv := db.CachedVerdict{
			IsTimeSensitive: result.IsTimeSensitive,
			ExpiresAt:       result.ExpiresAt,
			Reason:          result.Reason,
			Confidence:      result.Confidence,
			Classified:      result.Matches,
		}
		if classify {
			cv.Classified = result.Matches
		} else {
			cv.Expired = result.IsTimeSensitive
		}
		if err := s.db.SetCachedVerdict(msg.MessageID, cv); err != nil {
			log.Printf("scanner: cache verdict for %q: %v", msg.MessageID, err)
		}
	}

	if classify {
		if result.Matches {
			return &rules.RuleVerdict{
				Classified: true,
				Rule:       rule,
				Reason:     result.Reason,
				Confidence: result.Confidence,
			}, nil
		}
		return nil, nil
	}

	// llm (expiry) — check if expired.
	if !result.IsTimeSensitive || result.ExpiresAt == "" {
		return nil, nil
	}

	var expiresAt time.Time
	var parseErr error
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04:05Z", "2006-01-02"} {
		expiresAt, parseErr = time.Parse(layout, result.ExpiresAt)
		if parseErr == nil {
			break
		}
	}
	if parseErr != nil {
		return nil, nil
	}

	if time.Now().After(expiresAt) {
		t := expiresAt
		return &rules.RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &t,
			Reason:     result.Reason,
			Confidence: result.Confidence,
		}, nil
	}

	return nil, nil
}
