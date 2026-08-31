package scanner

import (
	"fmt"
	"log"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

/*
orphanedStatus marks a deferred-expiry verdict whose message is no longer in
the folder the verdict recorded -- the user moved it, or deleted it. Such a
verdict can never be satisfied by a move, and GetDeferredExpiries filters on
status IN ('executed','manual'), so setting this drops the row out of the
sweep's result set permanently.

Leaving them in place is what let the live backlog grow to 191 rows, the
oldest four months past its deadline: each one re-listed its folder on every
scan cycle, found nothing, and stayed.
*/
const orphanedStatus = "orphaned"

/*
SweepDeferredExpiries moves messages whose recorded expires_at has now passed
into the canonical Expired folder.

Expiries are grouped by folder before any IMAP work happens, so the cost is
one SELECT + SEARCH + FETCH per distinct folder rather than one per verdict.
The previous version called GetMessagesInFolder inside the per-verdict loop
and re-fetched the same folder dozens of times in a single sweep against
folders holding thousands of messages.
*/
func (s *Scanner) SweepDeferredExpiries(client MailClient, accountID string) error {
	expiries, err := s.db.GetDeferredExpiries(accountID)
	if err != nil {
		return fmt.Errorf("sweep deferred: get expiries: %w", err)
	}
	if len(expiries) == 0 {
		return nil
	}

	expiredFolder := s.canonicalExpiredFolder()
	if expiredFolder == "" {
		return nil
	}

	byFolder := map[string][]db.Verdict{}
	for _, v := range expiries {
		if v.DestinationFolder == "" || v.DestinationFolder == expiredFolder {
			continue
		}
		byFolder[v.DestinationFolder] = append(byFolder[v.DestinationFolder], v)
	}
	if len(byFolder) == 0 {
		return nil
	}

	if err := client.EnsureFolder(expiredFolder); err != nil {
		return fmt.Errorf("sweep deferred: ensure folder %q: %w", expiredFolder, err)
	}

	swept, orphaned := 0, 0
	for folder, verdicts := range byFolder {
		msgs, err := client.GetMessagesInFolder(folder)
		if err != nil {
			log.Printf("sweep deferred: get messages in %q: %v", folder, err)
			continue
		}

		uidByMessageID := make(map[string]uint32, len(msgs))
		for _, m := range msgs {
			if m.MessageID != "" {
				uidByMessageID[m.MessageID] = m.UID
			}
		}

		for _, v := range verdicts {
			uid, present := uidByMessageID[v.MessageIDHeader]
			if !present {
				v.Status = orphanedStatus
				if err := s.db.SaveVerdict(v); err != nil {
					log.Printf("sweep deferred: mark %q orphaned: %v", v.MessageIDHeader, err)
					continue
				}
				orphaned++
				log.Printf("sweep deferred: %q is no longer in %q; marked orphaned", v.MessageIDHeader, folder)
				continue
			}

			if err := client.MoveMessage(folder, uid, expiredFolder); err != nil {
				log.Printf("sweep deferred: move %q: %v", v.MessageIDHeader, err)
				continue
			}

			v.DestinationFolder = expiredFolder
			v.Status = "executed"
			now := time.Now().UTC()
			v.ActedAt = &now
			if err := s.db.SaveVerdict(v); err != nil {
				log.Printf("sweep deferred: update verdict %q: %v", v.MessageIDHeader, err)
			}

			if err := s.db.RecordPlacement(v.MessageIDHeader, expiredFolder); err != nil {
				log.Printf("sweep deferred: record placement for %q: %v", v.MessageIDHeader, err)
			}

			reason := "Deferred expiry"
			if v.ExpiresAt != nil {
				reason = fmt.Sprintf("Expired at %s", v.ExpiresAt.Format("2006-01-02"))
			}
			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            "expired",
				AccountID:       accountID,
				MessageIDHeader: v.MessageIDHeader,
				Subject:         v.Subject,
				Sender:          v.Sender,
				RuleName:        "Deferred expiry",
				Destination:     expiredFolder,
				Reason:          reason,
				Confidence:      1.0,
			}); err != nil {
				log.Printf("sweep deferred: log activity: %v", err)
			}

			swept++
		}
	}

	if swept > 0 || orphaned > 0 {
		log.Printf("sweep: moved %d deferred-expiry message(s) to %q, orphaned %d whose message had left its folder",
			swept, expiredFolder, orphaned)
	}
	return nil
}
