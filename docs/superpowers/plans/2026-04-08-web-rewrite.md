# MailReaper Web Rewrite — Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rewrite MailReaper from a Thunderbird extension into a standalone Go application with IMAP mail access, SQLite storage, and a web dashboard for mail triage.

**Architecture:** Single Go binary containing a cron-scheduled scanner (IMAP fetch → rule evaluation → action execution), a rule engine ported from the existing JS implementation, Gemini/Ollama LLM backends, and an embedded web server serving a vanilla HTML/JS dashboard. SQLite for all persistence. Docker for deployment.

**Tech Stack:** Go 1.22+, go-imap/v2, modernc.org/sqlite (pure Go), net/http, embed.FS, Docker

**Spec:** `docs/superpowers/specs/2026-04-08-web-rewrite-design.md`

**Branch:** `web-rewrite` (off `trunk`)

---

## File Structure

```
web/                          # All new code lives here (separate from extension code)
├── cmd/
│   └── mailreaper/
│       └── main.go           # Entry point: load config, init DB, start scanner + server
├── internal/
│   ├── config/
│   │   └── config.go         # YAML config loading with env var substitution
│   ├── db/
│   │   ├── db.go             # SQLite connection, migrations, helpers
│   │   ├── rules.go          # Rules CRUD
│   │   ├── verdicts.go       # Verdicts CRUD (including pending queue)
│   │   ├── activity.go       # Activity log CRUD
│   │   ├── categories.go     # Categories CRUD
│   │   ├── training.go       # Training examples CRUD
│   │   └── llmcache.go       # LLM cache with tiered TTL
│   ├── imap/
│   │   └── client.go         # IMAP connection, fetch, move, folder management
│   ├── rules/
│   │   ├── engine.go         # Rule evaluation: matchesRule, evaluateExpiration
│   │   ├── defaults.go       # Default built-in rules (ported from JS)
│   │   └── glob.go           # Glob pattern matching (ported from JS)
│   ├── llm/
│   │   ├── adapter.go        # Gemini/Ollama dispatch, response parsing
│   │   └── prompts.go        # Prompt templates (ported from JS)
│   ├── scanner/
│   │   ├── scanner.go        # Scan cycle orchestration
│   │   └── feedback.go       # Feedback detection (Inbox return = correction)
│   └── server/
│       ├── server.go         # HTTP server setup, middleware
│       ├── api.go            # JSON API routes
│       └── sse.go            # Server-sent events for live updates
├── ui/
│   ├── index.html            # Dashboard page
│   ├── review.html           # Review queue page
│   ├── rules.html            # Rules management page
│   ├── settings.html         # Settings page
│   ├── css/
│   │   └── style.css         # Dashboard styles
│   └── js/
│       ├── app.js            # Shared: routing, SSE, fetch helpers
│       ├── dashboard.js      # Dashboard page logic
│       ├── review.js         # Review queue logic
│       ├── rules.js          # Rules page logic
│       └── settings.js       # Settings page logic
├── config.example.yaml       # Example config file
├── Dockerfile
├── docker-compose.yml
├── go.mod
└── go.sum
```

---

## Task 1: Project Scaffolding and Config

**Files:**
- Create: `web/cmd/mailreaper/main.go`
- Create: `web/internal/config/config.go`
- Create: `web/go.mod`
- Create: `web/config.example.yaml`

- [ ] **Step 1: Create branch**

```bash
git checkout -b web-rewrite trunk
```

- [ ] **Step 2: Initialize Go module**

```bash
mkdir -p web
cd web
go mod init github.com/kraftbj/mailreaper
```

- [ ] **Step 3: Write config test**

Create `web/internal/config/config_test.go`:

```go
package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	yaml := `
accounts:
  - name: test
    host: imap.example.com
    port: 993
    username: user@example.com
    password: secret
    tls: true
    folders:
      scan: [INBOX]

llm:
  provider: none

scan:
  interval_minutes: 15
  min_message_age_minutes: 30
  max_messages_per_scan: 50

server:
  port: 9025
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(cfg.Accounts))
	}
	if cfg.Accounts[0].Host != "imap.example.com" {
		t.Errorf("host = %q, want %q", cfg.Accounts[0].Host, "imap.example.com")
	}
	if cfg.Scan.IntervalMinutes != 15 {
		t.Errorf("interval = %d, want 15", cfg.Scan.IntervalMinutes)
	}
	if cfg.Server.Port != 9025 {
		t.Errorf("port = %d, want 9025", cfg.Server.Port)
	}
}

func TestLoadConfigEnvSubstitution(t *testing.T) {
	t.Setenv("TEST_IMAP_PASS", "env-secret")

	yaml := `
accounts:
  - name: test
    host: imap.example.com
    port: 993
    username: user@example.com
    password: ${TEST_IMAP_PASS}
    tls: true

llm:
  provider: none

scan:
  interval_minutes: 30
  min_message_age_minutes: 60
  max_messages_per_scan: 100

server:
  port: 8025
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Accounts[0].Password != "env-secret" {
		t.Errorf("password = %q, want %q", cfg.Accounts[0].Password, "env-secret")
	}
}

func TestLoadConfigDefaults(t *testing.T) {
	yaml := `
accounts: []
llm:
  provider: none
`
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")
	if err := os.WriteFile(path, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Scan.IntervalMinutes != 30 {
		t.Errorf("default interval = %d, want 30", cfg.Scan.IntervalMinutes)
	}
	if cfg.Scan.MaxMessagesPerScan != 100 {
		t.Errorf("default max = %d, want 100", cfg.Scan.MaxMessagesPerScan)
	}
	if cfg.Server.Port != 8025 {
		t.Errorf("default port = %d, want 8025", cfg.Server.Port)
	}
}
```

- [ ] **Step 4: Run test to verify it fails**

Run: `cd web && go test ./internal/config/ -v`
Expected: FAIL — `Load` not defined

- [ ] **Step 5: Implement config loader**

Create `web/internal/config/config.go`:

```go
package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Accounts []Account `yaml:"accounts"`
	LLM      LLM       `yaml:"llm"`
	Scan     Scan      `yaml:"scan"`
	Server   Server    `yaml:"server"`
}

type Account struct {
	Name     string        `yaml:"name"`
	Host     string        `yaml:"host"`
	Port     int           `yaml:"port"`
	Username string        `yaml:"username"`
	Password string        `yaml:"password"`
	TLS      bool          `yaml:"tls"`
	Folders  AccountFolder `yaml:"folders"`
}

type AccountFolder struct {
	Scan []string `yaml:"scan"`
}

type LLM struct {
	Provider string       `yaml:"provider"` // none, gemini, ollama
	Gemini   GeminiConfig `yaml:"gemini"`
	Ollama   OllamaConfig `yaml:"ollama"`
}

type GeminiConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type OllamaConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
}

type Scan struct {
	IntervalMinutes    int `yaml:"interval_minutes"`
	MinMessageAgeMin   int `yaml:"min_message_age_minutes"`
	MaxMessagesPerScan int `yaml:"max_messages_per_scan"`
}

type Server struct {
	Port int `yaml:"port"`
}

var envVarPattern = regexp.MustCompile(`\$\{([^}]+)\}`)

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	// Substitute ${ENV_VAR} references
	expanded := envVarPattern.ReplaceAllStringFunc(string(data), func(match string) string {
		varName := envVarPattern.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(varName); ok {
			return val
		}
		return match // Leave unresolved vars as-is
	})

	cfg := &Config{
		Scan: Scan{
			IntervalMinutes:    30,
			MinMessageAgeMin:   60,
			MaxMessagesPerScan: 100,
		},
		Server: Server{
			Port: 8025,
		},
		LLM: LLM{
			Gemini: GeminiConfig{Model: "gemini-2.5-flash"},
			Ollama: OllamaConfig{
				Endpoint: "http://localhost:11434",
				Model:    "qwen2.5:7b",
			},
		},
	}

	if err := yaml.Unmarshal([]byte(expanded), cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	return cfg, nil
}
```

- [ ] **Step 6: Add yaml dependency and run tests**

```bash
cd web && go get gopkg.in/yaml.v3 && go test ./internal/config/ -v
```

Expected: all 3 tests PASS

- [ ] **Step 7: Create example config**

Create `web/config.example.yaml`:

```yaml
accounts:
  - name: personal
    host: imap.fastmail.com
    port: 993
    username: user@fastmail.com
    password: ${IMAP_PASSWORD}
    tls: true
    folders:
      scan: [INBOX]

llm:
  provider: none  # none | gemini | ollama
  gemini:
    api_key: ${GEMINI_API_KEY}
    model: gemini-2.5-flash
  ollama:
    endpoint: http://localhost:11434
    model: qwen2.5:7b

scan:
  interval_minutes: 30
  min_message_age_minutes: 60
  max_messages_per_scan: 100

server:
  port: 8025
```

- [ ] **Step 8: Create minimal main.go**

Create `web/cmd/mailreaper/main.go`:

```go
package main

import (
	"flag"
	"fmt"
	"log"
	"os"

	"github.com/kraftbj/mailreaper/internal/config"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
		os.Exit(1)
	}

	fmt.Printf("MailReaper loaded with %d account(s), LLM provider: %s\n", len(cfg.Accounts), cfg.LLM.Provider)
}
```

- [ ] **Step 9: Commit**

```bash
git add web/
git commit -m "feat: scaffold Go project with config loader and env var substitution"
```

---

## Task 2: SQLite Database Layer

**Files:**
- Create: `web/internal/db/db.go`
- Create: `web/internal/db/db_test.go`

- [ ] **Step 1: Write database init test**

Create `web/internal/db/db_test.go`:

```go
package db

import (
	"path/filepath"
	"testing"
)

func TestOpenAndMigrate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer database.Close()

	// Verify all tables exist
	tables := []string{"accounts", "rules", "verdicts", "activity_log", "llm_cache", "training_examples", "categories"}
	for _, table := range tables {
		var name string
		err := database.db.QueryRow("SELECT name FROM sqlite_master WHERE type='table' AND name=?", table).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "subdir", "test.db")
	database, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	defer database.Close()
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/db/ -v`
Expected: FAIL — `Open` not defined

- [ ] **Step 3: Implement database layer**

