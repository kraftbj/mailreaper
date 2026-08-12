package scanner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
)

// mockMailClient implements MailClient for testing.
type mockMailClient struct {
	messages       []imappkg.FetchedMessage
	movedMsgs      []string
	inboxMsgIDs    []string
	folderMessages map[string][]imappkg.FetchedMessage
	moveErr        error // when set, MoveMessage fails without recording
}

func (m *mockMailClient) FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]imappkg.FetchedMessage, error) {
	return m.messages, nil
}

func (m *mockMailClient) MoveMessage(folder string, uid uint32, dest string) error {
	if m.moveErr != nil {
		return m.moveErr
	}
	m.movedMsgs = append(m.movedMsgs, dest)
	return nil
}

func (m *mockMailClient) EnsureFolder(name string) error {
	return nil
}

func (m *mockMailClient) GetMessageIDsInFolder(folder string) ([]string, error) {
	return m.inboxMsgIDs, nil
}

func (m *mockMailClient) GetMessagesInFolder(folder string) ([]imappkg.FetchedMessage, error) {
	return m.folderMessages[folder], nil
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

// TestScanCycleFailedMoveRecordsMoveFailed verifies that a failed IMAP move
// is recorded as move_failed rather than executed. Recording executed is a
// permanent lie -- the dedup check means the message is never reconsidered,
// so the database disagrees with the mailbox forever. Recording nothing is
// also wrong: it re-spends an LLM call on every subsequent scan.
func TestScanCycleFailedMoveRecordsMoveFailed(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

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

	msg := imappkg.FetchedMessage{
		UID:       1,
		MessageID: "<msg-1@test>",
		Subject:   "Your OTP Code",
		Sender:    "security@example.com",
		Date:      time.Now().Add(-3 * time.Hour),
		Folder:    "INBOX",
	}

	client := &mockMailClient{
		messages: []imappkg.FetchedMessage{msg},
		moveErr:  errors.New("IMAP MOVE failed: mailbox is read-only"),
	}
	s := New(database, cfg)

	if err := s.ScanAccount(context.Background(), client, "acct1", []string{"INBOX"}); err != nil {
		t.Fatalf("ScanAccount: %v", err)
	}

	if len(client.movedMsgs) != 0 {
		t.Errorf("recorded %d move(s) despite MoveMessage failing", len(client.movedMsgs))
	}

	v, err := database.GetVerdictByMessageID("<msg-1@test>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID: %v", err)
	}
	if v == nil {
		t.Fatal("no verdict saved; the next scan will re-spend an LLM call on this message")
	}
	if v.Status != "move_failed" {
		t.Errorf("Status = %q, want move_failed", v.Status)
	}
	if v.ActedAt != nil {
		t.Error("ActedAt set despite the move failing")
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

// TestDetectManualClassifications verifies that messages found in a category
// folder with no existing verdict are recorded as manual classifications.
func TestDetectManualClassifications(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	// Seed a category pointing at a triage folder.
	cat := db.Category{
		ID:         "cat-newsletters",
		Name:       "Newsletters",
		FolderName: "Folders/AI-Triage/Newsletters",
	}
	if err := database.SaveCategory(cat); err != nil {
		t.Fatalf("save category: %v", err)
	}

	// One message sitting in the triage folder, no verdict in DB.
	msg := imappkg.FetchedMessage{
		UID:       42,
		MessageID: "<newsletter-manual-001@example.com>",
		Subject:   "Weekly Digest",
		Sender:    "digest@example.com",
		Date:      time.Now().Add(-48 * time.Hour),
		Folder:    "Folders/AI-Triage/Newsletters",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Newsletters": {msg},
		},
	}
	s := New(database, cfg)

	if err := s.DetectManualClassifications(client, "acct1"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	// Verdict should have been created with status "manual".
	v, err := database.GetVerdictByMessageID("<newsletter-manual-001@example.com>")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Status != "manual" {
		t.Errorf("expected status %q, got %q", "manual", v.Status)
	}
	if v.DestinationFolder != "Folders/AI-Triage/Newsletters" {
		t.Errorf("expected destination folder %q, got %q", "Folders/AI-Triage/Newsletters", v.DestinationFolder)
	}
	if v.Confidence != 1.0 {
		t.Errorf("expected confidence 1.0, got %f", v.Confidence)
	}

	// Training example should have been recorded.
	examples, err := database.GetTrainingExamples(cat.ID)
	if err != nil {
		t.Fatalf("get training examples: %v", err)
	}
	if len(examples) != 1 {
		t.Fatalf("expected 1 training example, got %d", len(examples))
	}
	if examples[0].Source != "manual" {
		t.Errorf("expected source %q, got %q", "manual", examples[0].Source)
	}
	if examples[0].Subject != msg.Subject {
		t.Errorf("expected subject %q, got %q", msg.Subject, examples[0].Subject)
	}

	// Activity log should have an entry.
	log, err := database.GetActivityLog(10)
	if err != nil {
		t.Fatalf("get activity log: %v", err)
	}
	if len(log) != 1 {
		t.Fatalf("expected 1 activity entry, got %d", len(log))
	}
	if log[0].Type != "manual_classify" {
		t.Errorf("expected activity type %q, got %q", "manual_classify", log[0].Type)
	}
	if log[0].Destination != "Folders/AI-Triage/Newsletters" {
		t.Errorf("expected destination %q, got %q", "Folders/AI-Triage/Newsletters", log[0].Destination)
	}
}

// TestDetectManualClassificationsSkipsExistingVerdicts verifies that messages
// already in the DB (processed by the scanner previously) are not re-recorded.
func TestDetectManualClassificationsSkipsExistingVerdicts(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	cat := db.Category{
		ID:         "cat-receipts",
		Name:       "Receipts",
		FolderName: "Folders/AI-Triage/Receipts",
	}
	if err := database.SaveCategory(cat); err != nil {
		t.Fatalf("save category: %v", err)
	}

	// Pre-seed a verdict so the message looks like it was handled by the scanner.
	now := time.Now().UTC()
	if err := database.SaveVerdict(db.Verdict{
		AccountID:         "acct1",
		MessageIDHeader:   "<receipt-001@example.com>",
		Subject:           "Your receipt",
		Sender:            "no-reply@shop.com",
		SentAt:            now.Add(-72 * time.Hour),
		Status:            "executed",
		DestinationFolder: "Folders/AI-Triage/Receipts",
		Reason:            "matched classify rule",
		Confidence:        0.9,
		EvaluatedAt:       now,
		ActedAt:           &now,
	}); err != nil {
		t.Fatalf("save existing verdict: %v", err)
	}

	msg := imappkg.FetchedMessage{
		UID:       7,
		MessageID: "<receipt-001@example.com>",
		Subject:   "Your receipt",
		Sender:    "no-reply@shop.com",
		Date:      now.Add(-72 * time.Hour),
		Folder:    "Folders/AI-Triage/Receipts",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Receipts": {msg},
		},
	}
	s := New(database, cfg)

	if err := s.DetectManualClassifications(client, "acct1"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	// No training examples should have been added.
	examples, err := database.GetTrainingExamples(cat.ID)
	if err != nil {
		t.Fatalf("get training examples: %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("expected 0 training examples for already-verdicted message, got %d", len(examples))
	}

	// Activity log should be empty.
	actLog, err := database.GetActivityLog(10)
	if err != nil {
		t.Fatalf("get activity log: %v", err)
	}
	if len(actLog) != 0 {
		t.Errorf("expected 0 activity entries, got %d", len(actLog))
	}
}

// TestDetectManualClassificationsIgnoresMoveFailed verifies that a message
// sitting in a category folder with a move_failed verdict is not treated as
// a manual placement. The move may have actually succeeded on the IMAP side
// before the error was returned, so this is an ambiguous case, not proof of
// a user filing -- it must not be laundered into training data.
func TestDetectManualClassificationsIgnoresMoveFailed(t *testing.T) {
	database := openTestDB(t)
	cfg := testConfig()

	if err := database.UpsertAccount("acct1", "Test Account"); err != nil {
		t.Fatalf("upsert account: %v", err)
	}

	cat := db.Category{
		ID:         "cat-receipts",
		Name:       "Receipts",
		FolderName: "Folders/AI-Triage/Receipts",
	}
	if err := database.SaveCategory(cat); err != nil {
		t.Fatalf("save category: %v", err)
	}

	// Pre-seed a move_failed verdict: MailReaper attempted this move and
	// got an error back, but the message is sitting in the destination
	// folder anyway (the ambiguous case).
	now := time.Now().UTC()
	if err := database.SaveVerdict(db.Verdict{
		AccountID:         "acct1",
		MessageIDHeader:   "<receipt-002@example.com>",
		Subject:           "Your receipt",
		Sender:            "no-reply@shop.com",
		SentAt:            now.Add(-72 * time.Hour),
		Status:            "move_failed",
		DestinationFolder: "Folders/AI-Triage/Receipts",
		Reason:            "matched classify rule",
		Confidence:        0.9,
		EvaluatedAt:       now,
	}); err != nil {
		t.Fatalf("save existing verdict: %v", err)
	}

	msg := imappkg.FetchedMessage{
		UID:       8,
		MessageID: "<receipt-002@example.com>",
		Subject:   "Your receipt",
		Sender:    "no-reply@shop.com",
		Date:      now.Add(-72 * time.Hour),
		Folder:    "Folders/AI-Triage/Receipts",
	}

	client := &mockMailClient{
		folderMessages: map[string][]imappkg.FetchedMessage{
			"Folders/AI-Triage/Receipts": {msg},
		},
	}
	s := New(database, cfg)

	if err := s.DetectManualClassifications(client, "acct1"); err != nil {
		t.Fatalf("DetectManualClassifications: %v", err)
	}

	// No training example should have been added for the ambiguous case.
	examples, err := database.GetTrainingExamples(cat.ID)
	if err != nil {
		t.Fatalf("get training examples: %v", err)
	}
	if len(examples) != 0 {
		t.Errorf("expected 0 training examples for move_failed message, got %d", len(examples))
	}

	// The verdict must remain move_failed, not be overwritten as manual.
	v, err := database.GetVerdictByMessageID("<receipt-002@example.com>")
	if err != nil {
		t.Fatalf("get verdict: %v", err)
	}
	if v == nil {
		t.Fatal("expected verdict, got nil")
	}
	if v.Status != "move_failed" {
		t.Errorf("expected status to remain %q, got %q", "move_failed", v.Status)
	}

	// Activity log should be empty.
	actLog, err := database.GetActivityLog(10)
	if err != nil {
		t.Fatalf("get activity log: %v", err)
	}
	if len(actLog) != 0 {
		t.Errorf("expected 0 activity entries, got %d", len(actLog))
	}
}
