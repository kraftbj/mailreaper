package db

import (
	"fmt"
	"time"
)

// Category represents a message classification bucket (e.g. "Paper-Trail").
type Category struct {
	ID         string `json:"id"`
	Name       string `json:"name"`
	FolderName string `json:"folderName"`
	Icon       string `json:"icon"`
	Color      string `json:"color"`
	/* CheckDeadlines marks the category as perishable: mail routed into its
	folder gets a deadline-extraction pass even when the routing rule
	supplied no deadline. Off by default -- see migrateCategoryCheckDeadlines
	for why. */
	CheckDeadlines bool      `json:"checkDeadlines"`
	CreatedAt      time.Time `json:"createdAt"`
}

// GetCategories returns all categories ordered by name.
func (d *DB) GetCategories() ([]Category, error) {
	rows, err := d.Query(`
		SELECT id, name, folder_name, COALESCE(icon, ''), COALESCE(color, ''),
		       created_at, COALESCE(check_deadlines, 0)
		FROM categories
		ORDER BY name ASC
	`)
	if err != nil {
		return nil, fmt.Errorf("db: get categories: %w", err)
	}
	defer rows.Close()

	var cats []Category
	for rows.Next() {
		var c Category
		var createdAtStr string
		/* Scanned as an int rather than straight into the bool field:
		SQLite has no boolean type, and relying on the driver's int64->bool
		conversion is a portability bet with nothing to gain. */
		var checkDeadlines int
		if err := rows.Scan(&c.ID, &c.Name, &c.FolderName, &c.Icon, &c.Color, &createdAtStr, &checkDeadlines); err != nil {
			return nil, fmt.Errorf("db: scan category: %w", err)
		}
		c.CheckDeadlines = checkDeadlines != 0
		if c.CreatedAt, err = parseDBTime(createdAtStr); err != nil {
			return nil, fmt.Errorf("db: parse category created_at: %w", err)
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// SaveCategory upserts a category.
func (d *DB) SaveCategory(c Category) error {
	now := time.Now().UTC()
	checkDeadlines := 0
	if c.CheckDeadlines {
		checkDeadlines = 1
	}
	_, err := d.Exec(`
		INSERT INTO categories (id, name, folder_name, icon, color, created_at, check_deadlines)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name            = excluded.name,
			folder_name     = excluded.folder_name,
			icon            = excluded.icon,
			color           = excluded.color,
			check_deadlines = excluded.check_deadlines
	`, c.ID, c.Name, c.FolderName, c.Icon, c.Color, formatDBTime(now), checkDeadlines)
	if err != nil {
		return fmt.Errorf("db: save category: %w", err)
	}
	return nil
}

// DeleteCategory removes a category by ID.
func (d *DB) DeleteCategory(id string) error {
	_, err := d.Exec(`DELETE FROM categories WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("db: delete category: %w", err)
	}
	return nil
}
