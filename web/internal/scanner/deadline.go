package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/llm"
	"github.com/kraftbj/mailreaper/internal/rules"
)

/*
deadlineCheckedFolders returns the set of folder names belonging to
categories flagged perishable (categories.check_deadlines), keyed by
lowercased folder name.

Resolved once per scan cycle rather than per message: it is a database read,
and the answer cannot change mid-cycle in any way worth reacting to. On error
it returns nil, which every caller reads as "no folder is perishable" -- the
safe direction, since the consequence is mail staying put rather than mail
being expired on a bad lookup.
*/
func (s *Scanner) deadlineCheckedFolders() map[string]bool {
	cats, err := s.db.GetCategories()
	if err != nil {
		log.Printf("scanner: get categories for deadline-check lookup: %v", err)
		return nil
	}
	checked := map[string]bool{}
	for _, c := range cats {
		if c.CheckDeadlines && c.FolderName != "" {
			checked[strings.ToLower(c.FolderName)] = true
		}
	}
	return checked
}

/*
analyzeDeadline runs the deadline-extraction prompt for a message, reading
through the "analysis" LLM cache so a message costs at most one call ever
rather than one per scan cycle.

Extracted from evaluateLLM so the "llm" rule type and the after-routing check
(applyDeadlineCheck) share one code path. The cache key, the 2000-character
body cap, and the training-example lookup must not drift between the two
callers: a divergence there would show up as duplicate spend or as one caller
silently reading the other's cache miss.

The cache key itself -- s.llmCacheKey("analysis") -- does not incorporate
customPrompt or category, so evaluateLLM (which passes a rule's Prompt and
Category) and applyDeadlineCheck (which passes "", "") read and write the
same cache row for a given message. That is safe only because at most one of
them ever runs per message: evaluateMessage is first-match-wins, and verdict
dedup stops a message from being re-evaluated once it has a cached verdict.
It stops being safe the moment an "llm" rule is given a priority below a
classify rule's, since that would let both callers reach the same message --
whichever ran first would have its prompt-specific answer served back to the
other.
*/
func (s *Scanner) analyzeDeadline(ctx context.Context, client MailClient, msg *imappkg.FetchedMessage, customPrompt, category string) (*llm.LLMResponse, error) {
	cacheKey := s.llmCacheKey("analysis")

	cached, err := s.db.GetCachedVerdict(msg.MessageID, cacheKey)
	if err != nil {
		return nil, fmt.Errorf("get cached verdict: %w", err)
	}
	if cached != nil {
		/* Surface the cached failure rather than silently reconstructing an
		empty response: within the 10-minute error TTL a network call is
		skipped, but the outage must stay visible in the logs the way a
		fresh failure would be. */
		if cached.Error != "" {
			return nil, fmt.Errorf("cached llm error: %s", cached.Error)
		}
		return &llm.LLMResponse{
			ExpiresAt:  cached.ExpiresAt,
			Reason:     cached.Reason,
			Confidence: cached.Confidence,
			Matches:    cached.Classified,
		}, nil
	}

	body, fetchErr := client.FetchBody(msg.Folder, msg.UID)
	if fetchErr != nil {
		log.Printf("scanner: analysis fetch body uid=%d: %v", msg.UID, fetchErr)
		body = ""
	}
	if len(body) > 2000 {
		body = body[:2000]
	}

	trainingCategory := category
	if trainingCategory == "" {
		trainingCategory = "expiry"
	}
	trainingExamples, err := s.db.GetTrainingExamples(trainingCategory)
	if err != nil {
		log.Printf("scanner: get training examples: %v", err)
	}
	refs := make([]llm.TrainingRef, 0, len(trainingExamples))
	for _, ex := range trainingExamples {
		refs = append(refs, llm.TrainingRef{Sender: ex.Sender, Subject: ex.Subject})
	}

	systemPrompt, userContent := llm.BuildAnalysisPrompt(llm.MessageData{
		Sender:       msg.Sender,
		Subject:      msg.Subject,
		SentDate:     msg.Date.Format(time.RFC3339),
		BodySnippet:  body,
		CustomPrompt: customPrompt,
		Category:     category,
	}, refs)

	result, err := llm.CallLLM(ctx, &s.cfg.LLM, systemPrompt, userContent)
	if err != nil {
		/* Cache the failure too: without this, a message that fails to
		analyze gets retried on every scan instead of backing off for the
		error TTL. */
		if cacheErr := s.db.SetCachedVerdict(msg.MessageID, cacheKey, db.CachedVerdict{Error: err.Error()}); cacheErr != nil {
			log.Printf("scanner: cache analysis error for %q: %v", msg.MessageID, cacheErr)
		}
		return nil, fmt.Errorf("call llm: %w", err)
	}

	if err := s.db.SetCachedVerdict(msg.MessageID, cacheKey, db.CachedVerdict{
		ExpiresAt:  result.ExpiresAt,
		Reason:     result.Reason,
		Confidence: result.Confidence,
	}); err != nil {
		log.Printf("scanner: cache analysis verdict for %q: %v", msg.MessageID, err)
	}
	return result, nil
}

