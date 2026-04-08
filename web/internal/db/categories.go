package db

import (
	"fmt"
	"time"
)

// Category represents a message classification bucket (e.g. "Paper-Trail").
type Category struct {
	ID         string    `json:"id"`
	Name       string    `json:"name"`
	FolderName string    `json:"folderName"`
	Icon       string    `json:"icon"`
	Color      string    `json:"color"`
	CreatedAt  time.Time `json:"createdAt"`
}

// GetCategories returns all categories ordered by name.
func (d *DB) GetCategories() ([]Category, error) {
	rows, err := d.Query(`
		SELECT id, name, folder_name, icon, color, created_at
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
		if err := rows.Scan(&c.ID, &c.Name, &c.FolderName, &c.Icon, &c.Color, &c.CreatedAt); err != nil {
			return nil, fmt.Errorf("db: scan category: %w", err)
		}
		cats = append(cats, c)
	}
	return cats, rows.Err()
}

// SaveCategory upserts a category.
func (d *DB) SaveCategory(c Category) error {
	now := time.Now().UTC()
	_, err := d.Exec(`
		INSERT INTO categories (id, name, folder_name, icon, color, created_at)
		VALUES (?, ?, ?, ?, ?, ?)
		ON CONFLICT(id) DO UPDATE SET
			name        = excluded.name,
			folder_name = excluded.folder_name,
			icon        = excluded.icon,
			color       = excluded.color
	`, c.ID, c.Name, c.FolderName, c.Icon, c.Color, now)
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
