package scanner

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
)

// BackfillStats summarizes a backfill run.
type BackfillStats struct {
	Folders   int // folders scanned
	Inspected int // messages re-evaluated
	Moved     int // messages moved to a different folder
	Kept      int // messages whose new verdict matched their current folder
	Skipped   int // messages skipped (no Message-ID, low confidence, fetch errors)
	Errored   int // moves or evaluations that errored
}

// BackfillFolders re-evaluates every message in the canonical Expired folder
// and every triage category folder against the currently-enabled rules. If
// the new verdict implies a different destination, the message is moved.
//
// Use cases:
//   - Rescue messages wrongly moved to Expired by an earlier rule version.
//   - Pull messages with newly-extractable deadlines (e.g. "sale ends 4/28")
//     out of Promotions / Notifications / Hobbies and into Expired now that
//     their deadline has passed and the prompt extracts it correctly.
//
// Messages whose new verdict has confidence < 0.7 are left in their current
// folder (matches the auto-execute threshold used by the live scan).
//
// rescueFolder is where messages with no matching rule go (typically INBOX).
func (s *Scanner) BackfillFolders(ctx context.Context, client MailClient, accountID, rescueFolder string) (*BackfillStats, error) {
	if rescueFolder == "" {
		rescueFolder = "INBOX"
	}

	enabledRules, err := s.db.GetEnabledRules()
	if err != nil {
		return nil, fmt.Errorf("backfill: get rules: %w", err)
	}

	expiredFolder := s.canonicalExpiredFolder()

	cats, err := s.db.GetCategories()
	if err != nil {
		return nil, fmt.Errorf("backfill: get categories: %w", err)
	}

	folderSet := map[string]bool{}
	if expiredFolder != "" {
		folderSet[expiredFolder] = true
	}
	for _, c := range cats {
		if c.FolderName != "" {
			folderSet[c.FolderName] = true
		}
	}
	if len(folderSet) == 0 {
		log.Printf("backfill: no category folders configured, nothing to do")
		return &BackfillStats{}, nil
	}

	stats := &BackfillStats{}
	for folder := range folderSet {
		select {
		case <-ctx.Done():
			return stats, ctx.Err()
		default:
		}

		stats.Folders++
		log.Printf("backfill: scanning %q", folder)

		msgs, mErr := client.GetMessagesInFolder(folder)
		if mErr != nil {
			log.Printf("backfill: list %q: %v", folder, mErr)
			stats.Errored++
			continue
		}
		log.Printf("backfill: %q has %d messages", folder, len(msgs))

		for i, msg := range msgs {
			select {
			case <-ctx.Done():
				return stats, ctx.Err()
			default:
			}

			if msg.MessageID == "" {
				stats.Skipped++
				continue
			}

			stats.Inspected++
			log.Printf("backfill: [%d/%d] %q from %s",
				i+1, len(msgs), msg.Subject, msg.Sender)

			// Backfill re-asks the LLM rather than trusting a prior answer,
			// so the cached verdict must go. The verdict row itself is left
			// alone: the dedup check lives in ScanAccount, not
			// evaluateMessage, so deleting it here bought nothing and left a
			// hole that DetectManualClassifications misread as a user filing
			// whenever the evaluation below failed.
			if err := s.db.RemoveCachedVerdict(msg.MessageID); err != nil {
				log.Printf("backfill: remove cached verdict for %q: %v", msg.MessageID, err)
			}

			verdict, err := s.evaluateMessage(ctx, client, &msg, enabledRules, expiredFolder)
			if err != nil {
				log.Printf("backfill: evaluate %q: %v", msg.MessageID, err)
				stats.Errored++
				continue
			}

			dest := backfillDestination(verdict, expiredFolder, rescueFolder)
			// A nil verdict means "no rule matched or the LLM call failed" --
			// that is an absence of information, not confidence that the
			// message belongs in the rescue folder. Scoring it 0 lets the
			// existing <0.7 gate below leave the message where it is.
			confidence := 0.0
			if verdict != nil {
				confidence = verdict.Confidence
			}

			if strings.EqualFold(dest, folder) {
				stats.Kept++
				saveBackfillVerdict(s.db, accountID, &msg, folder, verdict, "executed")
				continue
			}

			if confidence < 0.7 {
				log.Printf("backfill: low-confidence (%.2f) for %q; leaving in %q",
					confidence, msg.Subject, folder)
				stats.Skipped++
				saveBackfillVerdict(s.db, accountID, &msg, folder, verdict, "pending")
				continue
			}

			if err := client.EnsureFolder(dest); err != nil {
				log.Printf("backfill: ensure folder %q: %v", dest, err)
				stats.Errored++
				continue
			}
			if err := client.MoveMessage(folder, msg.UID, dest); err != nil {
				log.Printf("backfill: move %q from %q to %q: %v", msg.MessageID, folder, dest, err)
				stats.Errored++
				continue
			}

			log.Printf("backfill: moved %q from %q to %q", msg.Subject, folder, dest)
			stats.Moved++
			saveBackfillVerdict(s.db, accountID, &msg, dest, verdict, "executed")

			activityType := "triaged"
			if verdict != nil && verdict.Expired {
				activityType = "expired"
			}
			ruleName := "Backfill: no rule matched"
			reason := fmt.Sprintf("Backfill from %q to %q", folder, dest)
			if verdict != nil {
				ruleName = "Backfill: " + verdict.Rule.Name
				if verdict.Reason != "" {
					reason = fmt.Sprintf("Backfill: %s", verdict.Reason)
				}
			}
			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            activityType,
				AccountID:       accountID,
				MessageIDHeader: msg.MessageID,
				Subject:         msg.Subject,
				Sender:          msg.Sender,
				RuleName:        ruleName,
				Destination:     dest,
				Reason:          reason,
				Confidence:      confidence,
			}); err != nil {
				log.Printf("backfill: log activity for %q: %v", msg.MessageID, err)
			}
		}
	}

	log.Printf("backfill: done — folders=%d inspected=%d moved=%d kept=%d skipped=%d errored=%d",
		stats.Folders, stats.Inspected, stats.Moved, stats.Kept, stats.Skipped, stats.Errored)
	return stats, nil
}

