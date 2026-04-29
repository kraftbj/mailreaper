package scanner

import (
	"fmt"
	"log"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// SweepDeferredExpiries checks for verdicts with an expiresAt date that has now
// passed and moves those messages to the Expired folder.
func (s *Scanner) SweepDeferredExpiries(client MailClient, accountID string) error {
	expiries, err := s.db.GetDeferredExpiries()
	if err != nil {
		return fmt.Errorf("sweep deferred: get expiries: %w", err)
	}

	if len(expiries) == 0 {
		return nil
	}

	// Find the expired folder.
	categories, err := s.db.GetCategories()
	if err != nil {
		return fmt.Errorf("sweep deferred: get categories: %w", err)
	}
	expiredFolder := ""
	for _, cat := range categories {
		if cat.ID == "expired" {
			expiredFolder = cat.FolderName
			break
		}
	}
	if expiredFolder == "" {
		return nil
	}

	if err := client.EnsureFolder(expiredFolder); err != nil {
		return fmt.Errorf("sweep deferred: ensure folder %q: %w", expiredFolder, err)
	}

	swept := 0
	for _, v := range expiries {
		if v.DestinationFolder == "" || v.DestinationFolder == expiredFolder {
			continue
		}

		// Find the message in its current folder and move it.
		msgIDs, err := client.GetMessageIDsInFolder(v.DestinationFolder)
		if err != nil {
			log.Printf("sweep deferred: get messages in %q: %v", v.DestinationFolder, err)
			continue
		}

		// Find UID for this message.
		msgs, err := client.GetMessagesInFolder(v.DestinationFolder)
		if err != nil {
			log.Printf("sweep deferred: get messages in %q: %v", v.DestinationFolder, err)
			continue
		}
		_ = msgIDs // used msgs instead for UID

		for _, msg := range msgs {
			if msg.MessageID != v.MessageIDHeader {
				continue
			}

			if err := client.MoveMessage(v.DestinationFolder, msg.UID, expiredFolder); err != nil {
				log.Printf("sweep deferred: move %q: %v", v.MessageIDHeader, err)
				break
			}

			// Update the verdict.
			v.DestinationFolder = expiredFolder
			v.Status = "executed"
			now := time.Now().UTC()
			v.ActedAt = &now
			if err := s.db.SaveVerdict(v); err != nil {
				log.Printf("sweep deferred: update verdict %q: %v", v.MessageIDHeader, err)
			}

			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            "expired",
				AccountID:       accountID,
				MessageIDHeader: v.MessageIDHeader,
				Subject:         v.Subject,
				Sender:          v.Sender,
				RuleName:        "Deferred expiry",
				Destination:     expiredFolder,
				Reason:          fmt.Sprintf("Expired at %s", v.ExpiresAt.Format("2006-01-02")),
				Confidence:      1.0,
			}); err != nil {
				log.Printf("sweep deferred: log activity: %v", err)
			}

			swept++
			break
		}
	}

	if swept > 0 {
		log.Printf("sweep: moved %d deferred-expiry messages to %q", swept, expiredFolder)
	}

	return nil
}