/*
applyDeadlineCheck asks the model for a content-implied deadline on a message
that a routing rule placed in a perishable folder without one, and returns
the destination the message should actually go to, the deadline to persist,
and the reason to record.

This exists because evaluateMessage is first-match-wins: a classify rule at
priority 27 short-circuits the llm and llm-classify rules at 100 and 200+,
which are the only rules that ask about deadlines. Every one of the 4,359
classify-routed verdicts in the live database carried no deadline as a
result, so "sale ends Friday" sat in Promotions forever.

It runs after routing rather than before it so that llm-classify messages,
which already receive a deadline from their combined classify+extract call,
pay nothing extra.

If the verdict already carries a deadline (an llm-classify call, say),
nothing runs and dest is returned unchanged alongside that existing
deadline -- never nil, since a nil return here would let the caller
overwrite a deadline the classify call already found. Every other path
that does not apply -- a non-perishable or unmapped destination, provider
"none", an LLM error, no deadline found, an extraction rejected by
extractValidExpiresAt's confidence and plausibility guards -- returns dest
unchanged and a nil deadline, so the caller behaves exactly as it does
today.
*/
func (s *Scanner) applyDeadlineCheck(
	ctx context.Context,
	client MailClient,
	msg *imappkg.FetchedMessage,
	verdict *rules.RuleVerdict,
	dest string,
	checked map[string]bool,
	expiredFolder string,
) (string, *time.Time, string) {
	if verdict == nil {
		return dest, nil, ""
	}
	if verdict.ExpiresAt != nil {
		return dest, verdict.ExpiresAt, verdict.Reason
	}
	if !checked[strings.ToLower(dest)] {
		return dest, nil, verdict.Reason
	}
	if strings.ToLower(s.cfg.LLM.Provider) == "none" {
		return dest, nil, verdict.Reason
	}

	result, err := s.analyzeDeadline(ctx, client, msg, "", "")
	if err != nil {
		log.Printf("scanner: deadline check for %q: %v", msg.MessageID, err)
		return dest, nil, verdict.Reason
	}

	expiresAt, past, valid := extractValidExpiresAt(result.ExpiresAt, result.Confidence, s.loc, msg.Date)
	if !valid {
		return dest, nil, verdict.Reason
	}

	if past {
		routed := dest
		if expiredFolder != "" {
			routed = expiredFolder
		}
		log.Printf("scanner: %q has a past deadline (%s); routing to %q instead of %q",
			msg.Subject, expiresAt.Format(time.RFC3339), routed, dest)
		return routed, expiresAt, result.Reason
	}

	log.Printf("scanner: %q keeps folder %q with a deferred deadline of %s",
		msg.Subject, dest, expiresAt.Format(time.RFC3339))
	return dest, expiresAt, result.Reason
}
