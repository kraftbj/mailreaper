package db_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/kraftbj/mailreaper/internal/db"
)

func TestOpenAndMigrate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.db")

	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	tables := []string{
		"accounts",
		"rules",
		"verdicts",
		"activity_log",
		"llm_cache",
		"training_examples",
		"categories",
	}

	for _, table := range tables {
		var name string
		err := database.QueryRow(
			"SELECT name FROM sqlite_master WHERE type='table' AND name=?",
			table,
		).Scan(&name)
		if err != nil {
			t.Errorf("table %q not found: %v", table, err)
		}
	}
}

func TestOpenCreatesDirectory(t *testing.T) {
	dir := t.TempDir()
	nested := filepath.Join(dir, "subdir", "nested")

	// Verify the nested dir does not exist yet.
	if _, err := os.Stat(nested); !os.IsNotExist(err) {
		t.Fatalf("expected nested dir to not exist before Open()")
	}

	path := filepath.Join(nested, "test.db")
	database, err := db.Open(path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer database.Close()

	if _, err := os.Stat(nested); os.IsNotExist(err) {
		t.Errorf("expected Open() to create directory %q", nested)
	}
}
