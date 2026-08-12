package config

import (
	"os"
	"path/filepath"
	"testing"
)

func writeTemp(t *testing.T, content string) string {
	t.Helper()
	f, err := os.CreateTemp(t.TempDir(), "config-*.yaml")
	if err != nil {
		t.Fatalf("creating temp file: %v", err)
	}
	if _, err := f.WriteString(content); err != nil {
		t.Fatalf("writing temp file: %v", err)
	}
	f.Close()
	return f.Name()
}

func TestLoad_BasicConfig(t *testing.T) {
	yaml := `
accounts:
  - name: Personal
    host: imap.example.com
    port: 993
    username: user@example.com
    password: secret
    tls: true
    folders:
      scan:
        - INBOX
        - Newsletters

llm:
  provider: gemini
  gemini:
    api_key: gemini-api-key
    model: gemini-2.0-flash

scan:
  interval_minutes: 15
  min_message_age_min: 30
  max_messages_per_scan: 50

server:
  port: 9090
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if len(cfg.Accounts) != 1 {
		t.Fatalf("expected 1 account, got %d", len(cfg.Accounts))
	}
	a := cfg.Accounts[0]
	if a.Name != "Personal" {
		t.Errorf("account name: got %q, want %q", a.Name, "Personal")
	}
	if a.Host != "imap.example.com" {
		t.Errorf("host: got %q, want %q", a.Host, "imap.example.com")
	}
	if a.Port != 993 {
		t.Errorf("port: got %d, want %d", a.Port, 993)
	}
	if a.Username != "user@example.com" {
		t.Errorf("username: got %q", a.Username)
	}
	if a.Password != "secret" {
		t.Errorf("password: got %q", a.Password)
	}
	if !a.TLS {
		t.Error("tls: expected true")
	}
	if len(a.Folders.Scan) != 2 || a.Folders.Scan[0] != "INBOX" {
		t.Errorf("folders.scan: got %v", a.Folders.Scan)
	}

	if cfg.LLM.Provider != "gemini" {
		t.Errorf("llm.provider: got %q", cfg.LLM.Provider)
	}
	if cfg.LLM.Gemini.APIKey != "gemini-api-key" {
		t.Errorf("llm.gemini.api_key: got %q", cfg.LLM.Gemini.APIKey)
	}
	if cfg.LLM.Gemini.Model != "gemini-2.0-flash" {
		t.Errorf("llm.gemini.model: got %q", cfg.LLM.Gemini.Model)
	}

	if cfg.Scan.IntervalMinutes != 15 {
		t.Errorf("scan.interval_minutes: got %d", cfg.Scan.IntervalMinutes)
	}
	if cfg.Scan.MinMessageAgeMin != 30 {
		t.Errorf("scan.min_message_age_min: got %d", cfg.Scan.MinMessageAgeMin)
	}
	if cfg.Scan.MaxMessagesPerScan != 50 {
		t.Errorf("scan.max_messages_per_scan: got %d", cfg.Scan.MaxMessagesPerScan)
	}

	if cfg.Server.Port != 9090 {
		t.Errorf("server.port: got %d, want %d", cfg.Server.Port, 9090)
	}
}

func TestLoad_EnvVarSubstitution(t *testing.T) {
	t.Setenv("IMAP_HOST", "mail.example.com")
	t.Setenv("IMAP_PASS", "supersecret")
	t.Setenv("GEMINI_KEY", "key-from-env")

	yaml := `
accounts:
  - name: Work
    host: ${IMAP_HOST}
    port: 993
    username: work@example.com
    password: ${IMAP_PASS}
    tls: true
    folders:
      scan:
        - INBOX

llm:
  provider: gemini
  gemini:
    api_key: ${GEMINI_KEY}
    model: gemini-2.0-flash
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Accounts[0].Host != "mail.example.com" {
		t.Errorf("host env substitution: got %q, want %q", cfg.Accounts[0].Host, "mail.example.com")
	}
	if cfg.Accounts[0].Password != "supersecret" {
		t.Errorf("password env substitution: got %q, want %q", cfg.Accounts[0].Password, "supersecret")
	}
	if cfg.LLM.Gemini.APIKey != "key-from-env" {
		t.Errorf("gemini api_key env substitution: got %q, want %q", cfg.LLM.Gemini.APIKey, "key-from-env")
	}
}

func TestLoad_Defaults(t *testing.T) {
	yaml := `
accounts:
  - name: Test
    host: imap.example.com
    port: 993
    username: u
    password: p
    tls: true
    folders:
      scan:
        - INBOX
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.Scan.IntervalMinutes != 30 {
		t.Errorf("default scan.interval_minutes: got %d, want 30", cfg.Scan.IntervalMinutes)
	}
	if cfg.Scan.MinMessageAgeMin != 60 {
		t.Errorf("default scan.min_message_age_min: got %d, want 60", cfg.Scan.MinMessageAgeMin)
	}
	if cfg.Scan.MaxMessagesPerScan != 100 {
		t.Errorf("default scan.max_messages_per_scan: got %d, want 100", cfg.Scan.MaxMessagesPerScan)
	}
	if cfg.Server.Port != 8025 {
		t.Errorf("default server.port: got %d, want 8025", cfg.Server.Port)
	}
}

func TestDefaultBindAddrIsLoopback(t *testing.T) {
	cfg := &Config{}
	applyDefaults(cfg)
	if cfg.Server.BindAddr != "127.0.0.1" {
		t.Errorf("BindAddr = %q, want 127.0.0.1", cfg.Server.BindAddr)
	}
}

func TestExplicitBindAddrIsPreserved(t *testing.T) {
	cfg := &Config{Server: ServerConfig{BindAddr: "0.0.0.0"}}
	applyDefaults(cfg)
	if cfg.Server.BindAddr != "0.0.0.0" {
		t.Errorf("BindAddr = %q, want 0.0.0.0", cfg.Server.BindAddr)
	}
}

func TestLoad_FileNotFound(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err == nil {
		t.Error("expected error for missing file, got nil")
	}
}

func TestLoad_OllamaConfig(t *testing.T) {
	yaml := `
accounts:
  - name: Test
    host: imap.example.com
    port: 993
    username: u
    password: p
    tls: true
    folders:
      scan:
        - INBOX

llm:
  provider: ollama
  ollama:
    endpoint: http://localhost:11434
    model: llama3
`
	path := writeTemp(t, yaml)
	cfg, err := Load(path)
	if err != nil {
		t.Fatalf("Load() error: %v", err)
	}

	if cfg.LLM.Provider != "ollama" {
		t.Errorf("llm.provider: got %q, want ollama", cfg.LLM.Provider)
	}
	if cfg.LLM.Ollama.Endpoint != "http://localhost:11434" {
		t.Errorf("llm.ollama.endpoint: got %q", cfg.LLM.Ollama.Endpoint)
	}
	if cfg.LLM.Ollama.Model != "llama3" {
		t.Errorf("llm.ollama.model: got %q", cfg.LLM.Ollama.Model)
	}
}
