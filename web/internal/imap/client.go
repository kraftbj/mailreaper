package imap

import (
	"fmt"
	"net/mail"
	"strings"
	"time"

	imaplib "github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/kraftbj/mailreaper/internal/config"
)

// Client wraps go-imap/v2 with the operations MailReaper needs.
type Client struct {
	client *imapclient.Client
	config config.Account
}

// FetchedMessage holds the data extracted from an IMAP message.
type FetchedMessage struct {
	UID       uint32
	MessageID string
	Subject   string
	Sender    string
	Date      time.Time
	Folder    string
	Headers   map[string][]string // lowercase keys
}

// Connect dials the IMAP server and authenticates with the configured
// credentials. It uses TLS when acct.TLS is true, plain TCP otherwise.
func Connect(acct config.Account) (*Client, error) {
	addr := fmt.Sprintf("%s:%d", acct.Host, acct.Port)

	var (
		ic  *imapclient.Client
		err error
	)
	if acct.TLS {
		ic, err = imapclient.DialTLS(addr, nil)
	} else {
		ic, err = imapclient.DialInsecure(addr, nil)
	}
	if err != nil {
		return nil, fmt.Errorf("imap: dial %s: %w", addr, err)
	}

	if err := ic.Login(acct.Username, acct.Password).Wait(); err != nil {
		ic.Close()
		return nil, fmt.Errorf("imap: login as %s: %w", acct.Username, err)
	}

	return &Client{client: ic, config: acct}, nil
}

// Close terminates the IMAP connection.
func (c *Client) Close() error {
	return c.client.Close()
}

// FetchNewMessages selects the given folder and returns messages received since
// `since` that are no older than `maxAge`. At most `limit` messages are
// returned. If limit <= 0 no cap is applied.
func (c *Client) FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]FetchedMessage, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap: select %q: %w", folder, err)
	}

	criteria := &imaplib.SearchCriteria{
		Since: since,
	}
	searchData, err := c.client.Search(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: search in %q: %w", folder, err)
	}

	seqNums, ok := searchData.All.(imaplib.SeqSet)
	if !ok || len(seqNums) == 0 {
		return nil, nil
	}

	// Apply limit by trimming the set.
	allNums, ok := seqNums.Nums()
	if !ok {
		// Dynamic set — collect all numbers individually; fall through.
		allNums = nil
	}

	if limit > 0 && len(allNums) > limit {
		allNums = allNums[:limit]
		seqNums = imaplib.SeqSetNum(allNums...)
	}

	fetchOpts := &imaplib.FetchOptions{
		Envelope: true,
		UID:      true,
		BodySection: []*imaplib.FetchItemBodySection{
			{
				Specifier: imaplib.PartSpecifierHeader,
				Peek:      true,
			},
		},
	}

	msgs, err := c.client.Fetch(seqNums, fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: fetch in %q: %w", folder, err)
	}

	cutoff := time.Now().Add(-maxAge)
	var results []FetchedMessage
	for _, buf := range msgs {
		if buf.Envelope == nil {
			continue
		}

		// Skip messages newer than minAge (too fresh to evaluate).
		if maxAge > 0 && buf.Envelope.Date.After(cutoff) {
			continue
		}

		headers := make(map[string][]string)
		for _, sec := range buf.BodySection {
			if sec.Section != nil && sec.Section.Specifier != imaplib.PartSpecifierHeader {
				continue
			}
			parsed, parseErr := mail.ReadMessage(strings.NewReader(string(sec.Bytes)))
			if parseErr == nil {
				for k, vs := range parsed.Header {
					lower := strings.ToLower(k)
					headers[lower] = append(headers[lower], vs...)
				}
			}
			break
		}

		sender := ""
		if len(buf.Envelope.From) > 0 {
			addr := buf.Envelope.From[0]
			if addr.Name != "" {
				sender = fmt.Sprintf("%s <%s>", addr.Name, addr.Addr())
			} else {
				sender = addr.Addr()
			}
		}

		results = append(results, FetchedMessage{
			UID:       uint32(buf.UID),
			MessageID: buf.Envelope.MessageID,
			Subject:   buf.Envelope.Subject,
			Sender:    sender,
			Date:      buf.Envelope.Date,
			Folder:    folder,
			Headers:   headers,
		})
	}

	return results, nil
}

