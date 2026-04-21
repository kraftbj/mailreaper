package scanner

import (
	"fmt"
	"log"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// folderMaxAge defines how long messages stay in each triage folder before
// being swept to Expired. Folders not listed here are not swept.
var folderMaxAge = map[string]time.Duration{
	"notifications": 3 * 24 * time.Hour,  // 3 days
	"promotions":    7 * 24 * time.Hour,  // 1 week
	"expired":       0,                    // not swept (already expired)
	"newsletters":   0,                    // not swept (reading material)
	"paper-trail":   0,                    // not swept (archival)
	"hobbies":       0,                    // not swept (reading material)
}

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

// SweepExpiredNotifications checks triage folders for messages older than their
// category's max age and moves them to the Expired folder.
func (s *Scanner) SweepExpiredNotifications(client MailClient, accountID string) error {
	categories, err := s.db.GetCategories()
	if err != nil {
		return fmt.Errorf("sweep: get categories: %w", err)
	}

	// Find the expired folder name.
	expiredFolder := ""
	for _, cat := range categories {
		if cat.ID == "expired" {
			expiredFolder = cat.FolderName
			break
		}
	}
	if expiredFolder == "" {
		return nil // no expired category configured
	}

	for _, cat := range categories {
		maxAge, ok := folderMaxAge[cat.ID]
		if !ok || maxAge == 0 || cat.FolderName == "" {
			continue
		}

		cutoff := time.Now().Add(-maxAge)

		msgs, err := client.GetMessagesInFolder(cat.FolderName)
		if err != nil {
			log.Printf("sweep: get messages in %q: %v", cat.FolderName, err)
			continue
		}

		swept := 0
		for _, msg := range msgs {
			if msg.Date.After(cutoff) {
				continue // too recent
			}

			if err := client.EnsureFolder(expiredFolder); err != nil {
				log.Printf("sweep: ensure folder %q: %v", expiredFolder, err)
				break
			}

			if err := client.MoveMessage(cat.FolderName, msg.UID, expiredFolder); err != nil {
				log.Printf("sweep: move %q from %q to %q: %v", msg.MessageID, cat.FolderName, expiredFolder, err)
				continue
			}

			swept++

			if err := s.db.LogActivity(db.ActivityEntry{
				Type:            "expired",
				AccountID:       accountID,
				MessageIDHeader: msg.MessageID,
				Subject:         msg.Subject,
				Sender:          msg.Sender,
				RuleName:        fmt.Sprintf("Sweep: %s > %s", cat.Name, maxAge),
				Destination:     expiredFolder,
				Reason:          fmt.Sprintf("Aged out of %s after %s", cat.Name, maxAge),
				Confidence:      1.0,
			}); err != nil {
				log.Printf("sweep: log activity: %v", err)
			}
		}

		if swept > 0 {
			log.Printf("sweep: moved %d expired messages from %q to %q", swept, cat.FolderName, expiredFolder)
		}
	}

	return nil
}
