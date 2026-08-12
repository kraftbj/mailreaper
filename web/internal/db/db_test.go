package db_test

import (
	"context"
	"database/sql"
	"os"
	"path/filepath"
	"strings"
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

// TestOpenAppliesPragmasToPooledConnections asserts the settings Open's doc
// comment promises. modernc.org/sqlite ignores _journal_mode/_busy_timeout
// DSN params, and a one-off PRAGMA Exec only configures the single
// connection that ran it, so this must be checked on a fresh connection.
func TestOpenAppliesPragmasToPooledConnections(t *testing.T) {
	d := openTestDB(t)

	/* Hold DISTINCT connections open simultaneously. A loop of QueryRow on
	the pool proves nothing -- it can hand back the same connection every
	time, which is exactly the connection a one-off setup PRAGMA would
	have configured. Only concurrently-held conns force the pool to open
	new ones. */
	ctx := context.Background()
	var conns []*sql.Conn
	for i := 0; i < 3; i++ {
		c, err := d.Conn(ctx)
		if err != nil {
			t.Fatalf("open conn %d: %v", i, err)
		}
		defer c.Close()
		conns = append(conns, c)
	}

	for i, c := range conns {
		var journal string
		if err := c.QueryRowContext(ctx, "PRAGMA journal_mode").Scan(&journal); err != nil {
			t.Fatalf("conn %d journal_mode: %v", i, err)
		}
		if !strings.EqualFold(journal, "wal") {
			t.Errorf("conn %d journal_mode = %q, want wal", i, journal)
		}

		var busy int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatalf("conn %d busy_timeout: %v", i, err)
		}
		if busy != 5000 {
			t.Errorf("conn %d busy_timeout = %d, want 5000", i, busy)
		}

		var fk int
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatalf("conn %d foreign_keys: %v", i, err)
		}
		if fk != 1 {
			t.Errorf("conn %d foreign_keys = %d, want 1", i, fk)
		}
	}
}
