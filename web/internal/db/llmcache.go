package db

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// CachedVerdict is the LLM verdict stored in the cache.
//
// The schema reflects the post-rewrite design: the LLM extracts deadlines
// (ExpiresAt) and may classify into a category (Classified). It does NOT
// decide whether a deadline has passed — that comparison is done in Go.
type CachedVerdict struct {
	Classified bool    `json:"classified,omitempty"`
	ExpiresAt  string  `json:"expiresAt,omitempty"`
	Reason     string  `json:"reason,omitempty"`
	Confidence float64 `json:"confidence,omitempty"`
	Error      string  `json:"error,omitempty"`
}

// ttl returns the cache duration for a given verdict.
//
//	- error verdicts: 10 minutes (retry sooner)
//	- verdicts that took action (classified or extracted a deadline): 24 hours
//	- verdicts that found nothing actionable: 7 days
func (cv *CachedVerdict) ttl() time.Duration {
	if cv.Error != "" {
		return 10 * time.Minute
	}
	if cv.Classified || cv.ExpiresAt != "" {
		return 24 * time.Hour
	}
	return 7 * 24 * time.Hour
}

// GetCachedVerdict retrieves a cached LLM verdict for the given Message-ID header.
// Returns nil (no error) if there is no cached entry or if it has expired.
func (d *DB) GetCachedVerdict(messageIDHeader string) (*CachedVerdict, error) {
	var verdictJSON string
	var cachedAtStr string

	err := d.QueryRow(`
		SELECT verdict, cached_at
		FROM llm_cache
		WHERE message_id_header = ?
	`, messageIDHeader).Scan(&verdictJSON, &cachedAtStr)

	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("db: get cached verdict: %w", err)
	}

	cachedAt, err := parseDBTime(cachedAtStr)
	if err != nil {
		return nil, fmt.Errorf("db: parse cached_at: %w", err)
	}

	var cv CachedVerdict
	if err := json.Unmarshal([]byte(verdictJSON), &cv); err != nil {
		return nil, fmt.Errorf("db: unmarshal cached verdict: %w", err)
	}

	if time.Since(cachedAt) > cv.ttl() {
		// Expired — remove it and return a miss.
		_ = d.RemoveCachedVerdict(messageIDHeader)
		return nil, nil
	}

	return &cv, nil
}

// SetCachedVerdict upserts a cached verdict for the given Message-ID header.
func (d *DB) SetCachedVerdict(messageIDHeader string, cv CachedVerdict) error {
	verdictJSON, err := json.Marshal(cv)
	if err != nil {
		return fmt.Errorf("db: marshal cached verdict: %w", err)
	}

	_, err = d.Exec(`
		INSERT INTO llm_cache (message_id_header, verdict, cached_at)
		VALUES (?, ?, ?)
		ON CONFLICT(message_id_header) DO UPDATE SET
			verdict   = excluded.verdict,
			cached_at = excluded.cached_at
	`, messageIDHeader, string(verdictJSON), formatDBTime(time.Now().UTC()))
	if err != nil {
		return fmt.Errorf("db: set cached verdict: %w", err)
	}
	return nil
}

// RemoveCachedVerdict deletes the cached verdict for the given Message-ID header.
func (d *DB) RemoveCachedVerdict(messageIDHeader string) error {
	_, err := d.Exec(`DELETE FROM llm_cache WHERE message_id_header = ?`, messageIDHeader)
	if err != nil {
		return fmt.Errorf("db: remove cached verdict: %w", err)
	}
	return nil
}

// ClearLLMCache deletes all entries from the LLM verdict cache.
func (d *DB) ClearLLMCache() error {
	_, err := d.Exec(`DELETE FROM llm_cache`)
	if err != nil {
		return fmt.Errorf("db: clear llm cache: %w", err)
	}
	return nil
}
