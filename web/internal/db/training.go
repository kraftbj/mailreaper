package db

import (
	"fmt"
	"time"
)

// TrainingExample is a labelled example used to fine-tune or prompt LLM classification.
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

const maxTrainingExamplesPerCategory = 50

// AddTrainingExample inserts a new training example. If adding it would exceed
// the per-category cap, the oldest entry in that category is removed first.
func (d *DB) AddTrainingExample(ex TrainingExample) error {
	var count int
	if err := d.QueryRow(`SELECT COUNT(*) FROM training_examples WHERE category = ?`, ex.Category).Scan(&count); err != nil {
		return fmt.Errorf("db: count training examples: %w", err)
	}

	if count >= maxTrainingExamplesPerCategory {
		_, err := d.Exec(`
			DELETE FROM training_examples
			WHERE id = (SELECT id FROM training_examples WHERE category = ? ORDER BY created_at ASC LIMIT 1)
		`, ex.Category)
		if err != nil {
			return fmt.Errorf("db: trim training examples: %w", err)
		}
	}

	_, err := d.Exec(`
		INSERT INTO training_examples (category, subject, sender, body_snippet, label, source, created_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
	`,
		ex.Category, ex.Subject, ex.Sender, ex.BodySnippet,
		ex.Label, ex.Source, formatDBTime(time.Now().UTC()),
	)
	if err != nil {
		return fmt.Errorf("db: add training example: %w", err)
	}
	return nil
}

// GetTrainingExamples returns all training examples. If category is non-empty,
// only examples for that category are returned.
func (d *DB) GetTrainingExamples(category string) ([]TrainingExample, error) {
	var (
		q    string
		args []any
	)

	if category != "" {
		q = `SELECT id, category, subject, sender, body_snippet, label, source, created_at
		     FROM training_examples WHERE category = ? ORDER BY created_at ASC`
		args = []any{category}
	} else {
		q = `SELECT id, category, subject, sender, body_snippet, label, source, created_at
		     FROM training_examples ORDER BY created_at ASC`
	}

	rows, err := d.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("db: get training examples: %w", err)
	}
	defer rows.Close()

	var examples []TrainingExample
	for rows.Next() {
		var ex TrainingExample
		var createdAtStr string
		if err := rows.Scan(
			&ex.ID, &ex.Category, &ex.Subject, &ex.Sender,
			&ex.BodySnippet, &ex.Label, &ex.Source, &createdAtStr,
		); err != nil {
			return nil, fmt.Errorf("db: scan training example: %w", err)
		}
		if ex.CreatedAt, err = parseDBTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("db: parse training created_at: %w", err)
		}
		examples = append(examples, ex)
	}
	return examples, rows.Err()
}

// DeleteTrainingExample removes a training example by ID.
func (d *DB) DeleteTrainingExample(id int) error {
	_, err := d.Exec(`DELETE FROM training_examples WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete training example: %w", err)
	}
	return nil
}

// ClearTrainingExamples removes all training examples. If category is non-empty,
// only examples for that category are removed.
func (d *DB) ClearTrainingExamples(category string) error {
	var err error
	if category != "" {
		_, err = d.Exec(`DELETE FROM training_examples WHERE category = ?`, category)
	} else {
		_, err = d.Exec(`DELETE FROM training_examples`)
	}
	if err != nil {
		return fmt.Errorf("db: clear training examples: %w", err)
	}
	return nil
}
