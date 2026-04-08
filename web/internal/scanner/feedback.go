package scanner

import (
	"fmt"
	"log"

	"github.com/kraftbj/mailreaper/internal/db"
)

// DetectFeedback checks the inbox for messages that were previously moved
// (executed verdicts) but have since been returned by the user. Each such
// message is marked "corrected", a training example is recorded, and the LLM
// cache entry is invalidated.
func (s *Scanner) DetectFeedback(client MailClient, accountID string, inboxFolder string) error {
	// Get the set of Message-IDs currently in the inbox.
	inboxIDs, err := client.GetMessageIDsInFolder(inboxFolder)
	if err != nil {
		return fmt.Errorf("scanner: get inbox message ids: %w", err)
	}

	inboxSet := make(map[string]bool, len(inboxIDs))
	for _, id := range inboxIDs {
		inboxSet[id] = true
	}

	// Get all executed verdict Message-IDs for this account.
	executedIDs, err := s.db.GetExecutedMessageIDs(accountID)
	if err != nil {
		return fmt.Errorf("scanner: get executed message ids: %w", err)
	}

	for _, msgID := range executedIDs {
		if !inboxSet[msgID] {
			continue
		}

		// Message was moved but is back in the inbox — user corrected us.
		if err := s.db.UpdateVerdictStatus(msgID, "corrected"); err != nil {
			log.Printf("scanner: mark verdict corrected for %q: %v", msgID, err)
			continue
		}

		// Retrieve the verdict to get subject/sender for the training example.
		v, err := s.db.GetVerdictByMessageID(msgID)
		if err != nil {
			log.Printf("scanner: get verdict for feedback %q: %v", msgID, err)
			continue
		}

		if v != nil {
			if err := s.db.AddTrainingExample(db.TrainingExample{
				Category: "expiry",
				Subject:  v.Subject,
				Sender:   v.Sender,
				Label:    "keep",
				Source:   "correction",
			}); err != nil {
				log.Printf("scanner: add training example for %q: %v", msgID, err)
			}
		}

		// Invalidate the LLM cache so it will be re-evaluated next scan.
		if err := s.db.RemoveCachedVerdict(msgID); err != nil {
			log.Printf("scanner: remove cached verdict for %q: %v", msgID, err)
		}

		if err := s.db.LogActivity(db.ActivityEntry{
			Type:            "corrected",
			AccountID:       accountID,
			MessageIDHeader: msgID,
			Subject: func() string {
				if v != nil {
					return v.Subject
				}
				return ""
			}(),
			Sender: func() string {
				if v != nil {
					return v.Sender
				}
				return ""
			}(),
		}); err != nil {
			log.Printf("scanner: log correction activity for %q: %v", msgID, err)
		}
	}

	return nil
}