// backfillDestination decides where a re-evaluated message should land.
//   - nil verdict (no rule matched) → rescue folder (typically INBOX).
//   - Expired verdict → canonical Expired folder, falling back to the rule's
//     destination when none is configured.
//   - Classified verdict → the rule's destination, falling back to rescue.
func backfillDestination(v *rules.RuleVerdict, expiredFolder, rescueFolder string) string {
	if v == nil {
		return rescueFolder
	}
	if v.Expired {
		if expiredFolder != "" {
			return expiredFolder
		}
		if v.Rule.DestinationFolder != "" {
			return v.Rule.DestinationFolder
		}
		return "Expired"
	}
	if v.Classified && v.Rule.DestinationFolder != "" {
		return v.Rule.DestinationFolder
	}
	return rescueFolder
}

// saveBackfillVerdict writes a fresh verdict row reflecting the re-evaluation.
// status is "executed" when the message is in the destination implied by the
// verdict (whether moved or already there) and "pending" when low confidence
// caused us to leave it in place.
func saveBackfillVerdict(database *db.DB, accountID string, msg *imappkg.FetchedMessage, currentFolder string, v *rules.RuleVerdict, status string) {
	now := time.Now().UTC()
	row := db.Verdict{
		AccountID:         accountID,
		MessageIDHeader:   msg.MessageID,
		Subject:           msg.Subject,
		Sender:            msg.Sender,
		SentAt:            msg.Date,
		DestinationFolder: currentFolder,
		Status:            status,
		EvaluatedAt:       now,
	}
	if v != nil {
		ruleID := v.Rule.ID
		if ruleID != "" {
			row.RuleID = &ruleID
		}
		row.Reason = v.Reason
		row.Confidence = v.Confidence
		if v.ExpiresAt != nil {
			row.ExpiresAt = v.ExpiresAt
		}
	}
	if status == "executed" {
		row.ActedAt = &now
	}
	if err := database.SaveVerdict(row); err != nil {
		log.Printf("backfill: save verdict for %q: %v", msg.MessageID, err)
	}
}
