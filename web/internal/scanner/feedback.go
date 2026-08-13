package scanner

import (
	"fmt"
	"log"
	"time"

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

			// The user just reversed this exact move, revoking MailReaper's
			// claim on v.DestinationFolder for this message. Delete the
			// placement so a later genuine manual filing into that same
			// folder is detected again instead of being silently swallowed
			// by DetectManualClassifications' HasPlacement check.
			if v.DestinationFolder != "" {
				if err := s.db.DeletePlacement(msgID, v.DestinationFolder); err != nil {
					log.Printf("scanner: delete placement for %q: %v", msgID, err)
				}
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

// DetectManualClassifications checks each category's triage folder for messages
// that have no existing verdict. These must have been manually placed by the
// user, so a "manual" verdict and a training example are recorded for each one.
func (s *Scanner) DetectManualClassifications(client MailClient, accountID string) error {
	categories, err := s.db.GetCategories()
	if err != nil {
		return fmt.Errorf("scanner: get categories: %w", err)
	}

	for _, cat := range categories {
		if cat.FolderName == "" {
			continue
		}

		msgs, err := client.GetMessagesInFolder(cat.FolderName)
		if err != nil {
			log.Printf("scanner: get messages in folder %q: %v", cat.FolderName, err)
			continue
		}

		for _, msg := range msgs {
			if msg.MessageID == "" {
				continue
			}

			existing, err := s.db.GetVerdictByMessageID(msg.MessageID)
			if err != nil {
				log.Printf("scanner: check verdict for %q: %v", msg.MessageID, err)
				continue
			}
			if existing != nil && existing.Status == "move_failed" {
				// A move_failed verdict means MailReaper attempted this
				// exact move and got an error back, but the IMAP MOVE may
				// have actually succeeded before the error was returned
				// (an ambiguous outcome). A message sitting in this folder
				// with a move_failed verdict is therefore not proof of a
				// user placement -- do not launder an ambiguous failure
				// into training data. Kept as its own explicit condition,
				// not folded into the general "existing != nil" check
				// below, so this guard survives future changes to how
				// "already handled" is determined.
				continue
			}
			if existing != nil {
				// Already have a verdict — not a manual placement.
				continue
			}

			placed, err := s.db.HasPlacement(msg.MessageID, cat.FolderName)
			if err != nil {
				log.Printf("scanner: check placement for %q: %v", msg.MessageID, err)
				continue
			}
			if placed {
				// MailReaper itself moved this message into cat.FolderName
				// at some point. The verdict row that recorded that move
				// may be gone (e.g. a full rescan calls ClearAllVerdicts),
				// but the placements record survives that clear, so this
				// is still not proof of a user filing.
				continue
			}

			// No verdict and no recorded placement: the user manually placed this message.
			log.Printf("scanner: manual classification detected: %q → %s", msg.Subject, cat.FolderName)

			now := time.Now().UTC()
			v := db.Verdict{
				AccountID:         accountID,
				MessageIDHeader:   msg.MessageID,
				Subject:           msg.Subject,
				Sender:            msg.Sender,
				SentAt:            msg.Date,
				Status:            "manual",
				DestinationFolder: cat.FolderName,
				Reason:            "Manually classified by user",
				Confidence:        1.0,
				EvaluatedAt:       now,
				ActedAt:           &now,
			}
			if err := s.db.SaveVerdict(v); err != nil {
				log.Printf("scanner: save manual verdict for %q: %v", msg.MessageID, err)
				continue
			}

			if err := s.db.AddTrainingExample(db.TrainingExample{
				Category: cat.ID,
				Subject:  msg.Subject,
				Sender:   msg.Sender,
				Label:    cat.FolderName,
				Source:   "manual",
			}); err != nil {
				log.Printf("scanner: add training example for %q: %v", msg.MessageID, err)
			}

			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            "manual_classify",
				AccountID:       accountID,
				MessageIDHeader: msg.MessageID,
				Subject:         msg.Subject,
				Sender:          msg.Sender,
				Destination:     cat.FolderName,
				Reason:          "Manually classified by user",
				Confidence:      1.0,
			}); err != nil {
				log.Printf("scanner: log manual classify activity for %q: %v", msg.MessageID, err)
			}
		}
	}

	return nil
}
