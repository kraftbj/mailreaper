package config

import (
	"fmt"
	"os"
	"regexp"

	"gopkg.in/yaml.v3"
)

// AccountFolders holds the folder configuration for an account.
type AccountFolders struct {
	Scan []string `yaml:"scan"`
}

// Account represents a single IMAP account configuration.
type Account struct {
	Name     string         `yaml:"name"`
	Host     string         `yaml:"host"`
	Port     int            `yaml:"port"`
	Username string         `yaml:"username"`
	Password string         `yaml:"password"`
	TLS      bool           `yaml:"tls"`
	Folders  AccountFolders `yaml:"folders"`
}

// GeminiConfig holds Gemini LLM settings.
type GeminiConfig struct {
	APIKey      string `yaml:"api_key"`
	Model       string `yaml:"model"`
	ServiceTier string `yaml:"service_tier"` // "flex" for 50% cost reduction
}

// OllamaConfig holds Ollama LLM settings.
type OllamaConfig struct {
	Endpoint string `yaml:"endpoint"`
	Model    string `yaml:"model"`
}

// LLMConfig holds LLM provider configuration.
type LLMConfig struct {
	Provider string       `yaml:"provider"`
	Gemini   GeminiConfig `yaml:"gemini"`
	Ollama   OllamaConfig `yaml:"ollama"`
}

// ScanConfig holds mail scanning parameters.
type ScanConfig struct {
	IntervalMinutes    int `yaml:"interval_minutes"`
	MinMessageAgeMin   int `yaml:"min_message_age_min"`
	MaxMessagesPerScan int `yaml:"max_messages_per_scan"`
}

// ServerConfig holds HTTP server settings.
type ServerConfig struct {
	Port     int    `yaml:"port"`
	BindAddr string `yaml:"bind_addr"`
}

// Config is the top-level configuration structure.
type Config struct {
	Accounts []Account    `yaml:"accounts"`
	LLM      LLMConfig    `yaml:"llm"`
	Scan     ScanConfig   `yaml:"scan"`
	Server   ServerConfig `yaml:"server"`
}

var envVarRe = regexp.MustCompile(`\$\{([^}]+)\}`)

// substituteEnv replaces ${VAR_NAME} patterns with the corresponding
// environment variable values.
func substituteEnv(s string) string {
	return envVarRe.ReplaceAllStringFunc(s, func(match string) string {
		name := envVarRe.FindStringSubmatch(match)[1]
		if val, ok := os.LookupEnv(name); ok {
			return val
		}
		return match
	})
}

// applyDefaults sets default values for fields that were not explicitly set.
func applyDefaults(cfg *Config) {
	if cfg.Scan.IntervalMinutes == 0 {
		cfg.Scan.IntervalMinutes = 30
	}
	if cfg.Scan.MinMessageAgeMin == 0 {
		cfg.Scan.MinMessageAgeMin = 60
	}
	if cfg.Scan.MaxMessagesPerScan == 0 {
		cfg.Scan.MaxMessagesPerScan = 100
	}
	if cfg.Server.Port == 0 {
		cfg.Server.Port = 8025
	}
	if cfg.Server.BindAddr == "" {
		cfg.Server.BindAddr = "127.0.0.1"
	}
}

// Load reads a YAML config file at path, substitutes ${ENV_VAR} references,
// and applies defaults for any omitted fields.
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config file %q: %w", path, err)
	}

	expanded := substituteEnv(string(raw))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parsing config file %q: %w", path, err)
	}

	applyDefaults(&cfg)
	return &cfg, nil
}