Create `web/internal/db/db.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"

	_ "modernc.org/sqlite"
)

type DB struct {
	db *sql.DB
}

func Open(path string) (*DB, error) {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create db directory: %w", err)
	}

	sqlDB, err := sql.Open("sqlite", path+"?_journal_mode=WAL&_busy_timeout=5000")
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// Enable foreign keys
	if _, err := sqlDB.Exec("PRAGMA foreign_keys = ON"); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("enable foreign keys: %w", err)
	}

	d := &DB{db: sqlDB}
	if err := d.migrate(); err != nil {
		sqlDB.Close()
		return nil, fmt.Errorf("migrate: %w", err)
	}

	return d, nil
}

func (d *DB) Close() error {
	return d.db.Close()
}

func (d *DB) migrate() error {
	migrations := []string{
		`CREATE TABLE IF NOT EXISTS accounts (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			last_scan_at TIMESTAMP,
			last_scan_message_count INTEGER DEFAULT 0
		)`,
		`CREATE TABLE IF NOT EXISTS rules (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			enabled BOOLEAN NOT NULL DEFAULT 1,
			priority INTEGER NOT NULL DEFAULT 100,
			builtin BOOLEAN NOT NULL DEFAULT 0,
			match_config JSON NOT NULL DEFAULT '{}',
			expiration_config JSON NOT NULL DEFAULT '{}',
			action TEXT NOT NULL DEFAULT 'move',
			destination_folder TEXT,
			next_rule_id TEXT,
			grace_period_days INTEGER NOT NULL DEFAULT 7,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (next_rule_id) REFERENCES rules(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS verdicts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			account_id TEXT NOT NULL,
			message_id_header TEXT NOT NULL UNIQUE,
			subject TEXT,
			sender TEXT,
			sent_at TIMESTAMP,
			rule_id TEXT,
			status TEXT NOT NULL DEFAULT 'pending',
			destination_folder TEXT,
			expires_at TIMESTAMP,
			reason TEXT,
			confidence REAL,
			evaluated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
			acted_at TIMESTAMP,
			FOREIGN KEY (account_id) REFERENCES accounts(id),
			FOREIGN KEY (rule_id) REFERENCES rules(id) ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS activity_log (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			account_id TEXT,
			message_id_header TEXT,
			subject TEXT,
			sender TEXT,
			rule_name TEXT,
			destination TEXT,
			reason TEXT,
			confidence REAL,
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS llm_cache (
			message_id_header TEXT PRIMARY KEY,
			verdict JSON NOT NULL,
			cached_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS training_examples (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			category TEXT NOT NULL,
			subject TEXT,
			sender TEXT,
			body_snippet TEXT,
			label TEXT NOT NULL,
			source TEXT NOT NULL DEFAULT 'dashboard',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
		`CREATE TABLE IF NOT EXISTS categories (
			id TEXT PRIMARY KEY,
			name TEXT NOT NULL,
			folder_name TEXT NOT NULL,
			icon TEXT DEFAULT '',
			color TEXT DEFAULT '#888888',
			created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
		)`,
	}

	for _, m := range migrations {
		if _, err := d.db.Exec(m); err != nil {
			return fmt.Errorf("migration failed: %w\nSQL: %s", err, m)
		}
	}

	return nil
}
```

- [ ] **Step 4: Add sqlite dependency and run tests**

```bash
cd web && go get modernc.org/sqlite && go test ./internal/db/ -v
```

Expected: all 2 tests PASS

- [ ] **Step 5: Commit**

```bash
git add web/
git commit -m "feat: SQLite database layer with schema migrations"
```

---

## Task 3: Rules CRUD and Default Seeding

**Files:**
- Create: `web/internal/db/rules.go`
- Create: `web/internal/db/rules_test.go`
- Create: `web/internal/rules/defaults.go`

- [ ] **Step 1: Write rules CRUD tests**

Create `web/internal/db/rules_test.go`:

```go
package db

import (
	"path/filepath"
	"testing"
)

func openTestDB(t *testing.T) *DB {
	t.Helper()
	path := filepath.Join(t.TempDir(), "test.db")
	d, err := Open(path)
	if err != nil {
		t.Fatalf("Open() error: %v", err)
	}
	t.Cleanup(func() { d.Close() })
	return d
}

func TestRulesGetEmpty(t *testing.T) {
	d := openTestDB(t)
	rules, err := d.GetRules()
	if err != nil {
		t.Fatalf("GetRules() error: %v", err)
	}
	if len(rules) != 0 {
		t.Errorf("expected 0 rules, got %d", len(rules))
	}
}

func TestRulesSaveAndGet(t *testing.T) {
	d := openTestDB(t)

	rule := Rule{
		ID:       "test-rule-1",
		Name:     "Test Rule",
		Enabled:  true,
		Priority: 10,
		Builtin:  false,
		MatchConfig: MatchConfig{
			SenderPatterns:  []string{"*@example.com"},
			SubjectPatterns: []string{"*test*"},
		},
		ExpirationConfig: ExpirationConfig{
			Type:  "ttl",
			Hours: 24,
		},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   7,
	}

	if err := d.SaveRule(rule); err != nil {
		t.Fatalf("SaveRule() error: %v", err)
	}

	got, err := d.GetRule("test-rule-1")
	if err != nil {
		t.Fatalf("GetRule() error: %v", err)
	}
	if got.Name != "Test Rule" {
		t.Errorf("name = %q, want %q", got.Name, "Test Rule")
	}
	if len(got.MatchConfig.SenderPatterns) != 1 {
		t.Errorf("sender patterns = %d, want 1", len(got.MatchConfig.SenderPatterns))
	}
	if got.ExpirationConfig.Type != "ttl" {
		t.Errorf("expiration type = %q, want %q", got.ExpirationConfig.Type, "ttl")
	}
}

func TestRulesGetSortedByPriority(t *testing.T) {
	d := openTestDB(t)

	d.SaveRule(Rule{ID: "low", Name: "Low Priority", Priority: 100, Enabled: true, Action: "move", GracePeriodDays: 7})
	d.SaveRule(Rule{ID: "high", Name: "High Priority", Priority: 1, Enabled: true, Action: "move", GracePeriodDays: 7})
	d.SaveRule(Rule{ID: "mid", Name: "Mid Priority", Priority: 50, Enabled: true, Action: "move", GracePeriodDays: 7})

	rules, err := d.GetRules()
	if err != nil {
		t.Fatalf("GetRules() error: %v", err)
	}
	if len(rules) != 3 {
		t.Fatalf("expected 3 rules, got %d", len(rules))
	}
	if rules[0].ID != "high" {
		t.Errorf("first rule = %q, want %q", rules[0].ID, "high")
	}
	if rules[2].ID != "low" {
		t.Errorf("last rule = %q, want %q", rules[2].ID, "low")
	}
}

func TestRulesDelete(t *testing.T) {
	d := openTestDB(t)
	d.SaveRule(Rule{ID: "del-me", Name: "Delete Me", Priority: 10, Enabled: true, Action: "move", GracePeriodDays: 7})

	if err := d.DeleteRule("del-me"); err != nil {
		t.Fatalf("DeleteRule() error: %v", err)
	}

	got, err := d.GetRule("del-me")
	if err != nil {
		t.Fatalf("GetRule() error: %v", err)
	}
	if got != nil {
		t.Error("expected nil after delete")
	}
}

func TestRulesUpdate(t *testing.T) {
	d := openTestDB(t)
	d.SaveRule(Rule{ID: "upd", Name: "Original", Priority: 10, Enabled: true, Action: "move", GracePeriodDays: 7})
	d.SaveRule(Rule{ID: "upd", Name: "Updated", Priority: 20, Enabled: false, Action: "tag", GracePeriodDays: 3})

	got, err := d.GetRule("upd")
	if err != nil {
		t.Fatalf("GetRule() error: %v", err)
	}
	if got.Name != "Updated" {
		t.Errorf("name = %q, want %q", got.Name, "Updated")
	}
	if got.Priority != 20 {
		t.Errorf("priority = %d, want 20", got.Priority)
	}
	if got.Enabled {
		t.Error("expected enabled=false")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/db/ -v -run TestRules`
Expected: FAIL — `Rule` type not defined

- [ ] **Step 3: Implement rules CRUD**

Create `web/internal/db/rules.go`:

```go
package db

import (
	"encoding/json"
	"fmt"
)

type Rule struct {
	ID                string           `json:"id"`
	Name              string           `json:"name"`
	Enabled           bool             `json:"enabled"`
	Priority          int              `json:"priority"`
	Builtin           bool             `json:"builtin"`
	MatchConfig       MatchConfig      `json:"matchConfig"`
	ExpirationConfig  ExpirationConfig `json:"expirationConfig"`
	Action            string           `json:"action"`
	DestinationFolder string           `json:"destinationFolder"`
	NextRuleID        *string          `json:"nextRuleId,omitempty"`
	GracePeriodDays   int              `json:"gracePeriodDays"`
}

type MatchConfig struct {
	SenderPatterns  []string `json:"senderPatterns,omitempty"`
	SubjectPatterns []string `json:"subjectPatterns,omitempty"`
	Folders         []string `json:"folders,omitempty"`
	HeaderMatch     map[string]string `json:"headerMatch,omitempty"`
}

type ExpirationConfig struct {
	Type     string `json:"type"`
	Hours    int    `json:"hours,omitempty"`
	Pattern  string `json:"pattern,omitempty"`
	Prompt   string `json:"prompt,omitempty"`
	Category string `json:"category,omitempty"`
}

func (d *DB) GetRules() ([]Rule, error) {
	rows, err := d.db.Query(`
		SELECT id, name, enabled, priority, builtin, match_config, expiration_config,
		       action, destination_folder, next_rule_id, grace_period_days
		FROM rules ORDER BY priority ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("query rules: %w", err)
	}
	defer rows.Close()

	var rules []Rule
	for rows.Next() {
		var r Rule
		var matchJSON, expJSON string
		var destFolder, nextRuleID *string

		err := rows.Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority, &r.Builtin,
			&matchJSON, &expJSON, &r.Action, &destFolder, &nextRuleID, &r.GracePeriodDays)
		if err != nil {
			return nil, fmt.Errorf("scan rule: %w", err)
		}

		json.Unmarshal([]byte(matchJSON), &r.MatchConfig)
		json.Unmarshal([]byte(expJSON), &r.ExpirationConfig)
		if destFolder != nil {
			r.DestinationFolder = *destFolder
		}
		r.NextRuleID = nextRuleID

		rules = append(rules, r)
	}
	return rules, rows.Err()
}

func (d *DB) GetRule(id string) (*Rule, error) {
	var r Rule
	var matchJSON, expJSON string
	var destFolder, nextRuleID *string

	err := d.db.QueryRow(`
		SELECT id, name, enabled, priority, builtin, match_config, expiration_config,
		       action, destination_folder, next_rule_id, grace_period_days
		FROM rules WHERE id = ?
	`, id).Scan(&r.ID, &r.Name, &r.Enabled, &r.Priority, &r.Builtin,
		&matchJSON, &expJSON, &r.Action, &destFolder, &nextRuleID, &r.GracePeriodDays)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, fmt.Errorf("query rule: %w", err)
	}

	json.Unmarshal([]byte(matchJSON), &r.MatchConfig)
	json.Unmarshal([]byte(expJSON), &r.ExpirationConfig)
	if destFolder != nil {
		r.DestinationFolder = *destFolder
	}
	r.NextRuleID = nextRuleID

	return &r, nil
}

func (d *DB) SaveRule(rule Rule) error {
	matchJSON, err := json.Marshal(rule.MatchConfig)
	if err != nil {
		return fmt.Errorf("marshal match config: %w", err)
	}
	expJSON, err := json.Marshal(rule.ExpirationConfig)
	if err != nil {
		return fmt.Errorf("marshal expiration config: %w", err)
	}

	var destFolder *string
	if rule.DestinationFolder != "" {
		destFolder = &rule.DestinationFolder
	}

	_, err = d.db.Exec(`
		INSERT INTO rules (id, name, enabled, priority, builtin, match_config, expiration_config,
		                   action, destination_folder, next_rule_id, grace_period_days, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, enabled=excluded.enabled, priority=excluded.priority,
			builtin=excluded.builtin, match_config=excluded.match_config,
			expiration_config=excluded.expiration_config, action=excluded.action,
			destination_folder=excluded.destination_folder, next_rule_id=excluded.next_rule_id,
			grace_period_days=excluded.grace_period_days, updated_at=CURRENT_TIMESTAMP
	`, rule.ID, rule.Name, rule.Enabled, rule.Priority, rule.Builtin,
		string(matchJSON), string(expJSON), rule.Action, destFolder, rule.NextRuleID, rule.GracePeriodDays)

	return err
}

func (d *DB) DeleteRule(id string) error {
	_, err := d.db.Exec("DELETE FROM rules WHERE id = ?", id)
	return err
}

func (d *DB) GetEnabledRules() ([]Rule, error) {
	rules, err := d.GetRules()
	if err != nil {
		return nil, err
	}
	var enabled []Rule
	for _, r := range rules {
		if r.Enabled {
			enabled = append(enabled, r)
		}
	}
	return enabled, nil
}
```

- [ ] **Step 4: Run tests**

Run: `cd web && go test ./internal/db/ -v -run TestRules`
Expected: all 5 tests PASS

- [ ] **Step 5: Port default rules**

Create `web/internal/rules/defaults.go`:

```go
package rules

import "github.com/kraftbj/mailreaper/internal/db"

var DefaultRules = []db.Rule{
	{
		ID: "builtin-otp-codes", Name: "One-time codes & verification emails",
		Enabled: true, Priority: 10, Builtin: true,
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{
				"*verification code*", "*verify your*", "*one-time*", "*OTP*",
				"*login code*", "*security code*", "*confirm your email*",
				"*authentication code*", "*two-factor*", "*2FA*", "*sign-in code*",
				"*passcode*", "*your * code is*", "*your * code:*", "*your code is*",
				"*your code:*", "*confirmation code*", "*access code*",
				"*temporary password*", "*magic link*", "*sign in to*", "*log in to*",
			},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "ttl", Hours: 1},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   1,
	},
	{
		ID: "builtin-shipping-delivered", Name: "Package delivery confirmations",
		Enabled: true, Priority: 20, Builtin: true,
		MatchConfig: db.MatchConfig{
			SenderPatterns:  []string{"*@ups.com", "*@fedex.com", "*@usps.com", "*@dhl.com", "*@amazonses.com", "*@amazon.com"},
			SubjectPatterns: []string{"*delivered*", "*has been delivered*", "*was delivered*"},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "ttl", Hours: 72},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   7,
	},
	{
		ID: "builtin-transit-alerts", Name: "Transit delay & service alerts",
		Enabled: false, Priority: 15, Builtin: true,
		MatchConfig: db.MatchConfig{
			SenderPatterns:  []string{"*@capmetro.org", "*@transitapp.com", "*@metro.net", "*@mta.info", "*@bart.gov"},
			SubjectPatterns: []string{"*delay*", "*service alert*", "*disruption*", "*suspended*", "*detour*"},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "ttl", Hours: 2},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   1,
	},
	{
		ID: "builtin-calendar-reminders", Name: "Calendar event reminders",
		Enabled: true, Priority: 25, Builtin: true,
		MatchConfig: db.MatchConfig{
			SenderPatterns:  []string{"*calendar-notification*@google.com", "*@calendly.com"},
			SubjectPatterns: []string{"*reminder:*", "*starts in*", "*upcoming event*"},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "ttl", Hours: 24},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   1,
	},
	{
		ID: "builtin-expires-header", Name: "Emails with Expires header (RFC standard)",
		Enabled: true, Priority: 1, Builtin: true,
		MatchConfig:       db.MatchConfig{},
		ExpirationConfig:  db.ExpirationConfig{Type: "header"},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   3,
	},
	{
		ID: "builtin-llm-promo-scanner", Name: "AI: Scan promotions for deadlines",
		Enabled: false, Priority: 100, Builtin: true,
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{
				"*sale*", "*% off*", "*discount*", "*limited time*", "*ends*",
				"*expires*", "*last chance*", "*final hours*", "*today only*",
				"*flash*", "*clearance*", "*coupon*", "*deal*",
			},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "llm"},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   3,
	},
	{
		ID: "builtin-receipts", Name: "Receipts & statements -> Paper-Trail",
		Enabled: false, Priority: 30, Builtin: true,
		MatchConfig: db.MatchConfig{
			SenderPatterns: []string{
				"*@paypal.com", "*@venmo.com", "*@square.com", "*@stripe.com",
				"*@shopify.com", "*@toast-restaurant.com", "*@amazonses.com",
				"*@apple.com", "*@amazon.com", "*@google.com", "*@microsoft.com",
				"*receipt*@*", "*billing*@*", "*invoice*@*",
			},
			SubjectPatterns: []string{
				"*receipt*", "*invoice*", "*payment*", "*order confirmation*",
				"*purchase confirmation*", "*billing statement*", "*your order*",
				"*transaction*", "*subscription*renew*", "*your * statement*",
			},
		},
		ExpirationConfig:  db.ExpirationConfig{Type: "classify"},
		Action:            "move",
		DestinationFolder: "Paper-Trail",
		GracePeriodDays:   0,
	},
	{
		ID: "builtin-llm-receipt-scanner", Name: "AI: Detect receipts & statements",
		Enabled: false, Priority: 200, Builtin: true,
		MatchConfig:       db.MatchConfig{},
		ExpirationConfig:  db.ExpirationConfig{Type: "llm-classify", Category: "receipt"},
		Action:            "move",
		DestinationFolder: "Paper-Trail",
		GracePeriodDays:   0,
	},
	{
		ID: "builtin-llm-catch-all", Name: "AI: Catch-all expiration scanner",
		Enabled: false, Priority: 999, Builtin: true,
		MatchConfig:       db.MatchConfig{},
		ExpirationConfig:  db.ExpirationConfig{Type: "llm"},
		Action:            "move",
		DestinationFolder: "Expired",
		GracePeriodDays:   7,
	},
}
```

- [ ] **Step 6: Add seeding function to db.go and test**

Add to `web/internal/db/db.go` (after `Close` method):

```go
func (d *DB) SeedDefaults(defaults []Rule) error {
	for _, rule := range defaults {
		existing, err := d.GetRule(rule.ID)
		if err != nil {
			return err
		}
		if existing == nil {
			if err := d.SaveRule(rule); err != nil {
				return fmt.Errorf("seed rule %s: %w", rule.ID, err)
			}
		}
	}
	return nil
}
```

Add test to `web/internal/db/db_test.go`:

```go
func TestSeedDefaults(t *testing.T) {
	d := openTestDB(t)

	defaults := []Rule{
		{ID: "builtin-a", Name: "Rule A", Priority: 10, Enabled: true, Builtin: true, Action: "move", GracePeriodDays: 7},
		{ID: "builtin-b", Name: "Rule B", Priority: 20, Enabled: true, Builtin: true, Action: "move", GracePeriodDays: 3},
	}

	if err := d.SeedDefaults(defaults); err != nil {
		t.Fatalf("SeedDefaults() error: %v", err)
	}

	rules, _ := d.GetRules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules, got %d", len(rules))
	}

	// Seeding again should not duplicate
	if err := d.SeedDefaults(defaults); err != nil {
		t.Fatalf("SeedDefaults() second call error: %v", err)
	}
	rules, _ = d.GetRules()
	if len(rules) != 2 {
		t.Fatalf("expected 2 rules after re-seed, got %d", len(rules))
	}
}
```

- [ ] **Step 7: Run all db tests**

Run: `cd web && go test ./internal/db/ -v`
Expected: all tests PASS

- [ ] **Step 8: Commit**

```bash
git add web/
git commit -m "feat: rules CRUD, default rule definitions, and seeding"
```

---

## Task 4: Verdicts, Activity Log, LLM Cache, Training, and Categories CRUD

**Files:**
- Create: `web/internal/db/verdicts.go`
- Create: `web/internal/db/activity.go`
- Create: `web/internal/db/llmcache.go`
- Create: `web/internal/db/training.go`
- Create: `web/internal/db/categories.go`
- Create: `web/internal/db/verdicts_test.go`
- Create: `web/internal/db/activity_test.go`
- Create: `web/internal/db/llmcache_test.go`
- Create: `web/internal/db/training_test.go`
- Create: `web/internal/db/categories_test.go`

This task covers five small CRUD modules. Each follows the same TDD pattern: write test, verify fail, implement, verify pass. I'll list them together since the pattern is identical.

- [ ] **Step 1: Write verdicts tests**

Create `web/internal/db/verdicts_test.go`:

```go
package db

import (
	"testing"
	"time"
)

func TestVerdictSaveAndGet(t *testing.T) {
	d := openTestDB(t)
	d.db.Exec("INSERT INTO accounts (id, name) VALUES ('acct1', 'Test')")

	v := Verdict{
		AccountID:       "acct1",
		MessageIDHeader: "<msg1@example.com>",
		Subject:         "Test Subject",
		Sender:          "sender@example.com",
		SentAt:          time.Now(),
		RuleID:          nil,
		Status:          "executed",
		DestinationFolder: "Expired",
		Reason:          "TTL expired",
		Confidence:      1.0,
	}

	if err := d.SaveVerdict(v); err != nil {
		t.Fatalf("SaveVerdict() error: %v", err)
	}

	got, err := d.GetVerdictByMessageID("<msg1@example.com>")
	if err != nil {
		t.Fatalf("GetVerdictByMessageID() error: %v", err)
	}
	if got == nil {
		t.Fatal("expected verdict, got nil")
	}
	if got.Status != "executed" {
		t.Errorf("status = %q, want %q", got.Status, "executed")
	}
}

func TestGetPendingVerdicts(t *testing.T) {
	d := openTestDB(t)
	d.db.Exec("INSERT INTO accounts (id, name) VALUES ('acct1', 'Test')")

	d.SaveVerdict(Verdict{AccountID: "acct1", MessageIDHeader: "<p1@ex>", Status: "pending", Confidence: 0.5})
	d.SaveVerdict(Verdict{AccountID: "acct1", MessageIDHeader: "<p2@ex>", Status: "pending", Confidence: 0.6})
	d.SaveVerdict(Verdict{AccountID: "acct1", MessageIDHeader: "<e1@ex>", Status: "executed", Confidence: 1.0})

	pending, err := d.GetPendingVerdicts()
	if err != nil {
		t.Fatalf("GetPendingVerdicts() error: %v", err)
	}
	if len(pending) != 2 {
		t.Errorf("expected 2 pending, got %d", len(pending))
	}
}

func TestUpdateVerdictStatus(t *testing.T) {
	d := openTestDB(t)
	d.db.Exec("INSERT INTO accounts (id, name) VALUES ('acct1', 'Test')")
	d.SaveVerdict(Verdict{AccountID: "acct1", MessageIDHeader: "<u1@ex>", Status: "pending"})

	if err := d.UpdateVerdictStatus("<u1@ex>", "approved"); err != nil {
		t.Fatalf("UpdateVerdictStatus() error: %v", err)
	}

	got, _ := d.GetVerdictByMessageID("<u1@ex>")
	if got.Status != "approved" {
		t.Errorf("status = %q, want %q", got.Status, "approved")
	}
}
```

- [ ] **Step 2: Implement verdicts CRUD**

Create `web/internal/db/verdicts.go`:

```go
package db

import (
	"database/sql"
	"fmt"
	"time"
)

type Verdict struct {
	ID                int       `json:"id"`
	AccountID         string    `json:"accountId"`
	MessageIDHeader   string    `json:"messageIdHeader"`
	Subject           string    `json:"subject"`
	Sender            string    `json:"sender"`
	SentAt            time.Time `json:"sentAt"`
	RuleID            *string   `json:"ruleId,omitempty"`
	Status            string    `json:"status"`
	DestinationFolder string    `json:"destinationFolder"`
	ExpiresAt         *time.Time `json:"expiresAt,omitempty"`
	Reason            string    `json:"reason"`
	Confidence        float64   `json:"confidence"`
	EvaluatedAt       time.Time `json:"evaluatedAt"`
	ActedAt           *time.Time `json:"actedAt,omitempty"`
}

func (d *DB) SaveVerdict(v Verdict) error {
	_, err := d.db.Exec(`
		INSERT INTO verdicts (account_id, message_id_header, subject, sender, sent_at,
		                      rule_id, status, destination_folder, expires_at, reason, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(message_id_header) DO UPDATE SET
			status=excluded.status, reason=excluded.reason, confidence=excluded.confidence,
			rule_id=excluded.rule_id, destination_folder=excluded.destination_folder,
			expires_at=excluded.expires_at, evaluated_at=CURRENT_TIMESTAMP
	`, v.AccountID, v.MessageIDHeader, v.Subject, v.Sender, v.SentAt,
		v.RuleID, v.Status, v.DestinationFolder, v.ExpiresAt, v.Reason, v.Confidence)
	return err
}

func (d *DB) GetVerdictByMessageID(messageIDHeader string) (*Verdict, error) {
	var v Verdict
	var ruleID, destFolder, reason sql.NullString
	var expiresAt, actedAt sql.NullTime

	err := d.db.QueryRow(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason, confidence,
		       evaluated_at, acted_at
		FROM verdicts WHERE message_id_header = ?
	`, messageIDHeader).Scan(&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject,
		&v.Sender, &v.SentAt, &ruleID, &v.Status, &destFolder, &expiresAt,
		&reason, &v.Confidence, &v.EvaluatedAt, &actedAt)
	if err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, fmt.Errorf("query verdict: %w", err)
	}

	if ruleID.Valid { v.RuleID = &ruleID.String }
	if destFolder.Valid { v.DestinationFolder = destFolder.String }
	if expiresAt.Valid { v.ExpiresAt = &expiresAt.Time }
	if reason.Valid { v.Reason = reason.String }
	if actedAt.Valid { v.ActedAt = &actedAt.Time }

	return &v, nil
}

func (d *DB) GetPendingVerdicts() ([]Verdict, error) {
	rows, err := d.db.Query(`
		SELECT id, account_id, message_id_header, subject, sender, sent_at,
		       rule_id, status, destination_folder, expires_at, reason, confidence,
		       evaluated_at, acted_at
		FROM verdicts WHERE status = 'pending' ORDER BY evaluated_at DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanVerdicts(rows)
}

func (d *DB) UpdateVerdictStatus(messageIDHeader, status string) error {
	actedAt := sql.NullTime{}
	if status == "approved" || status == "rejected" || status == "corrected" {
		actedAt = sql.NullTime{Time: time.Now(), Valid: true}
	}
	_, err := d.db.Exec(`
		UPDATE verdicts SET status = ?, acted_at = ? WHERE message_id_header = ?
	`, status, actedAt, messageIDHeader)
	return err
}

func (d *DB) GetExecutedMessageIDs(accountID string) ([]string, error) {
	rows, err := d.db.Query(`
		SELECT message_id_header FROM verdicts
		WHERE account_id = ? AND status = 'executed'
	`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

func scanVerdicts(rows *sql.Rows) ([]Verdict, error) {
	var verdicts []Verdict
	for rows.Next() {
		var v Verdict
		var ruleID, destFolder, reason sql.NullString
		var expiresAt, actedAt sql.NullTime

		err := rows.Scan(&v.ID, &v.AccountID, &v.MessageIDHeader, &v.Subject,
			&v.Sender, &v.SentAt, &ruleID, &v.Status, &destFolder, &expiresAt,
			&reason, &v.Confidence, &v.EvaluatedAt, &actedAt)
		if err != nil {
			return nil, err
		}

		if ruleID.Valid { v.RuleID = &ruleID.String }
		if destFolder.Valid { v.DestinationFolder = destFolder.String }
		if expiresAt.Valid { v.ExpiresAt = &expiresAt.Time }
		if reason.Valid { v.Reason = reason.String }
		if actedAt.Valid { v.ActedAt = &actedAt.Time }

		verdicts = append(verdicts, v)
	}
	return verdicts, rows.Err()
}
```

- [ ] **Step 3: Implement activity log, LLM cache, training, and categories**

Create `web/internal/db/activity.go`:

```go
package db

import (
	"fmt"
	"time"
)

type ActivityEntry struct {
	ID              int       `json:"id"`
	Type            string    `json:"type"`
	AccountID       string    `json:"accountId"`
	MessageIDHeader string    `json:"messageIdHeader"`
	Subject         string    `json:"subject"`
	Sender          string    `json:"sender"`
	RuleName        string    `json:"ruleName"`
	Destination     string    `json:"destination"`
	Reason          string    `json:"reason"`
	Confidence      float64   `json:"confidence"`
	CreatedAt       time.Time `json:"createdAt"`
}

func (d *DB) LogActivity(entry ActivityEntry) error {
	_, err := d.db.Exec(`
		INSERT INTO activity_log (type, account_id, message_id_header, subject, sender,
		                          rule_name, destination, reason, confidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, entry.Type, entry.AccountID, entry.MessageIDHeader, entry.Subject,
		entry.Sender, entry.RuleName, entry.Destination, entry.Reason, entry.Confidence)
	return err
}

func (d *DB) GetActivityLog(limit int) ([]ActivityEntry, error) {
	rows, err := d.db.Query(`
		SELECT id, type, account_id, message_id_header, subject, sender,
		       rule_name, destination, reason, confidence, created_at
		FROM activity_log ORDER BY created_at DESC LIMIT ?
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("query activity: %w", err)
	}
	defer rows.Close()

	var entries []ActivityEntry
	for rows.Next() {
		var e ActivityEntry
		if err := rows.Scan(&e.ID, &e.Type, &e.AccountID, &e.MessageIDHeader,
			&e.Subject, &e.Sender, &e.RuleName, &e.Destination, &e.Reason,
			&e.Confidence, &e.CreatedAt); err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func (d *DB) ClearActivityLog() error {
	_, err := d.db.Exec("DELETE FROM activity_log")
	return err
}
```

Create `web/internal/db/llmcache.go`:

```go
package db

import (
	"encoding/json"
	"time"
)

const (
	CacheTTLExpired      = 24 * time.Hour
	CacheTTLNotSensitive = 7 * 24 * time.Hour
	CacheTTLError        = 10 * time.Minute
)

type CachedVerdict struct {
	IsTimeSensitive bool    `json:"isTimeSensitive,omitempty"`
	Expired         bool    `json:"expired,omitempty"`
	Classified      bool    `json:"classified,omitempty"`
	ExpiresAt       string  `json:"expiresAt,omitempty"`
	Reason          string  `json:"reason,omitempty"`
	Confidence      float64 `json:"confidence,omitempty"`
	Error           string  `json:"error,omitempty"`
}

func (d *DB) GetCachedVerdict(messageIDHeader string) (*CachedVerdict, error) {
	var verdictJSON string
	var cachedAt time.Time

	err := d.db.QueryRow(`
		SELECT verdict, cached_at FROM llm_cache WHERE message_id_header = ?
	`, messageIDHeader).Scan(&verdictJSON, &cachedAt)
	if err != nil {
		if err.Error() == "sql: no rows in result set" {
			return nil, nil
		}
		return nil, err
	}

	var cv CachedVerdict
	if err := json.Unmarshal([]byte(verdictJSON), &cv); err != nil {
		return nil, err
	}

	// Check TTL
	age := time.Since(cachedAt)
	var ttl time.Duration
	switch {
	case cv.Error != "":
		ttl = CacheTTLError
	case cv.Expired || cv.Classified:
		ttl = CacheTTLExpired
	default:
		ttl = CacheTTLNotSensitive
	}

	if age > ttl {
		return nil, nil
	}

	return &cv, nil
}

func (d *DB) SetCachedVerdict(messageIDHeader string, cv CachedVerdict) error {
	verdictJSON, err := json.Marshal(cv)
	if err != nil {
		return err
	}
	_, err = d.db.Exec(`
		INSERT INTO llm_cache (message_id_header, verdict, cached_at)
		VALUES (?, ?, CURRENT_TIMESTAMP)
		ON CONFLICT(message_id_header) DO UPDATE SET
			verdict=excluded.verdict, cached_at=CURRENT_TIMESTAMP
	`, messageIDHeader, string(verdictJSON))
	return err
}

func (d *DB) RemoveCachedVerdict(messageIDHeader string) error {
	_, err := d.db.Exec("DELETE FROM llm_cache WHERE message_id_header = ?", messageIDHeader)
	return err
}

func (d *DB) ClearLLMCache() error {
	_, err := d.db.Exec("DELETE FROM llm_cache")
	return err
}
```

Create `web/internal/db/training.go`:

```go
package db

import (
	"fmt"
	"time"
)

const MaxTrainingExamples = 100

type TrainingExample struct {
	ID          int       `json:"id"`
	Category    string    `json:"category"`
	Subject     string    `json:"subject"`
	Sender      string    `json:"sender"`
	BodySnippet string    `json:"bodySnippet"`
	Label       string    `json:"label"`
	Source      string    `json:"source"`
	CreatedAt   time.Time `json:"createdAt"`
}

func (d *DB) AddTrainingExample(ex TrainingExample) error {
	_, err := d.db.Exec(`
		INSERT INTO training_examples (category, subject, sender, body_snippet, label, source)
		VALUES (?, ?, ?, ?, ?, ?)
	`, ex.Category, ex.Subject, ex.Sender, ex.BodySnippet, ex.Label, ex.Source)
	if err != nil {
		return err
	}

	// Trim to max
	_, err = d.db.Exec(`
		DELETE FROM training_examples WHERE id IN (
			SELECT id FROM training_examples ORDER BY created_at DESC
			LIMIT -1 OFFSET ?
		)
	`, MaxTrainingExamples)
	return err
}

func (d *DB) GetTrainingExamples(category string) ([]TrainingExample, error) {
	query := "SELECT id, category, subject, sender, body_snippet, label, source, created_at FROM training_examples"
	var args []interface{}
	if category != "" {
		query += " WHERE category = ?"
		args = append(args, category)
	}
	query += " ORDER BY created_at DESC"

	rows, err := d.db.Query(query, args...)
	if err != nil {
		return nil, fmt.Errorf("query training: %w", err)
	}
	defer rows.Close()

	var examples []TrainingExample
	for rows.Next() {
		var ex TrainingExample
		if err := rows.Scan(&ex.ID, &ex.Category, &ex.Subject, &ex.Sender,
			&ex.BodySnippet, &ex.Label, &ex.Source, &ex.CreatedAt); err != nil {
			return nil, err
		}
		examples = append(examples, ex)
	}
	return examples, rows.Err()
}

func (d *DB) DeleteTrainingExample(id int) error {
	_, err := d.db.Exec("DELETE FROM training_examples WHERE id = ?", id)
	return err
}

func (d *DB) ClearTrainingExamples(category string) error {
	if category == "" {
		_, err := d.db.Exec("DELETE FROM training_examples")
		return err
	}
	_, err := d.db.Exec("DELETE FROM training_examples WHERE category = ?", category)
	return err
}
```

Create `web/internal/db/categories.go`:

```go
package db

import (
	"fmt"
	"time"
)

type Category struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	FolderName string    `json:"folderName"`
	Icon       string    `json:"icon"`
	Color      string    `json:"color"`
	CreatedAt  time.Time `json:"createdAt"`
}

func (d *DB) GetCategories() ([]Category, error) {
	rows, err := d.db.Query(`
		SELECT id, name, folder_name, icon, color, created_at
		FROM categories ORDER BY name
	`)
	if err != nil {
		return nil, fmt.Errorf("query categories: %w", err)
	}
	defer rows.Close()

	var cats []Category
	for rows.Next() {
		var c Category
		if err := rows.Scan(&c.ID, &c.Name, &c.FolderName, &c.Icon, &c.Color, &c.CreatedAt); err != nil {
			return nil, err
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

func (d *DB) SaveCategory(c Category) error {
	_, err := d.db.Exec(`
		INSERT INTO categories (id, name, folder_name, icon, color)
		VALUES (?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name=excluded.name, folder_name=excluded.folder_name,
			icon=excluded.icon, color=excluded.color
	`, c.ID, c.Name, c.FolderName, c.Icon, c.Color)
	return err
}

func (d *DB) DeleteCategory(id string) error {
	_, err := d.db.Exec("DELETE FROM categories WHERE id = ?", id)
	return err
}
```

- [ ] **Step 4: Write tests for activity, LLM cache, training, and categories**

Create `web/internal/db/activity_test.go`:

```go
package db

import "testing"

func TestActivityLogAndGet(t *testing.T) {
	d := openTestDB(t)

	d.LogActivity(ActivityEntry{Type: "moved", Subject: "Test 1", RuleName: "OTP"})
	d.LogActivity(ActivityEntry{Type: "moved", Subject: "Test 2", RuleName: "TTL"})

	entries, err := d.GetActivityLog(10)
	if err != nil {
		t.Fatalf("GetActivityLog() error: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("expected 2 entries, got %d", len(entries))
	}
	// Most recent first
	if entries[0].Subject != "Test 2" {
		t.Errorf("first entry subject = %q, want %q", entries[0].Subject, "Test 2")
	}
}
```

Create `web/internal/db/llmcache_test.go`:

```go
package db

import "testing"

func TestLLMCacheSetAndGet(t *testing.T) {
	d := openTestDB(t)

	cv := CachedVerdict{Expired: true, Reason: "TTL", Confidence: 0.9}
	if err := d.SetCachedVerdict("<cache1@ex>", cv); err != nil {
		t.Fatalf("SetCachedVerdict() error: %v", err)
	}

	got, err := d.GetCachedVerdict("<cache1@ex>")
	if err != nil {
		t.Fatalf("GetCachedVerdict() error: %v", err)
	}
	if got == nil {
		t.Fatal("expected cached verdict, got nil")
	}
	if !got.Expired {
		t.Error("expected expired=true")
	}
}

func TestLLMCacheMiss(t *testing.T) {
	d := openTestDB(t)

	got, err := d.GetCachedVerdict("<nonexistent@ex>")
	if err != nil {
		t.Fatalf("GetCachedVerdict() error: %v", err)
	}
	if got != nil {
		t.Error("expected nil for cache miss")
	}
}

func TestLLMCacheRemove(t *testing.T) {
	d := openTestDB(t)
	d.SetCachedVerdict("<rm@ex>", CachedVerdict{Expired: true})
	d.RemoveCachedVerdict("<rm@ex>")

	got, _ := d.GetCachedVerdict("<rm@ex>")
	if got != nil {
		t.Error("expected nil after remove")
	}
}
```

Create `web/internal/db/training_test.go`:

```go
package db

import "testing"

func TestTrainingAddAndGet(t *testing.T) {
	d := openTestDB(t)

	d.AddTrainingExample(TrainingExample{Category: "expiry", Subject: "OTP Code", Sender: "noreply@github.com", Label: "expired", Source: "correction"})
	d.AddTrainingExample(TrainingExample{Category: "receipt", Subject: "Payment", Sender: "receipts@stripe.com", Label: "receipt", Source: "dashboard"})

	all, err := d.GetTrainingExamples("")
	if err != nil {
		t.Fatalf("GetTrainingExamples() error: %v", err)
	}
	if len(all) != 2 {
		t.Errorf("expected 2 examples, got %d", len(all))
	}

	expiry, _ := d.GetTrainingExamples("expiry")
	if len(expiry) != 1 {
		t.Errorf("expected 1 expiry example, got %d", len(expiry))
	}
}
```

Create `web/internal/db/categories_test.go`:

```go
package db

import "testing"

func TestCategoriesSaveAndGet(t *testing.T) {
	d := openTestDB(t)

	d.SaveCategory(Category{ID: "paper-trail", Name: "Paper-Trail", FolderName: "Paper-Trail", Icon: "📋", Color: "#9b59b6"})
	d.SaveCategory(Category{ID: "newsletters", Name: "Newsletters", FolderName: "Newsletters", Icon: "📰", Color: "#3498db"})

	cats, err := d.GetCategories()
	if err != nil {
		t.Fatalf("GetCategories() error: %v", err)
	}
	if len(cats) != 2 {
		t.Errorf("expected 2 categories, got %d", len(cats))
	}
}

func TestCategoryDelete(t *testing.T) {
	d := openTestDB(t)
	d.SaveCategory(Category{ID: "del", Name: "Delete Me", FolderName: "Del"})
	d.DeleteCategory("del")

	cats, _ := d.GetCategories()
	if len(cats) != 0 {
		t.Errorf("expected 0 categories after delete, got %d", len(cats))
	}
}
```

- [ ] **Step 5: Run all db tests**

Run: `cd web && go test ./internal/db/ -v`
Expected: all tests PASS

- [ ] **Step 6: Commit**

```bash
git add web/
git commit -m "feat: verdicts, activity log, LLM cache, training, and categories CRUD"
```

---

## Task 5: Glob Matching and Rule Engine

**Files:**
- Create: `web/internal/rules/glob.go`
- Create: `web/internal/rules/glob_test.go`
- Create: `web/internal/rules/engine.go`
- Create: `web/internal/rules/engine_test.go`

- [ ] **Step 1: Write glob matching tests**

Create `web/internal/rules/glob_test.go`:

```go
package rules

import "testing"

func TestGlobMatch(t *testing.T) {
	tests := []struct {
		str, pattern string
		want         bool
	}{
		{"hello@example.com", "*@example.com", true},
		{"hello@other.com", "*@example.com", false},
		{"noreply@github.com", "*@github.com", true},
		{"test subject line", "*subject*", true},
		{"no match here", "*subject*", false},
		{"a]b", "*]*", true},
		{"abc", "a?c", true},
		{"abbc", "a?c", false},
		{"", "*", true},
		{"anything", "*", true},
		{"exact", "exact", true},
	}

	for _, tt := range tests {
		got := GlobMatch(tt.str, tt.pattern)
		if got != tt.want {
			t.Errorf("GlobMatch(%q, %q) = %v, want %v", tt.str, tt.pattern, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/rules/ -v -run TestGlob`
Expected: FAIL

- [ ] **Step 3: Implement glob matching**

Create `web/internal/rules/glob.go`:

```go
package rules

import (
	"regexp"
	"strings"
)

// GlobMatch checks if str matches a glob pattern (supports * and ?).
// Patterns are anchored — must match the entire string.
func GlobMatch(str, pattern string) bool {
	str = strings.ToLower(str)
	pattern = strings.ToLower(pattern)

	// Escape regex special chars except * and ?
	var b strings.Builder
	b.WriteString("^")
	for _, ch := range pattern {
		switch ch {
		case '*':
			b.WriteString(".*")
		case '?':
			b.WriteString(".")
		case '.', '+', '^', '$', '{', '}', '(', ')', '|', '[', ']', '\\':
			b.WriteRune('\\')
			b.WriteRune(ch)
		default:
			b.WriteRune(ch)
		}
	}
	b.WriteString("$")

	re, err := regexp.Compile(b.String())
	if err != nil {
		return false
	}
	return re.MatchString(str)
}

// ExtractEmail extracts the email address from a "Display Name <email>" string.
func ExtractEmail(author string) string {
	start := strings.LastIndex(author, "<")
	end := strings.LastIndex(author, ">")
	if start >= 0 && end > start {
		return strings.ToLower(author[start+1 : end])
	}
	return strings.ToLower(author)
}
```

- [ ] **Step 4: Run glob tests**

Run: `cd web && go test ./internal/rules/ -v -run TestGlob`
Expected: PASS

- [ ] **Step 5: Write rule engine tests**

Create `web/internal/rules/engine_test.go`:

```go
package rules

import (
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

type testMessage struct {
	Author    string
	Subject   string
	Date      time.Time
	Folder    string
	MessageID string
	Headers   map[string][]string
	Body      string
}

func TestMatchesRuleSenderPattern(t *testing.T) {
	rule := db.Rule{
		MatchConfig: db.MatchConfig{
			SenderPatterns: []string{"*@github.com"},
		},
	}

	msg := &Message{Author: "noreply@github.com", Subject: "Your code"}
	if !MatchesRule(msg, rule) {
		t.Error("expected match for *@github.com sender")
	}

	msg2 := &Message{Author: "noreply@gitlab.com", Subject: "Your code"}
	if MatchesRule(msg2, rule) {
		t.Error("expected no match for gitlab sender")
	}
}

func TestMatchesRuleSubjectPattern(t *testing.T) {
	rule := db.Rule{
		MatchConfig: db.MatchConfig{
			SubjectPatterns: []string{"*verification code*"},
		},
	}

	msg := &Message{Subject: "Your verification code is 123456"}
	if !MatchesRule(msg, rule) {
		t.Error("expected match for verification code subject")
	}

	msg2 := &Message{Subject: "Meeting notes"}
	if MatchesRule(msg2, rule) {
		t.Error("expected no match for meeting notes subject")
	}
}

func TestMatchesRuleDisplayNameSender(t *testing.T) {
	rule := db.Rule{
		MatchConfig: db.MatchConfig{
			SenderPatterns: []string{"*@github.com"},
		},
	}

	msg := &Message{Author: "GitHub <noreply@github.com>"}
	if !MatchesRule(msg, rule) {
		t.Error("expected match when email is inside display name brackets")
	}
}

func TestMatchesRuleNoPatterns(t *testing.T) {
	rule := db.Rule{MatchConfig: db.MatchConfig{}}

	msg := &Message{Author: "anyone@anywhere.com", Subject: "Anything"}
	if !MatchesRule(msg, rule) {
		t.Error("rule with no patterns should match everything")
	}
}

func TestEvaluateTTL(t *testing.T) {
	rule := db.Rule{
		ID:   "ttl-rule",
		Name: "TTL test",
		ExpirationConfig: db.ExpirationConfig{Type: "ttl", Hours: 1},
	}

	// Message sent 2 hours ago — should be expired
	msg := &Message{
		Date:    time.Now().Add(-2 * time.Hour),
		Subject: "OTP code",
	}

	verdict := EvaluateTTL(msg, rule)
	if verdict == nil {
		t.Fatal("expected verdict, got nil")
	}
	if !verdict.Expired {
		t.Error("expected expired=true for message sent 2h ago with 1h TTL")
	}

	// Message sent 30 minutes ago — should NOT be expired
	msg2 := &Message{
		Date:    time.Now().Add(-30 * time.Minute),
		Subject: "Fresh OTP",
	}

	verdict2 := EvaluateTTL(msg2, rule)
	if verdict2 != nil {
		t.Error("expected nil verdict for fresh message")
	}
}

func TestEvaluateHeader(t *testing.T) {
	rule := db.Rule{
		ID:   "header-rule",
		Name: "Header test",
		ExpirationConfig: db.ExpirationConfig{Type: "header"},
	}

	// Message with past Expires header
	pastDate := time.Now().Add(-24 * time.Hour).Format(time.RFC1123Z)
	headers := map[string][]string{"expires": {pastDate}}

	verdict := EvaluateHeader(headers, rule)
	if verdict == nil {
		t.Fatal("expected verdict for expired header")
	}
	if !verdict.Expired {
		t.Error("expected expired=true")
	}

	// No header
	verdict2 := EvaluateHeader(map[string][]string{}, rule)
	if verdict2 != nil {
		t.Error("expected nil for missing header")
	}

	// Future header
	futureDate := time.Now().Add(24 * time.Hour).Format(time.RFC1123Z)
	verdict3 := EvaluateHeader(map[string][]string{"expires": {futureDate}}, rule)
	if verdict3 != nil {
		t.Error("expected nil for future header")
	}
}
```

- [ ] **Step 6: Run tests to verify they fail**

Run: `cd web && go test ./internal/rules/ -v -run "TestMatchesRule|TestEvaluate"`
Expected: FAIL

- [ ] **Step 7: Implement rule engine**

Create `web/internal/rules/engine.go`:

```go
package rules

import (
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/db"
)

// Message is a simplified representation of an email for rule evaluation.
type Message struct {
	Author    string
	Subject   string
	Date      time.Time
	Folder    string
	MessageID string
}

// RuleVerdict is the result of evaluating a single rule against a message.
type RuleVerdict struct {
	Expired    bool
	Classified bool
	Rule       db.Rule
	ExpiresAt  *time.Time
	Reason     string
	Confidence float64
}

// MatchesRule checks if a message matches a rule's static criteria.
func MatchesRule(msg *Message, rule db.Rule) bool {
	match := rule.MatchConfig

	// Sender patterns
	if len(match.SenderPatterns) > 0 {
		authorRaw := strings.ToLower(msg.Author)
		emailOnly := ExtractEmail(authorRaw)
		senderMatch := false
		for _, p := range match.SenderPatterns {
			if GlobMatch(authorRaw, p) || GlobMatch(emailOnly, p) {
				senderMatch = true
				break
			}
		}
		if !senderMatch {
			return false
		}
	}

	// Subject patterns
	if len(match.SubjectPatterns) > 0 {
		subject := strings.ToLower(msg.Subject)
		subjectMatch := false
		for _, p := range match.SubjectPatterns {
			if GlobMatch(subject, p) {
				subjectMatch = true
				break
			}
		}
		if !subjectMatch {
			return false
		}
	}

	// Folder filter
	if len(match.Folders) > 0 {
		folderMatch := false
		for _, f := range match.Folders {
			if f == msg.Folder {
				folderMatch = true
				break
			}
		}
		if !folderMatch {
			return false
		}
	}

	return true
}

// EvaluateTTL checks if a message has exceeded its TTL.
func EvaluateTTL(msg *Message, rule db.Rule) *RuleVerdict {
	ttl := time.Duration(rule.ExpirationConfig.Hours) * time.Hour
	expiresAt := msg.Date.Add(ttl)
	if time.Now().After(expiresAt) {
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &expiresAt,
			Reason:     fmt.Sprintf("TTL expired: %dh after send date", rule.ExpirationConfig.Hours),
			Confidence: 1.0,
		}
	}
	return nil
}

// EvaluateHeader checks the Expires header.
func EvaluateHeader(headers map[string][]string, rule db.Rule) *RuleVerdict {
	vals, ok := headers["expires"]
	if !ok || len(vals) == 0 {
		return nil
	}

	expiresAt, err := time.Parse(time.RFC1123Z, vals[0])
	if err != nil {
		// Try other common formats
		for _, layout := range []string{time.RFC1123, time.RFC850, time.RFC3339} {
			expiresAt, err = time.Parse(layout, vals[0])
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil
		}
	}

	if time.Now().After(expiresAt) {
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &expiresAt,
			Reason:     fmt.Sprintf("Expires header: %s", vals[0]),
			Confidence: 1.0,
		}
	}
	return nil
}

// EvaluateContentRegex checks body content against a regex pattern.
func EvaluateContentRegex(body string, rule db.Rule) *RuleVerdict {
	pattern := rule.ExpirationConfig.Pattern
	if pattern == "" || body == "" {
		return nil
	}

	re, err := regexp.Compile("(?i)" + pattern)
	if err != nil {
		return nil
	}

	// Truncate body to limit ReDoS exposure
	if len(body) > 50000 {
		body = body[:50000]
	}

	match := re.FindStringSubmatch(body)
	if match == nil || len(match) < 2 {
		return nil
	}

	expiresAt, err := time.Parse(time.RFC3339, match[1])
	if err != nil {
		// Try common date formats
		for _, layout := range []string{"2006-01-02", "01/02/2006", "January 2, 2006", time.RFC1123} {
			expiresAt, err = time.Parse(layout, match[1])
			if err == nil {
				break
			}
		}
		if err != nil {
			return nil
		}
	}

	if time.Now().After(expiresAt) {
		return &RuleVerdict{
			Expired:    true,
			Rule:       rule,
			ExpiresAt:  &expiresAt,
			Reason:     fmt.Sprintf("Content regex matched: %s", match[0]),
			Confidence: 0.9,
		}
	}
	return nil
}

// EvaluateClassify returns a classification verdict (always matches if rule matched).
func EvaluateClassify(rule db.Rule) *RuleVerdict {
	return &RuleVerdict{
		Classified: true,
		Rule:       rule,
		Reason:     rule.Name,
		Confidence: 1.0,
	}
}
```

- [ ] **Step 8: Run all rule tests**

Run: `cd web && go test ./internal/rules/ -v`
Expected: all tests PASS

- [ ] **Step 9: Commit**

```bash
git add web/
git commit -m "feat: rule engine with glob matching, TTL, header, regex, and classify evaluation"
```

---

## Task 6: LLM Adapter

**Files:**
- Create: `web/internal/llm/adapter.go`
- Create: `web/internal/llm/adapter_test.go`
- Create: `web/internal/llm/prompts.go`
- Create: `web/internal/llm/prompts_test.go`

- [ ] **Step 1: Write prompt builder tests**

Create `web/internal/llm/prompts_test.go`:

```go
package llm

import (
	"strings"
	"testing"
)

func TestBuildAnalysisPrompt(t *testing.T) {
	msg := MessageData{
		Sender:      "deals@store.com",
		Subject:     "Flash sale ends tonight!",
		SentDate:    "2026-04-08T10:00:00Z",
		BodySnippet: "Don't miss our 50% off sale, ending at midnight!",
	}

	system, user := BuildAnalysisPrompt(msg, nil)

	if !strings.Contains(system, "Decide if this email has expired") {
		t.Error("system prompt missing instruction")
	}
	if !strings.Contains(user, "Flash sale ends tonight!") {
		t.Error("user content missing subject")
	}
	if !strings.Contains(user, "deals@store.com") {
		t.Error("user content missing sender")
	}
}

func TestBuildAnalysisPromptWithExamples(t *testing.T) {
	msg := MessageData{
		Sender:  "test@example.com",
		Subject: "Test",
	}
	examples := []TrainingRef{
		{Sender: "promo@store.com", Subject: "Sale ends today", SentDate: "2026-04-01"},
	}

	system, _ := BuildAnalysisPrompt(msg, examples)
	if !strings.Contains(system, "user has confirmed") {
		t.Error("system prompt missing examples section")
	}
	if !strings.Contains(system, "promo@store.com") {
		t.Error("system prompt missing example sender")
	}
}

func TestBuildClassificationPrompt(t *testing.T) {
	msg := MessageData{
		Sender:   "receipts@stripe.com",
		Subject:  "Payment receipt",
		Category: "receipt",
	}

	system, user := BuildClassificationPrompt(msg, nil)

	if !strings.Contains(system, "email classifier") {
		t.Error("system prompt missing classifier instruction")
	}
	if !strings.Contains(user, "receipts@stripe.com") {
		t.Error("user content missing sender")
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/llm/ -v -run TestBuild`
Expected: FAIL

- [ ] **Step 3: Implement prompt builders**

Create `web/internal/llm/prompts.go`:

```go
package llm

import (
	"fmt"
	"strings"
	"time"
)

type MessageData struct {
	Sender       string
	Subject      string
	SentDate     string
	BodySnippet  string
	CustomPrompt string
	Category     string
}

type TrainingRef struct {
	Sender    string
	Subject   string
	SentDate  string
	ExpiresAt string
}

func BuildAnalysisPrompt(msg MessageData, examples []TrainingRef) (systemPrompt, userContent string) {
	now := time.Now()
	currentTime := now.Format(time.RFC3339)
	currentDate := now.Format("Monday, January 2, 2006")

	sentDate := msg.SentDate
	if t, err := time.Parse(time.RFC3339, msg.SentDate); err == nil {
		sentDate = t.Format("Monday, January 2, 2006") + " (" + msg.SentDate + ")"
	}

	var examplesSection string
	if len(examples) > 0 {
		limit := 5
		if len(examples) < limit {
			limit = len(examples)
		}
		recent := examples[len(examples)-limit:]
		var lines []string
		for i, ex := range recent {
			line := fmt.Sprintf("%d. From: %s | Subject: %s | Sent: %s", i+1, ex.Sender, ex.Subject, ex.SentDate)
			if ex.ExpiresAt != "" {
				line += " | Expired at: " + ex.ExpiresAt
			}
			lines = append(lines, line)
		}
		examplesSection = fmt.Sprintf("\n\nThe user has confirmed these emails were time-sensitive and expired:\n%s\n\nUse these as reference — similar emails should be treated as time-sensitive.", strings.Join(lines, "\n"))
	}

	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = "\n\nAdditional user-provided guidelines (these take priority):\n" + msg.CustomPrompt
	}

	systemPrompt = fmt.Sprintf(`Decide if this email has expired. Follow the steps below.

TODAY is %s (%s).
%sSTEP 1: Does the email contain a deadline, event date, or expiration?
Look for: dates, times, "ends tonight", "ends at midnight", "today only", "last chance", "expires", "final hours", appointment times, event times, check-in times, verification codes.

STEP 2: What is the expiration date/time?
- If "ends tonight", "ends at midnight", "today only", "last chance", "final hours" → midnight on the SEND DATE.
- If a specific date/time is mentioned → use that date/time.
- If "appointment" or "event" with a date → the event date/time.
- If verification code, OTP, magic link → 1 hour after send date.
- If shipping "out for delivery", "arriving today" → 24 hours after send date.
- If transit alert, delay, disruption → 3 hours after send date.

STEP 3: Is the expiration date BEFORE today (%s)?
If yes → isTimeSensitive: true, and set expiresAt.

NOT time-sensitive (always set isTimeSensitive: false):
- Newsletters, digests, informational content.
- Personal correspondence.
- Receipts, order confirmations, billing statements.

Respond ONLY with JSON:
{
  "isTimeSensitive": true/false,
  "expiresAt": "ISO-8601 datetime or null",
  "reason": "brief explanation",
  "confidence": 0.0 to 1.0
}

When in doubt, prefer false negatives.%s`, currentDate, currentTime, examplesSection, currentDate, customSection)

	bodySection := "\n(Body content not provided — analyze based on metadata only)"
	if msg.BodySnippet != "" {
		bodySection = fmt.Sprintf("\nContent snippet:\n---\n%s\n---", msg.BodySnippet)
	}

	sender := msg.Sender
	if sender == "" {
		sender = "unknown"
	}
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}

	userContent = fmt.Sprintf("Email:\n- From: %s\n- Subject: %s\n- Sent: %s\n%s", sender, subject, sentDate, bodySection)

	return systemPrompt, userContent
}

func BuildClassificationPrompt(msg MessageData, examples []TrainingRef) (systemPrompt, userContent string) {
	categoryDescriptions := map[string]string{
		"receipt": "a purchase receipt, payment confirmation, order confirmation, paid invoice, billing statement, or financial transaction record",
	}

	desc := msg.Category
	if d, ok := categoryDescriptions[msg.Category]; ok {
		desc = d
	}

	var examplesSection string
	if len(examples) > 0 {
		limit := 5
		if len(examples) < limit {
			limit = len(examples)
		}
		recent := examples[len(examples)-limit:]
		var lines []string
		for i, ex := range recent {
			lines = append(lines, fmt.Sprintf("%d. From: %s | Subject: %s", i+1, ex.Sender, ex.Subject))
		}
		examplesSection = fmt.Sprintf("\n\nThe user has confirmed these emails are %ss:\n%s\n\nUse these as reference when classifying the email below.", msg.Category, strings.Join(lines, "\n"))
	}

	customSection := ""
	if msg.CustomPrompt != "" {
		customSection = "\n\nAdditional user-provided guidelines (these take priority):\n" + msg.CustomPrompt
	}

	systemPrompt = fmt.Sprintf(`You are an email classifier. Determine if this email is %s.%s

Respond ONLY with a JSON object:
{
  "matches": true/false,
  "reason": "brief 1-sentence explanation",
  "confidence": 0.0 to 1.0
}

Guidelines:
- Purchase receipts, order confirmations, payment confirmations → matches
- Monthly/annual billing statements, subscription renewals → matches
- Paid invoices, donation receipts, tax documents → matches
- Invoices requesting payment (unpaid, due, amount owed) → does NOT match
- Invoices where payment status is unclear → does NOT match
- Shipping/delivery notifications → does NOT match
- Marketing emails from stores → does NOT match
- Account alerts, password resets → does NOT match
- Prefer false negatives over false positives — when in doubt, say false.%s`, desc, examplesSection, customSection)

	bodySection := "\n(Body content not provided — analyze based on metadata only)"
	if msg.BodySnippet != "" {
		bodySection = fmt.Sprintf("\nContent snippet:\n---\n%s\n---", msg.BodySnippet)
	}

	sender := msg.Sender
	if sender == "" {
		sender = "unknown"
	}
	subject := msg.Subject
	if subject == "" {
		subject = "(no subject)"
	}

	userContent = fmt.Sprintf("Email metadata:\n- From: %s\n- Subject: %s\n- Sent: %s\n%s", sender, subject, msg.SentDate, bodySection)

	return systemPrompt, userContent
}
```

- [ ] **Step 4: Run prompt tests**

Run: `cd web && go test ./internal/llm/ -v -run TestBuild`
Expected: all tests PASS

- [ ] **Step 5: Write LLM adapter with response parsing tests**

Create `web/internal/llm/adapter_test.go`:

```go
package llm

import "testing"

func TestParseJSONResponse(t *testing.T) {
	tests := []struct {
		name  string
		input string
		check func(t *testing.T, r *LLMResponse)
	}{
		{
			name:  "standard response",
			input: `{"isTimeSensitive": true, "expiresAt": "2026-04-01T00:00:00Z", "reason": "sale ended", "confidence": 0.85}`,
			check: func(t *testing.T, r *LLMResponse) {
				if !r.IsTimeSensitive { t.Error("expected isTimeSensitive=true") }
				if r.Confidence != 0.85 { t.Errorf("confidence = %f, want 0.85", r.Confidence) }
			},
		},
		{
			name:  "snake_case keys",
			input: `{"is_time_sensitive": true, "expires_at": "2026-04-01T00:00:00Z", "explanation": "expired", "score": 0.9}`,
			check: func(t *testing.T, r *LLMResponse) {
				if !r.IsTimeSensitive { t.Error("expected isTimeSensitive=true from snake_case") }
				if r.Confidence != 0.9 { t.Errorf("confidence = %f, want 0.9", r.Confidence) }
				if r.Reason != "expired" { t.Errorf("reason = %q, want %q", r.Reason, "expired") }
			},
		},
		{
			name:  "markdown code fence",
			input: "```json\n{\"isTimeSensitive\": false, \"reason\": \"not urgent\", \"confidence\": 0.3}\n```",
			check: func(t *testing.T, r *LLMResponse) {
				if r.IsTimeSensitive { t.Error("expected isTimeSensitive=false") }
			},
		},
		{
			name:  "classification response",
			input: `{"matches": true, "reason": "receipt detected", "confidence": 0.95}`,
			check: func(t *testing.T, r *LLMResponse) {
				if !r.Matches { t.Error("expected matches=true") }
			},
		},
		{
			name:  "confidence clamped to 0-1",
			input: `{"isTimeSensitive": true, "confidence": 1.5, "reason": "test"}`,
			check: func(t *testing.T, r *LLMResponse) {
				if r.Confidence != 1.0 { t.Errorf("confidence = %f, want 1.0 (clamped)", r.Confidence) }
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, err := ParseJSONResponse(tt.input)
			if err != nil {
				t.Fatalf("ParseJSONResponse() error: %v", err)
			}
			tt.check(t, r)
		})
	}
}
```

- [ ] **Step 6: Implement LLM adapter**

Create `web/internal/llm/adapter.go`:

```go
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
)

type LLMResponse struct {
	IsTimeSensitive bool    `json:"isTimeSensitive"`
	ExpiresAt       string  `json:"expiresAt"`
	Reason          string  `json:"reason"`
	Confidence      float64 `json:"confidence"`
	Matches         bool    `json:"matches"`
}

var codeFenceRe = regexp.MustCompile("(?s)```(?:json)?\\s*(.*?)```")

func ParseJSONResponse(text string) (*LLMResponse, error) {
	cleaned := text
	if m := codeFenceRe.FindStringSubmatch(text); len(m) > 1 {
		cleaned = m[1]
	}
	cleaned = strings.TrimSpace(cleaned)

	var raw map[string]interface{}
	if err := json.Unmarshal([]byte(cleaned), &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON from LLM: %w", err)
	}

	r := &LLMResponse{}

	// isTimeSensitive / is_time_sensitive
	if v, ok := raw["isTimeSensitive"]; ok {
		r.IsTimeSensitive = toBool(v)
	} else if v, ok := raw["is_time_sensitive"]; ok {
		r.IsTimeSensitive = toBool(v)
	}

	// expiresAt / expires_at
	if v, ok := raw["expiresAt"]; ok {
		r.ExpiresAt = toStr(v)
	} else if v, ok := raw["expires_at"]; ok {
		r.ExpiresAt = toStr(v)
	}

	// reason / explanation
	if v, ok := raw["reason"]; ok {
		r.Reason = toStr(v)
	} else if v, ok := raw["explanation"]; ok {
		r.Reason = toStr(v)
	}

	// confidence / score
	conf := 1.0
	if v, ok := raw["confidence"]; ok {
		conf = toFloat(v)
	} else if v, ok := raw["score"]; ok {
		conf = toFloat(v)
	}
	r.Confidence = math.Max(0, math.Min(1, conf))

	// matches (for classification)
	if v, ok := raw["matches"]; ok {
		r.Matches = toBool(v)
	}

	return r, nil
}

func CallLLM(ctx context.Context, cfg *config.LLM, systemPrompt, userContent string) (*LLMResponse, error) {
	switch cfg.Provider {
	case "gemini":
		return callGemini(ctx, cfg.Gemini, systemPrompt, userContent)
	case "ollama":
		return callOllama(ctx, cfg.Ollama, systemPrompt, userContent)
	default:
		return nil, fmt.Errorf("unknown LLM provider: %s", cfg.Provider)
	}
}

func callGemini(ctx context.Context, cfg config.GeminiConfig, systemPrompt, userContent string) (*LLMResponse, error) {
	if cfg.APIKey == "" {
		return nil, fmt.Errorf("Gemini API key not configured")
	}

	endpoint := fmt.Sprintf("https://generativelanguage.googleapis.com/v1beta/models/%s:generateContent", cfg.Model)

	body := map[string]interface{}{
		"systemInstruction": map[string]interface{}{
			"parts": []map[string]string{{"text": systemPrompt}},
		},
		"contents": []map[string]interface{}{
			{"parts": []map[string]string{{"text": userContent}}},
		},
		"generationConfig": map[string]interface{}{
			"temperature":      0.1,
			"maxOutputTokens":  512,
			"responseMimeType": "application/json",
		},
	}

	return doLLMRequest(ctx, endpoint, body, map[string]string{
		"Content-Type":  "application/json",
		"x-goog-api-key": cfg.APIKey,
	}, func(data []byte) (string, error) {
		var resp struct {
			Candidates []struct {
				Content struct {
					Parts []struct{ Text string } `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return "", err
		}
		if len(resp.Candidates) == 0 || len(resp.Candidates[0].Content.Parts) == 0 {
			return "", fmt.Errorf("empty response from Gemini")
		}
		if fr := resp.Candidates[0].FinishReason; fr != "" && fr != "STOP" {
			return "", fmt.Errorf("Gemini refused (reason: %s)", fr)
		}
		return resp.Candidates[0].Content.Parts[0].Text, nil
	})
}

func callOllama(ctx context.Context, cfg config.OllamaConfig, systemPrompt, userContent string) (*LLMResponse, error) {
	if cfg.Endpoint == "" {
		return nil, fmt.Errorf("Ollama endpoint not configured")
	}

	endpoint := cfg.Endpoint + "/api/chat"

	body := map[string]interface{}{
		"model": cfg.Model,
		"messages": []map[string]string{
			{"role": "system", "content": systemPrompt},
			{"role": "user", "content": userContent},
		},
		"stream": false,
		"format": "json",
		"options": map[string]interface{}{
			"temperature": 0.1,
			"num_predict": 512,
		},
	}

	return doLLMRequest(ctx, endpoint, body, map[string]string{
		"Content-Type": "application/json",
	}, func(data []byte) (string, error) {
		var resp struct {
			Message struct{ Content string } `json:"message"`
		}
		if err := json.Unmarshal(data, &resp); err != nil {
			return "", err
		}
		if resp.Message.Content == "" {
			return "", fmt.Errorf("empty response from Ollama")
		}
		return resp.Message.Content, nil
	})
}

func doLLMRequest(ctx context.Context, endpoint string, body interface{}, headers map[string]string, extractText func([]byte) (string, error)) (*LLMResponse, error) {
	jsonBody, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "POST", endpoint, bytes.NewReader(jsonBody))
	if err != nil {
		return nil, err
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LLM request failed: %w", err)
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("LLM API error (%d): %s", resp.StatusCode, string(data))
	}

	text, err := extractText(data)
	if err != nil {
		return nil, err
	}

	return ParseJSONResponse(text)
}

func toBool(v interface{}) bool {
	switch val := v.(type) {
	case bool:
		return val
	case float64:
		return val != 0
	default:
		return false
	}
}

func toStr(v interface{}) string {
	if v == nil {
		return ""
	}
	return fmt.Sprintf("%v", v)
}

func toFloat(v interface{}) float64 {
	switch val := v.(type) {
	case float64:
		return val
	case int:
		return float64(val)
	default:
		return 1.0
	}
}
```

- [ ] **Step 7: Run all LLM tests**

Run: `cd web && go test ./internal/llm/ -v`
Expected: all tests PASS

- [ ] **Step 8: Commit**

```bash
git add web/
git commit -m "feat: LLM adapter with Gemini/Ollama backends and prompt builders"
```

---

## Task 7: IMAP Client

**Files:**
- Create: `web/internal/imap/client.go`
- Create: `web/internal/imap/client_test.go`

The IMAP client wraps go-imap with the specific operations MailReaper needs. Integration tests against a real IMAP server would be ideal but are environment-dependent, so we'll test the message parsing and folder name helpers with unit tests and leave connection tests as manual verification.

- [ ] **Step 1: Write IMAP helper tests**

Create `web/internal/imap/client_test.go`:

```go
package imap

import "testing"

func TestSanitizeFolderName(t *testing.T) {
	tests := []struct {
		input, want string
	}{
		{"Expired", "Expired"},
		{"Paper-Trail", "Paper-Trail"},
		{"My Folder", "My Folder"},
		{"", ""},
	}

	for _, tt := range tests {
		got := SanitizeFolderName(tt.input)
		if got != tt.want {
			t.Errorf("SanitizeFolderName(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
```

- [ ] **Step 2: Implement IMAP client**

Create `web/internal/imap/client.go`:

```go
package imap

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"

	"github.com/kraftbj/mailreaper/internal/config"
)

type Client struct {
	client *imapclient.Client
	config config.Account
}

type FetchedMessage struct {
	UID       uint32
	MessageID string
	Subject   string
	Sender    string
	Date      time.Time
	Folder    string
	Headers   map[string][]string
}

func Connect(acct config.Account) (*Client, error) {
	addr := fmt.Sprintf("%s:%d", acct.Host, acct.Port)

	var c *imapclient.Client
	var err error

	if acct.TLS {
		c, err = imapclient.DialTLS(addr, nil)
	} else {
		c, err = imapclient.DialInsecure(addr)
	}
	if err != nil {
		return nil, fmt.Errorf("connect to %s: %w", addr, err)
	}

	if err := c.Login(acct.Username, acct.Password).Wait(); err != nil {
		c.Close()
		return nil, fmt.Errorf("login to %s: %w", acct.Host, err)
	}

	return &Client{client: c, config: acct}, nil
}

func (c *Client) Close() error {
	if err := c.client.Logout().Wait(); err != nil {
		return c.client.Close()
	}
	return c.client.Close()
}

func (c *Client) FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]FetchedMessage, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", folder, err)
	}

	criteria := &imap.SearchCriteria{
		Since: since,
	}
	// Only fetch messages older than maxAge
	if maxAge > 0 {
		before := time.Now().Add(-maxAge)
		criteria.Before = before
	}

	searchData, err := c.client.Search(criteria, nil).Wait()
	if err != nil {
		return nil, fmt.Errorf("search %s: %w", folder, err)
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		return nil, nil
	}

	// Limit results
	if limit > 0 && len(seqNums) > limit {
		seqNums = seqNums[len(seqNums)-limit:]
	}

	seqSet := imap.SeqSetNum(seqNums...)
	fetchItems := &imap.FetchOptions{
		Envelope:    true,
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierHeader},
		},
	}

	fetchCmd := c.client.Fetch(seqSet, fetchItems)

	var messages []FetchedMessage
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}

		fm := FetchedMessage{Folder: folder}

		if env := msg.Envelope; env != nil {
			fm.Subject = env.Subject
			fm.Date = env.Date
			fm.MessageID = env.MessageID
			if len(env.From) > 0 {
				from := env.From[0]
				if from.Name != "" {
					fm.Sender = fmt.Sprintf("%s <%s@%s>", from.Name, from.Mailbox, from.Host)
				} else {
					fm.Sender = fmt.Sprintf("%s@%s", from.Mailbox, from.Host)
				}
			}
		}

		// Parse headers
		fm.Headers = make(map[string][]string)
		for _, section := range msg.BodySection {
			if section != nil {
				headerStr := string(section)
				for _, line := range strings.Split(headerStr, "\r\n") {
					if idx := strings.Index(line, ":"); idx > 0 {
						key := strings.ToLower(strings.TrimSpace(line[:idx]))
						val := strings.TrimSpace(line[idx+1:])
						fm.Headers[key] = append(fm.Headers[key], val)
					}
				}
			}
		}

		messages = append(messages, fm)
	}

	if err := fetchCmd.Close(); err != nil {
		return messages, fmt.Errorf("fetch close: %w", err)
	}

	return messages, nil
}

func (c *Client) FetchBody(folder string, uid uint32) (string, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return "", fmt.Errorf("select %s: %w", folder, err)
	}

	seqSet := imap.SeqSetNum(uid)
	fetchItems := &imap.FetchOptions{
		BodySection: []*imap.FetchItemBodySection{
			{Specifier: imap.PartSpecifierText},
		},
	}

	fetchCmd := c.client.Fetch(seqSet, fetchItems)
	msg := fetchCmd.Next()
	if msg == nil {
		fetchCmd.Close()
		return "", fmt.Errorf("message not found")
	}

	var body string
	for _, section := range msg.BodySection {
		if section != nil {
			body = string(section)
			break
		}
	}

	fetchCmd.Close()
	return body, nil
}

func (c *Client) MoveMessage(folder string, uid uint32, destFolder string) error {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", folder, err)
	}

	seqSet := imap.SeqSetNum(uid)
	if err := c.client.Move(seqSet, destFolder).Wait(); err != nil {
		return fmt.Errorf("move to %s: %w", destFolder, err)
	}
	return nil
}

func (c *Client) DeleteMessage(folder string, uid uint32, permanent bool) error {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return fmt.Errorf("select %s: %w", folder, err)
	}

	seqSet := imap.SeqSetNum(uid)

	storeFlags := &imap.StoreFlags{
		Op:    imap.StoreFlagsAdd,
		Flags: []imap.Flag{imap.FlagDeleted},
	}
	if err := c.client.Store(seqSet, storeFlags, nil).Wait(); err != nil {
		return fmt.Errorf("flag deleted: %w", err)
	}

	if permanent {
		if err := c.client.Expunge().Wait(); err != nil {
			return fmt.Errorf("expunge: %w", err)
		}
	}
	return nil
}

func (c *Client) EnsureFolder(name string) error {
	listCmd := c.client.List("", name, nil)
	found := false
	for {
		mbox := listCmd.Next()
		if mbox == nil {
			break
		}
		found = true
	}
	listCmd.Close()

	if !found {
		if err := c.client.Create(name, nil).Wait(); err != nil {
			return fmt.Errorf("create folder %s: %w", name, err)
		}
		log.Printf("[MailReaper] Created folder: %s", name)
	}
	return nil
}

func (c *Client) GetMessageIDsInFolder(folder string) ([]string, error) {
	if _, err := c.client.Select(folder, nil).Wait(); err != nil {
		return nil, fmt.Errorf("select %s: %w", folder, err)
	}

	criteria := &imap.SearchCriteria{}
	searchData, err := c.client.Search(criteria, nil).Wait()
	if err != nil {
		return nil, err
	}

	seqNums := searchData.AllSeqNums()
	if len(seqNums) == 0 {
		return nil, nil
	}

	seqSet := imap.SeqSetNum(seqNums...)
	fetchCmd := c.client.Fetch(seqSet, &imap.FetchOptions{Envelope: true})

	var ids []string
	for {
		msg := fetchCmd.Next()
		if msg == nil {
			break
		}
		if msg.Envelope != nil && msg.Envelope.MessageID != "" {
			ids = append(ids, msg.Envelope.MessageID)
		}
	}
	fetchCmd.Close()

	return ids, nil
}

func SanitizeFolderName(name string) string {
	return strings.TrimSpace(name)
}
```

- [ ] **Step 3: Add go-imap dependency and run tests**

```bash
cd web && go get github.com/emersion/go-imap/v2 && go test ./internal/imap/ -v
```

Expected: unit tests PASS (no integration test against a real server)

- [ ] **Step 4: Commit**

```bash
git add web/
git commit -m "feat: IMAP client with fetch, move, delete, and folder management"
```

---

## Task 8: Scanner Orchestration

**Files:**
- Create: `web/internal/scanner/scanner.go`
- Create: `web/internal/scanner/feedback.go`
- Create: `web/internal/scanner/scanner_test.go`

- [ ] **Step 1: Write scanner tests using interfaces**

Create `web/internal/scanner/scanner_test.go`:

```go
package scanner

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	dbpkg "github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
)

// mockMailClient implements MailClient for testing without IMAP
type mockMailClient struct {
	messages     []imappkg.FetchedMessage
	movedMsgs    []string // message IDs that were moved
	inboxMsgIDs  []string // message IDs currently in inbox
}

func (m *mockMailClient) FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]imappkg.FetchedMessage, error) {
	return m.messages, nil
}

func (m *mockMailClient) MoveMessage(folder string, uid uint32, dest string) error {
	m.movedMsgs = append(m.movedMsgs, dest)
	return nil
}

func (m *mockMailClient) EnsureFolder(name string) error { return nil }

func (m *mockMailClient) GetMessageIDsInFolder(folder string) ([]string, error) {
	return m.inboxMsgIDs, nil
}

func (m *mockMailClient) FetchBody(folder string, uid uint32) (string, error) {
	return "test body", nil
}

func (m *mockMailClient) Close() error { return nil }

func TestScanCycleTTLRule(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()

	// Seed a TTL rule
	database.SaveRule(dbpkg.Rule{
		ID: "test-otp", Name: "OTP", Enabled: true, Priority: 10,
		MatchConfig: dbpkg.MatchConfig{SubjectPatterns: []string{"*verification*"}},
		ExpirationConfig: dbpkg.ExpirationConfig{Type: "ttl", Hours: 1},
		Action: "move", DestinationFolder: "Expired", GracePeriodDays: 1,
	})

	// Insert account
	database.UpsertAccount("test", "Test Account")

	mock := &mockMailClient{
		messages: []imappkg.FetchedMessage{
			{
				UID:       1,
				MessageID: "<otp1@ex>",
				Subject:   "Your verification code",
				Sender:    "noreply@github.com",
				Date:      time.Now().Add(-2 * time.Hour), // 2 hours old, 1h TTL → expired
				Folder:    "INBOX",
			},
		},
	}

	cfg := &config.Config{
		Scan: config.Scan{MaxMessagesPerScan: 100, MinMessageAgeMin: 0},
		LLM:  config.LLM{Provider: "none"},
	}

	s := New(database, cfg)
	err = s.ScanAccount(context.Background(), mock, "test", []string{"INBOX"})
	if err != nil {
		t.Fatalf("ScanAccount() error: %v", err)
	}

	if len(mock.movedMsgs) != 1 {
		t.Errorf("expected 1 move, got %d", len(mock.movedMsgs))
	}
	if len(mock.movedMsgs) > 0 && mock.movedMsgs[0] != "Expired" {
		t.Errorf("moved to %q, want %q", mock.movedMsgs[0], "Expired")
	}

	// Check verdict was recorded
	v, _ := database.GetVerdictByMessageID("<otp1@ex>")
	if v == nil {
		t.Fatal("expected verdict to be recorded")
	}
	if v.Status != "executed" {
		t.Errorf("verdict status = %q, want %q", v.Status, "executed")
	}
}

func TestScanCycleNoMatch(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, _ := dbpkg.Open(dbPath)
	defer database.Close()

	database.SaveRule(dbpkg.Rule{
		ID: "test-otp", Name: "OTP", Enabled: true, Priority: 10,
		MatchConfig: dbpkg.MatchConfig{SubjectPatterns: []string{"*verification*"}},
		ExpirationConfig: dbpkg.ExpirationConfig{Type: "ttl", Hours: 1},
		Action: "move", DestinationFolder: "Expired", GracePeriodDays: 1,
	})
	database.UpsertAccount("test", "Test Account")

	mock := &mockMailClient{
		messages: []imappkg.FetchedMessage{
			{UID: 1, MessageID: "<nomatch@ex>", Subject: "Meeting notes", Sender: "boss@work.com", Date: time.Now().Add(-2 * time.Hour), Folder: "INBOX"},
		},
	}

	cfg := &config.Config{
		Scan: config.Scan{MaxMessagesPerScan: 100},
		LLM:  config.LLM{Provider: "none"},
	}

	s := New(database, cfg)
	s.ScanAccount(context.Background(), mock, "test", []string{"INBOX"})

	if len(mock.movedMsgs) != 0 {
		t.Errorf("expected 0 moves, got %d", len(mock.movedMsgs))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/scanner/ -v`
Expected: FAIL

- [ ] **Step 3: Add UpsertAccount to db**

Add to `web/internal/db/db.go`:

```go
func (d *DB) UpsertAccount(id, name string) error {
	_, err := d.db.Exec(`
		INSERT INTO accounts (id, name) VALUES (?, ?)
		ON CONFLICT(id) DO UPDATE SET name=excluded.name
	`, id, name)
	return err
}

func (d *DB) UpdateAccountScan(id string, messageCount int) error {
	_, err := d.db.Exec(`
		UPDATE accounts SET last_scan_at = CURRENT_TIMESTAMP, last_scan_message_count = ? WHERE id = ?
	`, messageCount, id)
	return err
}
```

- [ ] **Step 4: Implement scanner**

Create `web/internal/scanner/scanner.go`:

```go
package scanner

import (
	"context"
	"log"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
)

// MailClient abstracts IMAP operations for testing.
type MailClient interface {
	FetchNewMessages(folder string, since time.Time, maxAge time.Duration, limit int) ([]imappkg.FetchedMessage, error)
	MoveMessage(folder string, uid uint32, dest string) error
	EnsureFolder(name string) error
	GetMessageIDsInFolder(folder string) ([]string, error)
	FetchBody(folder string, uid uint32) (string, error)
	Close() error
}

type Scanner struct {
	db  *db.DB
	cfg *config.Config
}

func New(database *db.DB, cfg *config.Config) *Scanner {
	return &Scanner{db: database, cfg: cfg}
}

func (s *Scanner) ScanAccount(ctx context.Context, client MailClient, accountID string, folders []string) error {
	enabledRules, err := s.db.GetEnabledRules()
	if err != nil {
		return err
	}

	confidenceThreshold := 0.7 // default

	totalProcessed := 0

	for _, folder := range folders {
		since := time.Now().Add(-7 * 24 * time.Hour) // Look back 7 days
		maxAge := time.Duration(s.cfg.Scan.MinMessageAgeMin) * time.Minute
		limit := s.cfg.Scan.MaxMessagesPerScan

		messages, err := client.FetchNewMessages(folder, since, maxAge, limit)
		if err != nil {
			log.Printf("[MailReaper] Error fetching from %s: %v", folder, err)
			continue
		}

		for _, msg := range messages {
			if ctx.Err() != nil {
				return ctx.Err()
			}

			// Skip if we already have a verdict for this message
			existing, _ := s.db.GetVerdictByMessageID(msg.MessageID)
			if existing != nil {
				continue
			}

			verdict := s.evaluateMessage(ctx, client, msg, enabledRules)
			if verdict == nil {
				continue
			}

			// Record verdict
			v := db.Verdict{
				AccountID:         accountID,
				MessageIDHeader:   msg.MessageID,
				Subject:           msg.Subject,
				Sender:            msg.Sender,
				SentAt:            msg.Date,
				Status:            "pending",
				DestinationFolder: verdict.Rule.DestinationFolder,
				Reason:            verdict.Reason,
				Confidence:        verdict.Confidence,
			}

			ruleID := verdict.Rule.ID
			v.RuleID = &ruleID

			if verdict.ExpiresAt != nil {
				v.ExpiresAt = verdict.ExpiresAt
			}

			// Confidence gate
			if verdict.Confidence >= confidenceThreshold {
				// Auto-execute
				dest := verdict.Rule.DestinationFolder
				if dest == "" {
					dest = "Expired"
				}

				if err := client.EnsureFolder(dest); err != nil {
					log.Printf("[MailReaper] Error ensuring folder %s: %v", dest, err)
					continue
				}

				if err := client.MoveMessage(msg.Folder, msg.UID, dest); err != nil {
					log.Printf("[MailReaper] Error moving message: %v", err)
					s.db.LogActivity(db.ActivityEntry{
						Type: "error", AccountID: accountID,
						MessageIDHeader: msg.MessageID, Subject: msg.Subject,
						Reason: err.Error(),
					})
					continue
				}

				v.Status = "executed"
				now := time.Now()
				v.ActedAt = &now

				actType := "moved"
				if verdict.Classified {
					actType = "classified"
				}
				s.db.LogActivity(db.ActivityEntry{
					Type: actType, AccountID: accountID,
					MessageIDHeader: msg.MessageID, Subject: msg.Subject,
					Sender: msg.Sender, RuleName: verdict.Rule.Name,
					Destination: dest, Reason: verdict.Reason,
					Confidence: verdict.Confidence,
				})

				log.Printf("[MailReaper] %s \"%s\" → %s (rule: %s, conf: %.2f)",
					actType, msg.Subject, dest, verdict.Rule.Name, verdict.Confidence)
			}

			s.db.SaveVerdict(v)
			totalProcessed++
		}
	}

	s.db.UpdateAccountScan(accountID, totalProcessed)
	return nil
}

func (s *Scanner) evaluateMessage(ctx context.Context, client MailClient, msg imappkg.FetchedMessage, enabledRules []db.Rule) *rules.RuleVerdict {
	ruleMsg := &rules.Message{
		Author:    msg.Sender,
		Subject:   msg.Subject,
		Date:      msg.Date,
		Folder:    msg.Folder,
		MessageID: msg.MessageID,
	}

	for _, rule := range enabledRules {
		if !rules.MatchesRule(ruleMsg, rule) {
			continue
		}

		switch rule.ExpirationConfig.Type {
		case "ttl":
			if v := rules.EvaluateTTL(ruleMsg, rule); v != nil {
				return v
			}
		case "header":
			if v := rules.EvaluateHeader(msg.Headers, rule); v != nil {
				return v
			}
		case "content-regex":
			body, err := client.FetchBody(msg.Folder, msg.UID)
			if err != nil {
				log.Printf("[MailReaper] Error fetching body for regex: %v", err)
				continue
			}
			if v := rules.EvaluateContentRegex(body, rule); v != nil {
				return v
			}
		case "classify":
			return rules.EvaluateClassify(rule)
		case "llm", "llm-classify":
			// LLM evaluation deferred to Task 9 integration
			if s.cfg.LLM.Provider == "none" {
				continue
			}
			// TODO: integrate LLM calls in Task 9
			continue
		}
	}

	return nil
}
```

- [ ] **Step 5: Implement feedback detection**

Create `web/internal/scanner/feedback.go`:

```go
package scanner

import (
	"log"

	"github.com/kraftbj/mailreaper/internal/db"
)

// DetectFeedback checks if any previously-executed messages have been
// moved back to Inbox, indicating a false positive.
func (s *Scanner) DetectFeedback(client MailClient, accountID string, inboxFolder string) error {
	// Get all message IDs currently in inbox
	inboxIDs, err := client.GetMessageIDsInFolder(inboxFolder)
	if err != nil {
		return err
	}

	// Get all executed verdict message IDs for this account
	executedIDs, err := s.db.GetExecutedMessageIDs(accountID)
	if err != nil {
		return err
	}

	// Build a set of inbox IDs for fast lookup
	inboxSet := make(map[string]bool, len(inboxIDs))
	for _, id := range inboxIDs {
		inboxSet[id] = true
	}

	// Any executed message that's back in inbox = correction
	for _, msgID := range executedIDs {
		if !inboxSet[msgID] {
			continue
		}

		log.Printf("[MailReaper] Correction detected: %s moved back to Inbox", msgID)

		// Mark verdict as corrected
		if err := s.db.UpdateVerdictStatus(msgID, "corrected"); err != nil {
			log.Printf("[MailReaper] Error updating verdict: %v", err)
			continue
		}

		// Get the verdict to create a training example
		v, err := s.db.GetVerdictByMessageID(msgID)
		if err != nil || v == nil {
			continue
		}

		// Create training example
		s.db.AddTrainingExample(db.TrainingExample{
			Category: "expiry",
			Subject:  v.Subject,
			Sender:   v.Sender,
			Label:    "keep",
			Source:   "correction",
		})

		// Invalidate LLM cache
		s.db.RemoveCachedVerdict(msgID)

		// Log activity
		s.db.LogActivity(db.ActivityEntry{
			Type:            "corrected",
			AccountID:       accountID,
			MessageIDHeader: msgID,
			Subject:         v.Subject,
			Sender:          v.Sender,
			Reason:          "Moved back to Inbox by user",
		})
	}

	return nil
}
```

- [ ] **Step 6: Run scanner tests**

Run: `cd web && go test ./internal/scanner/ -v`
Expected: all tests PASS

- [ ] **Step 7: Commit**

```bash
git add web/
git commit -m "feat: scanner orchestration with rule evaluation and feedback detection"
```

---

## Task 9: Scanner LLM Integration

**Files:**
- Modify: `web/internal/scanner/scanner.go` (add LLM evaluation to `evaluateMessage`)

- [ ] **Step 1: Write LLM integration test**

Add to `web/internal/scanner/scanner_test.go`:

```go
func TestScanSkipsLLMWhenProviderNone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, _ := dbpkg.Open(dbPath)
	defer database.Close()

	database.SaveRule(dbpkg.Rule{
		ID: "llm-rule", Name: "LLM Scanner", Enabled: true, Priority: 100,
		MatchConfig:      dbpkg.MatchConfig{SubjectPatterns: []string{"*"}},
		ExpirationConfig: dbpkg.ExpirationConfig{Type: "llm"},
		Action: "move", DestinationFolder: "Expired", GracePeriodDays: 7,
	})
	database.UpsertAccount("test", "Test")

	mock := &mockMailClient{
		messages: []imappkg.FetchedMessage{
			{UID: 1, MessageID: "<llm-skip@ex>", Subject: "Some promo", Sender: "promo@store.com", Date: time.Now().Add(-2 * time.Hour), Folder: "INBOX"},
		},
	}

	cfg := &config.Config{
		Scan: config.Scan{MaxMessagesPerScan: 100},
		LLM:  config.LLM{Provider: "none"},
	}

	s := New(database, cfg)
	s.ScanAccount(context.Background(), mock, "test", []string{"INBOX"})

	// Should NOT have moved anything since LLM is disabled
	if len(mock.movedMsgs) != 0 {
		t.Errorf("expected 0 moves with LLM=none, got %d", len(mock.movedMsgs))
	}
}
```

- [ ] **Step 2: Update evaluateMessage to call LLM**

Replace the `case "llm", "llm-classify"` block in `web/internal/scanner/scanner.go`:

```go
		case "llm":
			if s.cfg.LLM.Provider == "none" {
				continue
			}
			// Check LLM cache first
			cached, _ := s.db.GetCachedVerdict(msg.MessageID)
			if cached != nil {
				if cached.Error != "" {
					continue
				}
				if cached.Expired {
					expiresAt, _ := time.Parse(time.RFC3339, cached.ExpiresAt)
					return &rules.RuleVerdict{
						Expired: true, Rule: rule, ExpiresAt: &expiresAt,
						Reason: cached.Reason, Confidence: cached.Confidence,
					}
				}
				continue // cached as not expired
			}

			body, err := client.FetchBody(msg.Folder, msg.UID)
			if err != nil {
				continue
			}
			snippet := body
			if len(snippet) > 2000 {
				snippet = snippet[:2000]
			}

			examples, _ := s.db.GetTrainingExamples("expiry")
			var refs []llmpkg.TrainingRef
			for _, ex := range examples {
				refs = append(refs, llmpkg.TrainingRef{
					Sender: ex.Sender, Subject: ex.Subject,
				})
			}

			sysPrompt, userContent := llmpkg.BuildAnalysisPrompt(llmpkg.MessageData{
				Sender: msg.Sender, Subject: msg.Subject,
				SentDate: msg.Date.Format(time.RFC3339), BodySnippet: snippet,
				CustomPrompt: rule.ExpirationConfig.Prompt,
			}, refs)

			result, err := llmpkg.CallLLM(ctx, &s.cfg.LLM, sysPrompt, userContent)
			if err != nil {
				log.Printf("[MailReaper] LLM error: %v", err)
				s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{Error: err.Error()})
				continue
			}

			isExpired := result.IsTimeSensitive && result.ExpiresAt != "" && func() bool {
				t, err := time.Parse(time.RFC3339, result.ExpiresAt)
				return err == nil && time.Now().After(t)
			}()

			s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{
				Expired: isExpired, ExpiresAt: result.ExpiresAt,
				Reason: result.Reason, Confidence: result.Confidence,
			})

			if isExpired {
				expiresAt, _ := time.Parse(time.RFC3339, result.ExpiresAt)
				return &rules.RuleVerdict{
					Expired: true, Rule: rule, ExpiresAt: &expiresAt,
					Reason: "LLM: " + result.Reason, Confidence: result.Confidence,
				}
			}

		case "llm-classify":
			if s.cfg.LLM.Provider == "none" {
				continue
			}
			cached, _ := s.db.GetCachedVerdict(msg.MessageID)
			if cached != nil {
				if cached.Error != "" {
					continue
				}
				if cached.Classified {
					return &rules.RuleVerdict{
						Classified: true, Rule: rule,
						Reason: cached.Reason, Confidence: cached.Confidence,
					}
				}
				continue
			}

			body, err := client.FetchBody(msg.Folder, msg.UID)
			if err != nil {
				continue
			}
			snippet := body
			if len(snippet) > 2000 {
				snippet = snippet[:2000]
			}

			category := rule.ExpirationConfig.Category
			if category == "" {
				category = "receipt"
			}
			examples, _ := s.db.GetTrainingExamples(category)
			var refs []llmpkg.TrainingRef
			for _, ex := range examples {
				refs = append(refs, llmpkg.TrainingRef{Sender: ex.Sender, Subject: ex.Subject})
			}

			sysPrompt, userContent := llmpkg.BuildClassificationPrompt(llmpkg.MessageData{
				Sender: msg.Sender, Subject: msg.Subject,
				SentDate: msg.Date.Format(time.RFC3339), BodySnippet: snippet,
				Category: category, CustomPrompt: rule.ExpirationConfig.Prompt,
			}, refs)

			result, err := llmpkg.CallLLM(ctx, &s.cfg.LLM, sysPrompt, userContent)
			if err != nil {
				s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{Error: err.Error()})
				continue
			}

			s.db.SetCachedVerdict(msg.MessageID, db.CachedVerdict{
				Classified: result.Matches, Reason: result.Reason, Confidence: result.Confidence,
			})

			if result.Matches {
				return &rules.RuleVerdict{
					Classified: true, Rule: rule,
					Reason: "LLM: " + result.Reason, Confidence: result.Confidence,
				}
			}
```

Add the import for the llm package at the top of scanner.go:

```go
llmpkg "github.com/kraftbj/mailreaper/internal/llm"
```

- [ ] **Step 3: Run scanner tests**

Run: `cd web && go test ./internal/scanner/ -v`
Expected: all tests PASS

- [ ] **Step 4: Commit**

```bash
git add web/
git commit -m "feat: integrate LLM analysis and classification into scanner"
```

---

## Task 10: Web Server and API

**Files:**
- Create: `web/internal/server/server.go`
- Create: `web/internal/server/api.go`
- Create: `web/internal/server/sse.go`
- Create: `web/internal/server/api_test.go`

- [ ] **Step 1: Write API tests**

Create `web/internal/server/api_test.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	dbpkg "github.com/kraftbj/mailreaper/internal/db"
)

func setupTestServer(t *testing.T) (*Server, *dbpkg.DB) {
	t.Helper()
	dbPath := filepath.Join(t.TempDir(), "test.db")
	database, err := dbpkg.Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { database.Close() })

	srv := NewServer(database, 0)
	return srv, database
}

func TestAPIGetRules(t *testing.T) {
	srv, database := setupTestServer(t)

	database.SaveRule(dbpkg.Rule{ID: "r1", Name: "Rule 1", Enabled: true, Priority: 10, Action: "move", GracePeriodDays: 7})

	req := httptest.NewRequest("GET", "/api/rules", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var rules []dbpkg.Rule
	json.NewDecoder(w.Body).Decode(&rules)
	if len(rules) != 1 {
		t.Errorf("expected 1 rule, got %d", len(rules))
	}
}

func TestAPIGetActivity(t *testing.T) {
	srv, database := setupTestServer(t)

	database.LogActivity(dbpkg.ActivityEntry{Type: "moved", Subject: "Test"})

	req := httptest.NewRequest("GET", "/api/activity", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var entries []dbpkg.ActivityEntry
	json.NewDecoder(w.Body).Decode(&entries)
	if len(entries) != 1 {
		t.Errorf("expected 1 entry, got %d", len(entries))
	}
}

func TestAPIGetPendingVerdicts(t *testing.T) {
	srv, database := setupTestServer(t)

	database.UpsertAccount("acct1", "Test")
	database.SaveVerdict(dbpkg.Verdict{AccountID: "acct1", MessageIDHeader: "<p1@ex>", Status: "pending", Confidence: 0.5})

	req := httptest.NewRequest("GET", "/api/verdicts/pending", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var verdicts []dbpkg.Verdict
	json.NewDecoder(w.Body).Decode(&verdicts)
	if len(verdicts) != 1 {
		t.Errorf("expected 1 pending verdict, got %d", len(verdicts))
	}
}

func TestAPIUpdateVerdictStatus(t *testing.T) {
	srv, database := setupTestServer(t)

	database.UpsertAccount("acct1", "Test")
	database.SaveVerdict(dbpkg.Verdict{AccountID: "acct1", MessageIDHeader: "<v1@ex>", Status: "pending"})

	body := strings.NewReader(`{"status":"approved"}`)
	req := httptest.NewRequest("PUT", "/api/verdicts/%3Cv1@ex%3E/status", body)
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	v, _ := database.GetVerdictByMessageID("<v1@ex>")
	if v.Status != "approved" {
		t.Errorf("status = %q, want %q", v.Status, "approved")
	}
}

func TestAPIGetCategories(t *testing.T) {
	srv, database := setupTestServer(t)

	database.SaveCategory(dbpkg.Category{ID: "expired", Name: "Expired", FolderName: "Expired", Color: "#e74c3c"})

	req := httptest.NewRequest("GET", "/api/categories", nil)
	w := httptest.NewRecorder()
	srv.mux.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var cats []dbpkg.Category
	json.NewDecoder(w.Body).Decode(&cats)
	if len(cats) != 1 {
		t.Errorf("expected 1 category, got %d", len(cats))
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd web && go test ./internal/server/ -v`
Expected: FAIL

- [ ] **Step 3: Implement server**

Create `web/internal/server/server.go`:

```go
package server

import (
	"fmt"
	"log"
	"net/http"

	"github.com/kraftbj/mailreaper/internal/db"
)

type Server struct {
	db   *db.DB
	mux  *http.ServeMux
	port int
}

func NewServer(database *db.DB, port int) *Server {
	s := &Server{
		db:   database,
		mux:  http.NewServeMux(),
		port: port,
	}
	s.registerRoutes()
	return s
}

func (s *Server) registerRoutes() {
	// API routes
	s.mux.HandleFunc("GET /api/rules", s.handleGetRules)
	s.mux.HandleFunc("POST /api/rules", s.handleSaveRule)
	s.mux.HandleFunc("DELETE /api/rules/{id}", s.handleDeleteRule)
	s.mux.HandleFunc("GET /api/activity", s.handleGetActivity)
	s.mux.HandleFunc("GET /api/verdicts/pending", s.handleGetPendingVerdicts)
	s.mux.HandleFunc("PUT /api/verdicts/{messageID}/status", s.handleUpdateVerdictStatus)
	s.mux.HandleFunc("GET /api/categories", s.handleGetCategories)
	s.mux.HandleFunc("POST /api/categories", s.handleSaveCategory)
	s.mux.HandleFunc("DELETE /api/categories/{id}", s.handleDeleteCategory)
	s.mux.HandleFunc("GET /api/training", s.handleGetTrainingExamples)
	s.mux.HandleFunc("DELETE /api/training/{id}", s.handleDeleteTrainingExample)
	s.mux.HandleFunc("GET /api/stats", s.handleGetStats)
	s.mux.HandleFunc("GET /api/events", s.handleSSE)

	// Static files (will be embedded in production)
	s.mux.Handle("/", http.FileServer(http.Dir("ui")))
}

func (s *Server) Start() error {
	addr := fmt.Sprintf(":%d", s.port)
	log.Printf("[MailReaper] Dashboard at http://localhost:%d", s.port)
	return http.ListenAndServe(addr, s.mux)
}
```

- [ ] **Step 4: Implement API handlers**

Create `web/internal/server/api.go`:

```go
package server

import (
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/kraftbj/mailreaper/internal/db"
)

func jsonResponse(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func jsonError(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func (s *Server) handleGetRules(w http.ResponseWriter, r *http.Request) {
	rules, err := s.db.GetRules()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if rules == nil {
		rules = []db.Rule{}
	}
	jsonResponse(w, rules)
}

func (s *Server) handleSaveRule(w http.ResponseWriter, r *http.Request) {
	var rule db.Rule
	if err := json.NewDecoder(r.Body).Decode(&rule); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.db.SaveRule(rule); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, rule)
}

func (s *Server) handleDeleteRule(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteRule(id); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetActivity(w http.ResponseWriter, r *http.Request) {
	limitStr := r.URL.Query().Get("limit")
	limit := 50
	if limitStr != "" {
		if l, err := strconv.Atoi(limitStr); err == nil && l > 0 {
			limit = l
		}
	}
	entries, err := s.db.GetActivityLog(limit)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if entries == nil {
		entries = []db.ActivityEntry{}
	}
	jsonResponse(w, entries)
}

func (s *Server) handleGetPendingVerdicts(w http.ResponseWriter, r *http.Request) {
	verdicts, err := s.db.GetPendingVerdicts()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if verdicts == nil {
		verdicts = []db.Verdict{}
	}
	jsonResponse(w, verdicts)
}

func (s *Server) handleUpdateVerdictStatus(w http.ResponseWriter, r *http.Request) {
	messageID, err := url.PathUnescape(r.PathValue("messageID"))
	if err != nil {
		jsonError(w, "invalid message ID", http.StatusBadRequest)
		return
	}

	var body struct {
		Status string `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}

	validStatuses := map[string]bool{"approved": true, "rejected": true, "corrected": true}
	if !validStatuses[body.Status] {
		jsonError(w, "invalid status", http.StatusBadRequest)
		return
	}

	if err := s.db.UpdateVerdictStatus(messageID, body.Status); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// If approved or rejected, create a training example
	if body.Status == "approved" || body.Status == "rejected" {
		v, _ := s.db.GetVerdictByMessageID(messageID)
		if v != nil {
			label := v.DestinationFolder
			if body.Status == "rejected" {
				label = "keep"
			}
			s.db.AddTrainingExample(db.TrainingExample{
				Category: "expiry",
				Subject:  v.Subject,
				Sender:   v.Sender,
				Label:    label,
				Source:   "dashboard",
			})
		}
	}

	jsonResponse(w, map[string]string{"status": "ok"})
}

func (s *Server) handleGetCategories(w http.ResponseWriter, r *http.Request) {
	cats, err := s.db.GetCategories()
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if cats == nil {
		cats = []db.Category{}
	}
	jsonResponse(w, cats)
}

func (s *Server) handleSaveCategory(w http.ResponseWriter, r *http.Request) {
	var cat db.Category
	if err := json.NewDecoder(r.Body).Decode(&cat); err != nil {
		jsonError(w, "invalid JSON", http.StatusBadRequest)
		return
	}
	if err := s.db.SaveCategory(cat); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	jsonResponse(w, cat)
}

func (s *Server) handleDeleteCategory(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if err := s.db.DeleteCategory(id); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetTrainingExamples(w http.ResponseWriter, r *http.Request) {
	category := r.URL.Query().Get("category")
	examples, err := s.db.GetTrainingExamples(category)
	if err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if examples == nil {
		examples = []db.TrainingExample{}
	}
	jsonResponse(w, examples)
}

func (s *Server) handleDeleteTrainingExample(w http.ResponseWriter, r *http.Request) {
	idStr := r.PathValue("id")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		jsonError(w, "invalid ID", http.StatusBadRequest)
		return
	}
	if err := s.db.DeleteTrainingExample(id); err != nil {
		jsonError(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleGetStats(w http.ResponseWriter, r *http.Request) {
	// Aggregate stats for the dashboard
	pending, _ := s.db.GetPendingVerdicts()
	activity, _ := s.db.GetActivityLog(100)
	cats, _ := s.db.GetCategories()

	// Count today's activity by type
	todayMoved := 0
	todayCorrected := 0
	for _, e := range activity {
		switch e.Type {
		case "moved", "classified":
			todayMoved++
		case "corrected":
			todayCorrected++
		}
	}

	stats := map[string]interface{}{
		"pendingCount":    len(pending),
		"todayMoved":     todayMoved,
		"todayCorrected": todayCorrected,
		"categoryCount":  len(cats),
	}
	jsonResponse(w, stats)
}
```

- [ ] **Step 5: Implement SSE**

Create `web/internal/server/sse.go`:

```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
)

type SSEBroker struct {
	mu      sync.RWMutex
	clients map[chan string]bool
}

var broker = &SSEBroker{
	clients: make(map[chan string]bool),
}

func (b *SSEBroker) Subscribe() chan string {
	ch := make(chan string, 10)
	b.mu.Lock()
	b.clients[ch] = true
	b.mu.Unlock()
	return ch
}

func (b *SSEBroker) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *SSEBroker) Publish(event string, data interface{}) {
	jsonData, err := json.Marshal(data)
	if err != nil {
		return
	}
	msg := fmt.Sprintf("event: %s\ndata: %s\n\n", event, string(jsonData))

	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
			// Client too slow, skip
		}
	}
}

func PublishEvent(event string, data interface{}) {
	broker.Publish(event, data)
}

func (s *Server) handleSSE(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming not supported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch := broker.Subscribe()
	defer broker.Unsubscribe(ch)

	// Send initial keepalive
	fmt.Fprintf(w, ": connected\n\n")
	flusher.Flush()

	for {
		select {
		case msg, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprint(w, msg)
			flusher.Flush()
		case <-r.Context().Done():
			return
		}
	}
}
```

- [ ] **Step 6: Run API tests**

Run: `cd web && go test ./internal/server/ -v`
Expected: all tests PASS

- [ ] **Step 7: Commit**

```bash
git add web/
git commit -m "feat: web server with JSON API, SSE, and dashboard stats"
```

---

## Task 11: Main Entry Point and Cron Scheduling

**Files:**
- Modify: `web/cmd/mailreaper/main.go`

- [ ] **Step 1: Update main.go with full startup**

Replace `web/cmd/mailreaper/main.go`:

```go
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/kraftbj/mailreaper/internal/config"
	"github.com/kraftbj/mailreaper/internal/db"
	imappkg "github.com/kraftbj/mailreaper/internal/imap"
	"github.com/kraftbj/mailreaper/internal/rules"
	"github.com/kraftbj/mailreaper/internal/scanner"
	"github.com/kraftbj/mailreaper/internal/server"
)

func main() {
	configPath := flag.String("config", "config.yaml", "path to config file")
	dbPath := flag.String("db", "mailreaper.db", "path to SQLite database")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}

	database, err := db.Open(*dbPath)
	if err != nil {
		log.Fatalf("Failed to open database: %v", err)
	}
	defer database.Close()

	// Seed default rules
	if err := database.SeedDefaults(rules.DefaultRules); err != nil {
		log.Fatalf("Failed to seed defaults: %v", err)
	}

	// Register accounts from config
	for _, acct := range cfg.Accounts {
		database.UpsertAccount(acct.Name, acct.Name)
	}

	log.Printf("[MailReaper] Loaded %d account(s), LLM provider: %s", len(cfg.Accounts), cfg.LLM.Provider)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle shutdown
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("[MailReaper] Shutting down...")
		cancel()
	}()

	// Start scanner in background
	scan := scanner.New(database, cfg)
	go runScanLoop(ctx, scan, cfg)
	go runGraceCleanupLoop(ctx, scan, cfg)

	// Start web server (blocks)
	srv := server.NewServer(database, cfg.Server.Port)
	if err := srv.Start(); err != nil {
		log.Fatalf("Server error: %v", err)
	}
}

func runScanLoop(ctx context.Context, scan *scanner.Scanner, cfg *config.Config) {
	// Run immediately on start
	runFullScan(ctx, scan, cfg)

	ticker := time.NewTicker(time.Duration(cfg.Scan.IntervalMinutes) * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			runFullScan(ctx, scan, cfg)
		case <-ctx.Done():
			return
		}
	}
}

func runFullScan(ctx context.Context, scan *scanner.Scanner, cfg *config.Config) {
	log.Println("[MailReaper] Starting scan cycle...")

	for _, acct := range cfg.Accounts {
		folders := acct.Folders.Scan
		if len(folders) == 0 {
			folders = []string{"INBOX"}
		}

		client, err := imappkg.Connect(acct)
		if err != nil {
			log.Printf("[MailReaper] Failed to connect to %s: %v", acct.Name, err)
			continue
		}

		if err := scan.ScanAccount(ctx, client, acct.Name, folders); err != nil {
			log.Printf("[MailReaper] Scan error for %s: %v", acct.Name, err)
		}

		// Feedback detection
		for _, folder := range folders {
			scan.DetectFeedback(client, acct.Name, folder)
		}

		client.Close()
	}

	log.Println("[MailReaper] Scan cycle complete")
}

func runGraceCleanupLoop(ctx context.Context, scan *scanner.Scanner, cfg *config.Config) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Println("[MailReaper] Running grace period cleanup...")
			// Grace cleanup will be implemented when we add the cleanup method to scanner
		case <-ctx.Done():
			return
		}
	}
}
```

- [ ] **Step 2: Verify it compiles**

Run: `cd web && go build ./cmd/mailreaper/`
Expected: compiles without errors

- [ ] **Step 3: Commit**

```bash
git add web/
git commit -m "feat: main entry point with scan loop, grace cleanup, and web server"
```

---

## Task 12: Docker Setup

**Files:**
- Create: `web/Dockerfile`
- Create: `web/docker-compose.yml`

- [ ] **Step 1: Create Dockerfile**

Create `web/Dockerfile`:

```dockerfile
FROM golang:1.22-alpine AS builder

WORKDIR /build
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -o mailreaper ./cmd/mailreaper/

FROM alpine:3.19
RUN apk add --no-cache ca-certificates tzdata
COPY --from=builder /build/mailreaper /usr/local/bin/mailreaper
COPY --from=builder /build/ui /app/ui

WORKDIR /app
EXPOSE 8025

ENTRYPOINT ["mailreaper"]
CMD ["--config", "/app/config.yaml", "--db", "/app/data/mailreaper.db"]
```

- [ ] **Step 2: Create docker-compose.yml**

Create `web/docker-compose.yml`:

```yaml
services:
  mailreaper:
    build: .
    ports:
      - "8025:8025"
    volumes:
      - ./config.yaml:/app/config.yaml:ro
      - mailreaper-data:/app/data
    restart: unless-stopped

volumes:
  mailreaper-data:
```

- [ ] **Step 3: Commit**

```bash
git add web/
git commit -m "feat: Dockerfile and docker-compose for deployment"
```

---

## Task 13: Dashboard UI — HTML Shell and Styles

**Files:**
- Create: `web/ui/index.html`
- Create: `web/ui/css/style.css`
- Create: `web/ui/js/app.js`

- [ ] **Step 1: Create dashboard HTML**

Create `web/ui/index.html`:

```html
<!DOCTYPE html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>MailReaper</title>
  <link rel="stylesheet" href="/css/style.css">
</head>
<body>
  <nav class="topnav">
    <div class="nav-left">
      <span class="logo">&#9760; MailReaper</span>
      <a href="/" class="nav-link active" data-page="dashboard">Dashboard</a>
      <a href="/review.html" class="nav-link" data-page="review">Review Queue</a>
      <a href="/rules.html" class="nav-link" data-page="rules">Rules</a>
      <a href="/settings.html" class="nav-link" data-page="settings">Settings</a>
    </div>
    <div class="nav-right">
      <span class="status-dot"></span>
      <span id="last-scan">Loading...</span>
    </div>
  </nav>

  <main class="container">
    <section class="stats-row" id="stats-row"></section>
    <section class="triage-grid" id="triage-grid"></section>
    <div class="two-col">
      <section class="activity-panel" id="activity-panel">
        <div class="panel-header">
          <h3>Recent Activity</h3>
        </div>
        <div class="panel-body" id="activity-list"></div>
      </section>
      <section class="review-panel" id="review-panel">
        <div class="panel-header">
          <h3 id="review-title">Needs Review</h3>
        </div>
        <div class="panel-body" id="review-list"></div>
      </section>
    </div>
  </main>

  <script src="/js/app.js"></script>
  <script src="/js/dashboard.js"></script>
</body>
</html>
```

- [ ] **Step 2: Create CSS**

Create `web/ui/css/style.css`:

```css
:root {
  --bg: #0d1117;
  --surface: #161b22;
  --border: #222;
  --text: #ccc;
  --text-muted: #666;
  --accent: #e8c547;
  --green: #27ae60;
  --red: #e74c3c;
  --blue: #3498db;
  --purple: #9b59b6;
  --orange: #e67e22;
}

* { margin: 0; padding: 0; box-sizing: border-box; }

body {
  background: var(--bg);
  color: var(--text);
  font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
  font-size: 14px;
  line-height: 1.5;
}

.topnav {
  display: flex;
  align-items: center;
  justify-content: space-between;
  padding: 0.75rem 1.5rem;
  background: var(--surface);
  border-bottom: 1px solid var(--border);
}

.nav-left { display: flex; align-items: center; gap: 1.5rem; }
.nav-right { display: flex; align-items: center; gap: 0.75rem; }
.logo { color: var(--accent); font-weight: bold; font-size: 1rem; }

.nav-link {
  color: var(--text-muted);
  text-decoration: none;
  font-size: 0.85rem;
}
.nav-link.active { color: #fff; border-bottom: 2px solid var(--accent); padding-bottom: 2px; }
.nav-link:hover { color: #fff; }

.status-dot {
  width: 8px; height: 8px;
  border-radius: 50%;
  background: var(--green);
  display: inline-block;
}

#last-scan { color: var(--text-muted); font-size: 0.8rem; }

.container { padding: 1.5rem; max-width: 1400px; margin: 0 auto; }

.stats-row {
  display: grid;
  grid-template-columns: repeat(3, 1fr);
  gap: 1rem;
  margin-bottom: 1.5rem;
}

.stat-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 1rem;
  text-align: center;
}
.stat-label { color: var(--text-muted); font-size: 0.75rem; text-transform: uppercase; }
.stat-value { font-size: 1.8rem; font-weight: bold; margin: 0.3rem 0; }
.stat-detail { color: var(--text-muted); font-size: 0.7rem; }

.triage-grid {
  display: grid;
  grid-template-columns: repeat(auto-fill, minmax(200px, 1fr));
  gap: 0.75rem;
  margin-bottom: 1.5rem;
}

.triage-card {
  border: 1px solid var(--border);
  border-radius: 8px;
  padding: 1rem;
  cursor: pointer;
  transition: border-color 0.2s;
}
.triage-card:hover { border-color: var(--text-muted); }
.triage-card-header {
  display: flex;
  justify-content: space-between;
  align-items: center;
  margin-bottom: 0.5rem;
}
.triage-card-name { font-weight: bold; font-size: 0.85rem; }
.triage-card-count { color: #fff; font-weight: bold; }
.triage-card-detail { color: var(--text-muted); font-size: 0.7rem; }

.two-col {
  display: grid;
  grid-template-columns: 3fr 2fr;
  gap: 1rem;
}

.activity-panel, .review-panel {
  border: 1px solid var(--border);
  border-radius: 8px;
  overflow: hidden;
}

.panel-header {
  padding: 0.75rem 1rem;
  border-bottom: 1px solid var(--border);
}
.panel-header h3 { font-size: 0.85rem; font-weight: bold; }
.panel-body { font-size: 0.8rem; }

.activity-row {
  padding: 0.5rem 1rem;
  border-bottom: 1px solid #181818;
  display: flex;
  justify-content: space-between;
}

.review-item {
  padding: 0.6rem 1rem;
  border-bottom: 1px solid #181818;
}
.review-item-subject { color: var(--text); margin-bottom: 0.2rem; }
.review-item-meta { color: var(--text-muted); font-size: 0.75rem; margin-bottom: 0.4rem; }
.review-actions { display: flex; gap: 0.4rem; }

.btn {
  padding: 2px 8px;
  border-radius: 4px;
  font-size: 0.7rem;
  border: none;
  cursor: pointer;
}
.btn-accept { background: var(--green); color: #fff; }
.btn-reject { background: #333; color: #aaa; }
.btn-accept:hover { background: #2ecc71; }
.btn-reject:hover { background: #444; }
```

- [ ] **Step 3: Create shared JS**

Create `web/ui/js/app.js`:

```javascript
async function api(path, options = {}) {
  const resp = await fetch('/api' + path, {
    headers: { 'Content-Type': 'application/json' },
    ...options,
  });
  if (!resp.ok) {
    const err = await resp.json().catch(() => ({ error: resp.statusText }));
    throw new Error(err.error || resp.statusText);
  }
  if (resp.status === 204) return null;
  return resp.json();
}

function connectSSE(onEvent) {
  const source = new EventSource('/api/events');
  source.onopen = () => console.log('[MailReaper] SSE connected');
  source.onerror = () => {
    console.warn('[MailReaper] SSE disconnected, reconnecting...');
  };
  source.addEventListener('scan_complete', (e) => onEvent('scan_complete', JSON.parse(e.data)));
  source.addEventListener('verdict', (e) => onEvent('verdict', JSON.parse(e.data)));
  source.addEventListener('correction', (e) => onEvent('correction', JSON.parse(e.data)));
  return source;
}

function timeAgo(dateStr) {
  const diff = Date.now() - new Date(dateStr).getTime();
  const mins = Math.floor(diff / 60000);
  if (mins < 1) return 'just now';
  if (mins < 60) return mins + 'm';
  const hours = Math.floor(mins / 60);
  if (hours < 24) return hours + 'h';
  return Math.floor(hours / 24) + 'd';
}
```

- [ ] **Step 4: Create dashboard JS**

Create `web/ui/js/dashboard.js`:

```javascript
async function loadDashboard() {
  const [stats, activity, pending, categories] = await Promise.all([
    api('/stats'),
    api('/activity?limit=10'),
    api('/verdicts/pending'),
    api('/categories'),
  ]);

  renderStats(stats, pending);
  renderTriageGrid(categories);
  renderActivity(activity);
  renderReviewQueue(pending);
}

function renderStats(stats, pending) {
  const el = document.getElementById('stats-row');
  el.innerHTML = `
    <div class="stat-card">
      <div class="stat-label">Triaged Today</div>
      <div class="stat-value" style="color: var(--green)">${stats.todayMoved}</div>
      <div class="stat-detail">auto + approved</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Awaiting Review</div>
      <div class="stat-value" style="color: var(--accent)">${pending.length}</div>
      <div class="stat-detail">low confidence</div>
    </div>
    <div class="stat-card">
      <div class="stat-label">Corrections</div>
      <div class="stat-value" style="color: var(--red)">${stats.todayCorrected}</div>
      <div class="stat-detail">moved back to inbox</div>
    </div>
  `;
}

function renderTriageGrid(categories) {
  const el = document.getElementById('triage-grid');
  if (!categories.length) {
    el.innerHTML = '<p style="color: var(--text-muted); padding: 1rem;">No categories configured yet.</p>';
    return;
  }
  el.innerHTML = categories.map(cat => `
    <div class="triage-card">
      <div class="triage-card-header">
        <span class="triage-card-name" style="color: ${cat.color}">${cat.icon} ${cat.name}</span>
        <span class="triage-card-count">-</span>
      </div>
      <div class="triage-card-detail">${cat.folderName}</div>
    </div>
  `).join('');
}

function renderActivity(entries) {
  const el = document.getElementById('activity-list');
  if (!entries.length) {
    el.innerHTML = '<p style="color: var(--text-muted); padding: 1rem;">No activity yet.</p>';
    return;
  }
  el.innerHTML = entries.map(e => {
    const color = e.type === 'corrected' ? 'var(--red)' : 'var(--green)';
    const label = e.destination || e.type;
    return `
      <div class="activity-row">
        <div>
          <span style="color: ${color}">\u2192 ${label}</span>
          <span>${e.subject || ''}</span>
          <span style="color: var(--text-muted)"> \u00b7 ${e.sender || ''}</span>
        </div>
        <div style="color: var(--text-muted)">${timeAgo(e.createdAt)}</div>
      </div>
    `;
  }).join('');
}

function renderReviewQueue(pending) {
  const titleEl = document.getElementById('review-title');
  titleEl.textContent = `Needs Review (${pending.length})`;

  const el = document.getElementById('review-list');
  if (!pending.length) {
    el.innerHTML = '<p style="color: var(--text-muted); padding: 1rem;">Nothing to review.</p>';
    return;
  }
  el.innerHTML = pending.map(v => `
    <div class="review-item">
      <div class="review-item-subject">${v.subject || '(no subject)'}</div>
      <div class="review-item-meta">
        LLM suggests: <strong style="color: var(--accent)">${v.destinationFolder || 'Expired'}</strong>
        \u00b7 conf ${(v.confidence || 0).toFixed(2)}
      </div>
      <div class="review-actions">
        <button class="btn btn-accept" onclick="approveVerdict('${encodeURIComponent(v.messageIdHeader)}')">Accept</button>
        <button class="btn btn-reject" onclick="rejectVerdict('${encodeURIComponent(v.messageIdHeader)}')">Skip</button>
      </div>
    </div>
  `).join('');
}

async function approveVerdict(encodedMsgId) {
  await api(`/verdicts/${encodedMsgId}/status`, {
    method: 'PUT',
    body: JSON.stringify({ status: 'approved' }),
  });
  loadDashboard();
}

async function rejectVerdict(encodedMsgId) {
  await api(`/verdicts/${encodedMsgId}/status`, {
    method: 'PUT',
    body: JSON.stringify({ status: 'rejected' }),
  });
  loadDashboard();
}

// Initialize
loadDashboard();

// SSE for live updates
connectSSE((event, data) => {
  console.log('[MailReaper] SSE event:', event, data);
  loadDashboard();
});
```

- [ ] **Step 5: Commit**

```bash
git add web/
git commit -m "feat: dashboard UI with stats, triage grid, activity feed, and review queue"
```

---

## Task 14: Review Queue, Rules, and Settings Pages

**Files:**
- Create: `web/ui/review.html`
- Create: `web/ui/rules.html`
- Create: `web/ui/settings.html`
- Create: `web/ui/js/review.js`
- Create: `web/ui/js/rules.js`
- Create: `web/ui/js/settings.js`

This task creates the remaining three pages. Each follows the same pattern: HTML shell with the shared nav, a page-specific JS file that fetches data from the API and renders it.

- [ ] **Step 1: Create review queue page**

Create `web/ui/review.html` with the same nav header as index.html (with "Review Queue" link active), a sortable table of pending verdicts with accept/change/skip controls, and batch operations.

Create `web/ui/js/review.js` that fetches `/api/verdicts/pending`, renders the full table, and handles approve/reject with `/api/verdicts/{id}/status`.

- [ ] **Step 2: Create rules management page**

Create `web/ui/rules.html` with a rules list sorted by priority, enable/disable toggles, and a form for creating new rules with fields: name, sender patterns, subject patterns, expiration type, hours, destination folder, grace period days.

Create `web/ui/js/rules.js` that fetches `/api/rules`, renders the list, and handles CRUD via the API.

- [ ] **Step 3: Create settings page**

Create `web/ui/settings.html` with sections for: account status (read-only from the API), LLM provider display, scan schedule display, category management (add/edit/delete), and training examples browser.

Create `web/ui/js/settings.js` that fetches categories and training examples and handles CRUD.

- [ ] **Step 4: Commit**

```bash
git add web/
git commit -m "feat: review queue, rules, and settings pages"
```

---

## Task 15: Run All Tests and Integration Verification

**Files:** None new — verification only

- [ ] **Step 1: Run full test suite**

```bash
cd web && go test ./... -v
```

Expected: all tests PASS

- [ ] **Step 2: Verify build**

```bash
cd web && go build -o mailreaper ./cmd/mailreaper/
```

Expected: clean build, produces `mailreaper` binary

- [ ] **Step 3: Verify Docker build**

```bash
cd web && docker build -t mailreaper .
```

Expected: builds successfully

- [ ] **Step 4: Update .gitignore**

Add to project root `.gitignore`:

```
.superpowers/
web/mailreaper
```

- [ ] **Step 5: Final commit**

```bash
git add .
git commit -m "chore: verify full test suite and Docker build"
```