// FetchBody selects the given folder and returns the plain-text body of the
// message identified by uid.
func (c *Client) FetchBody(folder string, uid uint32) (string, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return "", fmt.Errorf("imap: select %q: %w", folder, err)
	}

	seqNums := imaplib.SeqSetNum(uid)
	fetchOpts := &imaplib.FetchOptions{
		BodySection: []*imaplib.FetchItemBodySection{
			{Peek: true},
		},
	}

	msgs, err := c.client.Fetch(seqNums, fetchOpts).Collect()
	if err != nil {
		return "", fmt.Errorf("imap: fetch body uid=%d in %q: %w", uid, folder, err)
	}
	if len(msgs) == 0 {
		return "", fmt.Errorf("imap: message uid=%d not found in %q", uid, folder)
	}

	for _, sec := range msgs[0].BodySection {
		if sec.Section == nil || sec.Section.Specifier == imaplib.PartSpecifierNone {
			return string(sec.Bytes), nil
		}
	}
	return "", nil
}

// MoveMessage selects folder, then moves the message with the given sequence
// number to destFolder.
func (c *Client) MoveMessage(folder string, seqNum uint32, destFolder string) error {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap: select %q: %w", folder, err)
	}

	numSet := imaplib.SeqSetNum(seqNum)
	if _, err := c.client.Move(numSet, destFolder).Wait(); err != nil {
		return fmt.Errorf("imap: move seq=%d from %q to %q: %w", seqNum, folder, destFolder, err)
	}
	return nil
}

// DeleteMessage selects folder, flags the message with the given sequence
// number as \Deleted, and optionally expunges it immediately.
func (c *Client) DeleteMessage(folder string, seqNum uint32, permanent bool) error {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("imap: select %q: %w", folder, err)
	}

	numSet := imaplib.SeqSetNum(seqNum)
	storeCmd := c.client.Store(numSet, &imaplib.StoreFlags{
		Op:     imaplib.StoreFlagsAdd,
		Silent: true,
		Flags:  []imaplib.Flag{imaplib.FlagDeleted},
	}, nil)
	if err := storeCmd.Close(); err != nil {
		return fmt.Errorf("imap: flag deleted seq=%d in %q: %w", seqNum, folder, err)
	}

	if permanent {
		if err := c.client.Expunge().Close(); err != nil {
			return fmt.Errorf("imap: expunge seq=%d in %q: %w", seqNum, folder, err)
		}
	}
	return nil
}

// EnsureFolder checks whether the named mailbox exists (via LIST) and creates
// it if it does not.
func (c *Client) EnsureFolder(name string) error {
	listCmd := c.client.List("", name, nil)
	mailboxes, err := listCmd.Collect()
	if err != nil {
		return fmt.Errorf("imap: list %q: %w", name, err)
	}

	for _, mb := range mailboxes {
		if strings.EqualFold(mb.Mailbox, name) {
			return nil // already exists
		}
	}

	if err := c.client.Create(name, nil).Wait(); err != nil {
		return fmt.Errorf("imap: create %q: %w", name, err)
	}
	return nil
}

// GetMessageIDsInFolder returns the Message-IDs of all messages currently in
// the named folder.
func (c *Client) GetMessageIDsInFolder(folder string) ([]string, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("imap: select %q: %w", folder, err)
	}

	// Search for ALL messages.
	criteria := &imaplib.SearchCriteria{}
	searchData, err := c.client.Search(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("imap: search in %q: %w", folder, err)
	}

	seqNums, ok := searchData.All.(imaplib.SeqSet)
	if !ok || len(seqNums) == 0 {
		return nil, nil
	}

	fetchOpts := &imaplib.FetchOptions{
		Envelope: true,
	}

	msgs, err := c.client.Fetch(seqNums, fetchOpts).Collect()
	if err != nil {
		return nil, fmt.Errorf("imap: fetch envelopes in %q: %w", folder, err)
	}

	ids := make([]string, 0, len(msgs))
	for _, buf := range msgs {
		if buf.Envelope != nil && buf.Envelope.MessageID != "" {
			ids = append(ids, buf.Envelope.MessageID)
		}
	}
	return ids, nil
}

// SanitizeFolderName trims leading and trailing whitespace from a folder name.
func SanitizeFolderName(name string) string {
	return strings.TrimSpace(name)
}

