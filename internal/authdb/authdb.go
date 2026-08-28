// SPDX-License-Identifier: AGPL-3.0-or-later

// Package authdb gestisce auth.db: i testi delle Pagine Protette.
// Sola lettura per l'applicazione; scritto solo da `duckline -task=pull`.
package authdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"duckline/internal/db"
)

var ErrNotFound = errors.New("pagina non trovata")

type Page struct {
	ID     string
	Titolo string
	Testo  string // Markdown, convertito in HTML a runtime dall'handler
}

type Store struct{ db *sql.DB }

func Open(path string) (*Store, error) {
	d, err := db.OpenReadOnly(path)
	if err != nil {
		return nil, err
	}
	return &Store{db: d}, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) Page(ctx context.Context, id string) (Page, error) {
	var p Page
	err := s.db.QueryRowContext(ctx,
		`SELECT id, titolo, testo FROM pages WHERE id = ?`, id).
		Scan(&p.ID, &p.Titolo, &p.Testo)
	if errors.Is(err, sql.ErrNoRows) {
		return Page{}, ErrNotFound
	}
	return p, err
}

// Rebuild riscrive integralmente auth.db (solo -task=pull).
func Rebuild(ctx context.Context, path string, pages []Page) error {
	d, err := db.OpenReadWrite(path)
	if err != nil {
		return err
	}
	defer d.Close()

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmts := []string{
		`DROP TABLE IF EXISTS pages`,
		`CREATE TABLE pages (
			id     TEXT PRIMARY KEY,
			titolo TEXT NOT NULL DEFAULT '',
			testo  TEXT NOT NULL
		)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("schema auth.db: %w", err)
		}
	}
	for _, p := range pages {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pages (id, titolo, testo) VALUES (?, ?, ?)`,
			p.ID, p.Titolo, p.Testo); err != nil {
			return fmt.Errorf("pagina protetta %q: %w", p.ID, err)
		}
	}
	return tx.Commit()
}
