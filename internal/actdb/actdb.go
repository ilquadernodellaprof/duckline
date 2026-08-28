// SPDX-License-Identifier: AGPL-3.0-or-later

// Package actdb gestisce act.db: esercizi, opzioni e correzioni (in
// Markdown, convertite in HTML solo a runtime dall'handler).
//
// Il database è in sola lettura per l'applicazione; l'unico scrittore è
// `duckline -task=pull` (funzione Rebuild), che gira come processo separato.
//
// Tutti gli ID (pagina, domanda, opzione) sono stringhe, mai interi:
// coerente con act.js, che li tratta già come stringhe lato client.
package actdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"duckline/internal/db"
)

// ErrNotFound segnala pagina o domanda inesistente: per il contratto di
// act.js è un errore applicativo (HTTP 200 con campo "error"), non un 4xx.
var ErrNotFound = errors.New("non trovato")

type Option struct {
	ID    string
	Testo string
}

type Question struct {
	ID      string
	Titolo  string
	Opzioni []Option
}

// Verdict è ciò che serve per giudicare una risposta.
type Verdict struct {
	Corretta   string // ID dell'opzione giusta
	Correzione string // Markdown, convertito in HTML solo se lo status è "stop"
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

// PageExists distingue "pagina inesistente" da "pagina senza domande".
func (s *Store) PageExists(ctx context.Context, pageID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx, `SELECT 1 FROM pages WHERE id = ?`, pageID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Questions restituisce le domande di una pagina nell'ordine d'autore,
// con le rispettive opzioni. ErrNotFound se la pagina non esiste.
func (s *Store) Questions(ctx context.Context, pageID string) ([]Question, error) {
	ok, err := s.PageExists(ctx, pageID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}

	rows, err := s.db.QueryContext(ctx,
		`SELECT id, titolo FROM questions WHERE page_id = ? ORDER BY position`, pageID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var questions []Question
	index := map[string]int{}
	for rows.Next() {
		var q Question
		if err := rows.Scan(&q.ID, &q.Titolo); err != nil {
			return nil, err
		}
		index[q.ID] = len(questions)
		questions = append(questions, q)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	orows, err := s.db.QueryContext(ctx,
		`SELECT question_id, id, testo FROM options WHERE page_id = ? ORDER BY question_id, position`,
		pageID)
	if err != nil {
		return nil, err
	}
	defer orows.Close()

	for orows.Next() {
		var qID string
		var o Option
		if err := orows.Scan(&qID, &o.ID, &o.Testo); err != nil {
			return nil, err
		}
		if i, ok := index[qID]; ok {
			questions[i].Opzioni = append(questions[i].Opzioni, o)
		}
	}
	return questions, orows.Err()
}

// VerdictFor recupera opzione corretta e correzione di una domanda.
func (s *Store) VerdictFor(ctx context.Context, pageID, questionID string) (Verdict, error) {
	var v Verdict
	err := s.db.QueryRowContext(ctx,
		`SELECT corretta, correzione FROM questions WHERE page_id = ? AND id = ?`,
		pageID, questionID).Scan(&v.Corretta, &v.Correzione)
	if errors.Is(err, sql.ErrNoRows) {
		return Verdict{}, ErrNotFound
	}
	return v, err
}

// OptionExists verifica che l'opzione appartenga davvero alla domanda:
// un optionId sconosciuto è un errore applicativo, non una risposta sbagliata.
func (s *Store) OptionExists(ctx context.Context, pageID, questionID, optionID string) (bool, error) {
	var one int
	err := s.db.QueryRowContext(ctx,
		`SELECT 1 FROM options WHERE page_id = ? AND question_id = ? AND id = ?`,
		pageID, questionID, optionID).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// QuestionIDs restituisce tutti gli ID esercizio di una pagina, nell'ordine
// d'autore. Serve al Semaforo per incrementare i contatori per differenza.
func (s *Store) QuestionIDs(ctx context.Context, pageID string) ([]string, error) {
	ok, err := s.PageExists(ctx, pageID)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotFound
	}
	rows, err := s.db.QueryContext(ctx,
		`SELECT id FROM questions WHERE page_id = ? ORDER BY position`, pageID)
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

// ---- Scrittura (solo -task=pull) -------------------------------------

// Page è la forma già validata prodotta dal parsing dei contenuti.
type Page struct {
	ID       string
	Titolo   string
	Domande  []PageQuestion
}

type PageQuestion struct {
	ID         string
	Titolo     string
	Corretta   string
	Correzione string // Markdown
	Opzioni    []Option
}

// Rebuild riscrive integralmente act.db in un'unica transazione: il server,
// che lo legge in WAL da un altro processo, continua a vedere lo stato
// precedente fino al commit.
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
		`DROP TABLE IF EXISTS options`,
		`DROP TABLE IF EXISTS questions`,
		`DROP TABLE IF EXISTS pages`,
		`CREATE TABLE pages (
			id     TEXT PRIMARY KEY,
			titolo TEXT NOT NULL DEFAULT ''
		)`,
		`CREATE TABLE questions (
			page_id    TEXT    NOT NULL,
			id         TEXT    NOT NULL,
			position   INTEGER NOT NULL,
			titolo     TEXT    NOT NULL,
			corretta   TEXT    NOT NULL,
			correzione TEXT    NOT NULL DEFAULT '',
			PRIMARY KEY (page_id, id)
		)`,
		`CREATE TABLE options (
			page_id     TEXT    NOT NULL,
			question_id TEXT    NOT NULL,
			id          TEXT    NOT NULL,
			position    INTEGER NOT NULL,
			testo       TEXT    NOT NULL,
			PRIMARY KEY (page_id, question_id, id)
		)`,
	}
	for _, s := range stmts {
		if _, err := tx.ExecContext(ctx, s); err != nil {
			return fmt.Errorf("schema act.db: %w", err)
		}
	}

	for _, p := range pages {
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO pages (id, titolo) VALUES (?, ?)`, p.ID, p.Titolo); err != nil {
			return fmt.Errorf("pagina %q: %w", p.ID, err)
		}
		for qi, q := range p.Domande {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO questions (page_id, id, position, titolo, corretta, correzione)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				p.ID, q.ID, qi, q.Titolo, q.Corretta, q.Correzione); err != nil {
				return fmt.Errorf("pagina %q, domanda %q: %w", p.ID, q.ID, err)
			}
			for oi, o := range q.Opzioni {
				if _, err := tx.ExecContext(ctx,
					`INSERT INTO options (page_id, question_id, id, position, testo)
					 VALUES (?, ?, ?, ?, ?)`,
					p.ID, q.ID, o.ID, oi, o.Testo); err != nil {
					return fmt.Errorf("pagina %q, domanda %q, opzione %q: %w", p.ID, q.ID, o.ID, err)
				}
			}
		}
	}

	return tx.Commit()
}
