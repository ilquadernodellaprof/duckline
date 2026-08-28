// SPDX-License-Identifier: AGPL-3.0-or-later

// Package db apre i file SQLite con il driver puro Go modernc.org/sqlite
// (niente cgo: la build di produzione è CGO_ENABLED=0 GOOS=linux GOARCH=amd64).
//
// Ogni connessione è aperta con journal WAL e busy_timeout di qualche
// secondo, coerente con il modello: un solo scrittore per database, più
// lettori concorrenti (gli handler HTTP in lettura, oppure il server che
// legge act.db mentre -task=pull lo riscrive in un processo separato).
package db

import (
	"database/sql"
	"fmt"

	_ "modernc.org/sqlite" // driver "sqlite"
)

const busyTimeoutMS = 5000

// OpenReadOnly apre un database in sola lettura. Fallisce (alla prima
// query) se il file non esiste: nessuna creazione implicita di un DB vuoto.
func OpenReadOnly(path string) (*sql.DB, error) {
	return open(path, "ro")
}

// OpenReadWrite apre (creandolo se assente) un database in lettura/scrittura
// e serializza le connessioni: un solo scrittore alla volta, per costruzione.
func OpenReadWrite(path string) (*sql.DB, error) {
	d, err := open(path, "rwc")
	if err != nil {
		return nil, err
	}
	// Una sola connessione: elimina alla radice i conflitti di scrittura
	// concorrente all'interno dello stesso processo. Il traffico atteso
	// (compiti a casa, piano Free) non giustifica di più.
	d.SetMaxOpenConns(1)
	return d, nil
}

func open(path, mode string) (*sql.DB, error) {
	// Il percorso entra nel file: URI così com'è: sul filesystem di
	// destinazione non contiene mai '?' o '#' (e un PathEscape
	// codificherebbe anche gli slash, rompendo l'URI).
	dsn := fmt.Sprintf(
		"file:%s?mode=%s&_pragma=busy_timeout(%d)&_pragma=journal_mode(WAL)",
		path, mode, busyTimeoutMS,
	)
	d, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("apertura %s: %w", path, err)
	}
	return d, nil
}
