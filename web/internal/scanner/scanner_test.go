package scanner

import (
	"context"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
)

// mockMailClient implements MailClient for testing.
type mockMailClient struct {
	messages    []imappkg.FetchedMessage
	movedMsgs   []string
	inboxMsgIDs []string
}

func (m *mockMailClient) FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]imappkg.FetchedMessage, error) {
	return m.messages, nil
}

func (m *mockMailClient) MoveMessage(folder string, uid uint32, dest string) error {
	m.movedMsgs = append(m.movedMsgs, dest)
	return nil
}

func (m *mockMailClient) EnsureFolder(name string) error {
	return nil
}

func (m *mockMailClient) GetMessageIDsInFolder(folder string) ([]string, error) {
	return m.inboxMsgIDs, nil
}

func (m *mockMailClient) FetchBody(folder string, uid uint32) (string, error) {
	return "", nil
}

func (m *mockMailClient) Close() error {
	return nil
}

// openTestDB creates an in-memory SQLite database for testing.
func openTestDB(t *testing.T) *db.DB {
	t.Helper()
	database, err := db.Open(":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { database.Close() })
	return database
}

// testConfig returns a minimal Config suitable for unit tests.
func testConfig() *config.Config {
	return &config.Config{
		LLM: config.LLMConfig{Provider: "none"},
		Scan: config.ScanConfig{
			MinMessageAgeMin:   0,
			MaxMessagesPerScan: 100,
		},
	}
}

// TestScanCycleTTLRule seeds a TTL rule and verifies that a matching message
// older than the TTL gets moved and saved with status "executed".
func TestScanCycleTTLRule(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	// Seed an account so the foreign key constraint is satisfied.
	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// Seed a TTL rule that matches subject "Your OTP Code" after 2 hours.
	rule := db.Rule{
		ID:       "rule-ttl-test",
		Name:     "OTP rule",
		Enabled:  true,
		Priority: 10,
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{"*OTP*"},
		},
		ExpirationConfig: db.ExpirationConfig{
			Type:  "ttl",
			Hours: 2,
		},
		Action:            "move",
		DestinationFolder: "Expired",
	}
	if err := database.SaveRule(rule); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	// Message is 3 hours old — should be expired.
	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<otp-001@example.com>",
		Subject:   "Your OTP Code",
		Sender:    "security@example.com",
		Date:      time.Now().Add(-3 * time.Hour),
		Folder:    "INBOX",
	}

	client := &mockMailClient{messages: []imappkg.FetchedMessage{msg}}
	s := New(database, cfg)

	if err := s.ScanAccount(context.Background(), client, "acct1", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	// Verify the message was moved.
	if len(client.movedMsgs) != 1 {
		t.Fatalf("expected 1 moved message, got %d", len(client.movedMsgs))
	}
	if client.movedMsgs[0] != "Expired" {
		t.Errorf("expected destination %q, got %q", "Expired", client.movedMsgs[0])
	}

	// Verify the verdict was saved with status "executed".
	v, err := database.GetVerdictByMessageID("<otp-001@example.com>")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Status != "executed" {
		t.Errorf("expected status %q, got %q", "executed", v.Status)
	}
}

// TestScanCycleNoMatch verifies that a message that doesn't match any rule
// results in no moves and no saved verdict.
func TestScanCycleNoMatch(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// Seed a rule that only matches a specific subject.
	rule := db.Rule{
		ID:       "rule-specific",
		Name:     "Specific subject",
		Enabled:  true,
		Priority: 10,
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{"*Secret OTP*"},
		},
		ExpirationConfig: db.ExpirationConfig{
			Type:  "ttl",
			Hours: 1,
		},
		Action: "move",
	}
	if err := database.SaveRule(rule); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	// Message subject does not match any rule.
	msg := imappkg.FetchedMessage{
		UID:       2,
		MessageID: "<newsletter-001@example.com>",
		Subject:   "Weekly Newsletter",
		Sender:    "news@example.com",
		Date:      time.Now().Add(-5 * time.Hour),
		Folder:    "INBOX",
	}

	client := &mockMailClient{messages: []imappkg.FetchedMessage{msg}}
	s := New(database, cfg)

	if err := s.ScanAccount(context.Background(), client, "acct1", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("expected 0 moved messages, got %d", len(client.movedMsgs))
	}

	v, err := database.GetVerdictByMessageID("<newsletter-001@example.com>")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v != nil {
		t.Errorf("expected no verdict, got status %q", v.Status)
	}
}

// TestScanSkipsLLMWhenProviderNone ensures that LLM rules are not evaluated
// when the LLM provider is set to "none".
func TestScanSkipsLLMWhenProviderNone(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig() // provider is "none"

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// Seed an LLM rule that should match.
	rule := db.Rule{
		ID:       "rule-llm",
		Name:     "LLM expiry",
		Enabled:  true,
		Priority: 5,
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{"*"},
		},
		ExpirationConfig: db.ExpirationConfig{
			Type: "llm",
		},
		Action:            "move",
		DestinationFolder: "Expired",
	}
	if err := database.SaveRule(rule); err != nil {
		t.Fatalf("save rule: %v", err)
	}

	msg := imappkg.FetchedMessage{
		UID:       3,
		MessageID: "<promo-001@example.com>",
		Subject:   "Flash Sale Ends Tonight!",
		Sender:    "promo@shop.com",
		Date:      time.Now().Add(-25 * time.Hour),
		Folder:    "INBOX",
	}

	client := &mockMailClient{messages: []imappkg.FetchedMessage{msg}}
	s := New(database, cfg)

	if err := s.ScanAccount(context.Background(), client, "acct1", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	// With provider=none, LLM rules are skipped — no moves should happen.
	if len(client.movedMsgs) != 0 {
		t.Errorf("expected 0 moved messages (LLM skipped), got %d", len(client.movedMsgs))
	}
}
