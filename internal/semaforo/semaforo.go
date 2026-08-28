// SPDX-License-Identifier: AGPL-3.0-or-later

// Package semaforo gestisce semaforo.db, nelle sue tre parti:
//
//   - contatori grezzi per esercizio (temporanei, distrutti a ogni ciclo
//     settimanale). Sono dati anonimi fin dall'origine: uno scalare per
//     esercizio, senza timestamp né alcun legame — nemmeno transitorio —
//     con la sessione che li ha incrementati;
//   - cache dell'ultimo report generato (tabella semaforo_last_report,
//     sovrascritta ogni settimana);
//   - log storico permanente: data, pagina, colore — mai numeri.
//
// I valori numerici usati per colore e ordinamento non sopravvivono mai
// alla singola esecuzione di GenerateReport.
package semaforo

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"duckline/internal/db"
)

// Forchette percentuali sul tasso d'errore aggregato di una pagina.
//
// L'autrice non fissa la soglia esatta ma una FORCHETTA per ciascun
// confine: a ogni ciclo settimanale duckline estrae, con generatore
// crittografico, un valore compreso tra gli estremi e lo usa per quella
// sola esecuzione. Il valore estratto non viene salvato né loggato da
// nessuna parte: così da un colore non è mai possibile risalire al punto
// esatto di taglio — e quindi nemmeno dedurre conteggi precisi a partire
// da una percentuale.
//
// Gli estremi arrivano dalla variabile d'ambiente SEMAFORO_FORCHETTE
// (formato "0.10-0.20,0.40-0.50": prima la forchetta GIALLO, poi la
// ROSSO); in sua assenza valgono i default qui sotto. Con Min == Max una
// forchetta degenera nella soglia fissa classica.

// Forchette raccoglie gli estremi delle due forchette di soglia.
type Forchette struct {
	FacileMin, FacileMax       float64 // sotto l'estratto → GIALLO (troppo facile)
	DifficileMin, DifficileMax float64 // dall'estratto in su → ROSSO (troppo difficile)
	// Nell'intervallo intermedio la difficoltà è adeguata → VERDE.
}

// ForchetteDefault sono i valori usati quando SEMAFORO_FORCHETTE non è
// impostata.
var ForchetteDefault = Forchette{
	FacileMin: 0.10, FacileMax: 0.20,
	DifficileMin: 0.40, DifficileMax: 0.50,
}

// Validate verifica la coerenza degli estremi: dentro [0,1], ciascuna
// forchetta con Min <= Max, e nessuna sovrapposizione tra le due
// (FacileMax <= DifficileMin), altrimenti i colori sarebbero ambigui.
func (f Forchette) Validate() error {
	for _, v := range []float64{f.FacileMin, f.FacileMax, f.DifficileMin, f.DifficileMax} {
		if v < 0 || v > 1 {
			return fmt.Errorf("estremo %v fuori da [0,1]", v)
		}
	}
	if f.FacileMin > f.FacileMax {
		return fmt.Errorf("forchetta GIALLO invertita: %v > %v", f.FacileMin, f.FacileMax)
	}
	if f.DifficileMin > f.DifficileMax {
		return fmt.Errorf("forchetta ROSSO invertita: %v > %v", f.DifficileMin, f.DifficileMax)
	}
	if f.FacileMax > f.DifficileMin {
		return fmt.Errorf("forchette sovrapposte: FacileMax %v > DifficileMin %v",
			f.FacileMax, f.DifficileMin)
	}
	return nil
}

// ParseForchette interpreta il valore di SEMAFORO_FORCHETTE. Stringa
// vuota → ForchetteDefault. Qualunque errore di formato è fatale per il
// task chiamante (exit 1 → email di alwaysdata): meglio nessun report che
// un report con soglie diverse da quelle volute.
func ParseForchette(s string) (Forchette, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return ForchetteDefault, nil
	}
	parti := strings.Split(s, ",")
	if len(parti) != 2 {
		return Forchette{}, fmt.Errorf(
			"SEMAFORO_FORCHETTE: attese 2 forchette separate da virgola (es. 0.10-0.20,0.40-0.50), trovate %d", len(parti))
	}
	coppia := func(p string) (float64, float64, error) {
		estremi := strings.Split(strings.TrimSpace(p), "-")
		if len(estremi) != 2 {
			return 0, 0, fmt.Errorf("forchetta %q: atteso min-max", p)
		}
		lo, err := strconv.ParseFloat(strings.TrimSpace(estremi[0]), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("forchetta %q: %w", p, err)
		}
		hi, err := strconv.ParseFloat(strings.TrimSpace(estremi[1]), 64)
		if err != nil {
			return 0, 0, fmt.Errorf("forchetta %q: %w", p, err)
		}
		return lo, hi, nil
	}
	var f Forchette
	var err error
	if f.FacileMin, f.FacileMax, err = coppia(parti[0]); err != nil {
		return Forchette{}, fmt.Errorf("SEMAFORO_FORCHETTE: %w", err)
	}
	if f.DifficileMin, f.DifficileMax, err = coppia(parti[1]); err != nil {
		return Forchette{}, fmt.Errorf("SEMAFORO_FORCHETTE: %w", err)
	}
	if err := f.Validate(); err != nil {
		return Forchette{}, fmt.Errorf("SEMAFORO_FORCHETTE: %w", err)
	}
	return f, nil
}

const (
	Verde  = "VERDE"
	Giallo = "GIALLO"
	Rosso  = "ROSSO"
)

// ReportEntry è la voce di report per una pagina: colore + sequenza degli
// ID esercizio ordinati per tasso d'errore decrescente. Solo la sequenza:
// nessun numero o percentuale la accompagna.
type ReportEntry struct {
	Pagina   string   `json:"pagina"`
	Colore   string   `json:"colore"`
	Esercizi []string `json:"esercizi"`
}

// Report è il contenuto della cache semaforo_last_report.
type Report struct {
	Generato string        `json:"generato"` // data locale YYYY-MM-DD
	Pagine   []ReportEntry `json:"pagine"`
}

type Store struct{ db *sql.DB }

// Open apre semaforo.db in lettura/scrittura (unico database su cui il
// server scrive) e ne garantisce lo schema.
func Open(ctx context.Context, path string) (*Store, error) {
	d, err := db.OpenReadWrite(path)
	if err != nil {
		return nil, err
	}
	s := &Store{db: d}
	if err := s.ensureSchema(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error { return s.db.Close() }

func (s *Store) ensureSchema(ctx context.Context) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS contatori (
			page_id     TEXT    NOT NULL,
			question_id TEXT    NOT NULL,
			errori      INTEGER NOT NULL DEFAULT 0,
			corrette    INTEGER NOT NULL DEFAULT 0,
			PRIMARY KEY (page_id, question_id)
		)`,
		`CREATE TABLE IF NOT EXISTS semaforo_last_report (
			page_id  TEXT PRIMARY KEY,
			colore   TEXT NOT NULL,
			esercizi TEXT NOT NULL, -- JSON: array di ID, già ordinati
			generato TEXT NOT NULL  -- YYYY-MM-DD
		)`,
		`CREATE TABLE IF NOT EXISTS log_storico (
			data    TEXT NOT NULL, -- YYYY-MM-DD
			page_id TEXT NOT NULL,
			colore  TEXT NOT NULL
		)`,
	}
	for _, q := range stmts {
		if _, err := s.db.ExecContext(ctx, q); err != nil {
			return fmt.Errorf("schema semaforo.db: %w", err)
		}
	}
	return nil
}

// RecordEsito incrementa i contatori per ogni esercizio della pagina:
// errori per gli ID presenti in `sbagliate`, risposte corrette per gli
// altri. Incremento per-esercizio, indipendente: nessun legame tra gli
// esercizi di uno stesso tentativo viene conservato.
func (s *Store) RecordEsito(ctx context.Context, pageID string, allQuestionIDs []string, sbagliate []string) error {
	wrong := make(map[string]bool, len(sbagliate))
	for _, id := range sbagliate {
		wrong[id] = true
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO contatori (page_id, question_id, errori, corrette)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (page_id, question_id)
		DO UPDATE SET errori   = errori   + excluded.errori,
		              corrette = corrette + excluded.corrette`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, qID := range allQuestionIDs {
		e, c := 0, 1
		if wrong[qID] {
			e, c = 1, 0
		}
		if _, err := stmt.ExecContext(ctx, pageID, qID, e, c); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// drawThreshold estrae un valore uniforme in [min, max] usando
// crypto/rand: nessun seed, nessuna riproducibilità — il valore estratto
// è irrecuperabile per costruzione una volta terminata l'esecuzione.
// Con min == max degenera nella soglia fissa.
func drawThreshold(min, max float64) (float64, error) {
	if max < min {
		return 0, fmt.Errorf("forchetta invalida: min %v > max %v", min, max)
	}
	if max == min {
		return min, nil
	}
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		return 0, fmt.Errorf("estrazione soglia: %w", err)
	}
	// 53 bit di mantissa → frazione uniforme in [0, 1).
	f := float64(binary.BigEndian.Uint64(b[:])>>11) / (1 << 53)
	return min + f*(max-min), nil
}

// GenerateReport esegue in un'unica transazione il ciclo settimanale:
// estrae le soglie della settimana dalle forchette dell'autrice, aggrega i
// contatori grezzi per pagina, mappa il colore sulle soglie estratte,
// ordina gli esercizi per tasso d'errore decrescente (solo gli ID),
// sovrascrive semaforo_last_report, appende al log storico e distrugge la
// tabella dei contatori. Né i numeri né le soglie estratte escono da
// questa funzione.
//
// Se la settimana non ha prodotto alcun contatore, la cache viene comunque
// sovrascritta (vuota) e nulla viene appeso al log: il report riflette
// sempre e solo l'ultima settimana, come da specifica.
func (s *Store) GenerateReport(ctx context.Context, now time.Time, f Forchette) (Report, error) {
	date := now.Format("2006-01-02")

	// Le soglie della settimana: estratte una volta per esecuzione dalle
	// forchette configurate, valide per tutte le pagine di questo ciclo,
	// mai salvate o loggate. Vivono solo in queste due variabili locali.
	if err := f.Validate(); err != nil {
		return Report{}, err
	}
	sogliaFacile, err := drawThreshold(f.FacileMin, f.FacileMax)
	if err != nil {
		return Report{}, err
	}
	sogliaDifficile, err := drawThreshold(f.DifficileMin, f.DifficileMax)
	if err != nil {
		return Report{}, err
	}

	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Report{}, err
	}
	defer tx.Rollback()

	type counter struct {
		question string
		errori   int
		corrette int
	}
	perPage := map[string][]counter{}

	rows, err := tx.QueryContext(ctx,
		`SELECT page_id, question_id, errori, corrette FROM contatori`)
	if err != nil {
		return Report{}, err
	}
	for rows.Next() {
		var page string
		var c counter
		if err := rows.Scan(&page, &c.question, &c.errori, &c.corrette); err != nil {
			rows.Close()
			return Report{}, err
		}
		perPage[page] = append(perPage[page], c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return Report{}, err
	}

	report := Report{Generato: date}
	pages := make([]string, 0, len(perPage))
	for p := range perPage {
		pages = append(pages, p)
	}
	sort.Strings(pages)

	for _, page := range pages {
		counters := perPage[page]

		totErr, totOk := 0, 0
		for _, c := range counters {
			totErr += c.errori
			totOk += c.corrette
		}
		tot := totErr + totOk
		if tot == 0 {
			continue // nessun tentativo reale: niente da riportare
		}

		// Il tasso d'errore vive solo in queste variabili locali,
		// come le soglie estratte a inizio esecuzione.
		pageRate := float64(totErr) / float64(tot)
		colore := Verde
		switch {
		case pageRate < sogliaFacile:
			colore = Giallo
		case pageRate >= sogliaDifficile:
			colore = Rosso
		}

		rate := func(c counter) float64 {
			n := c.errori + c.corrette
			if n == 0 {
				return 0
			}
			return float64(c.errori) / float64(n)
		}
		sort.SliceStable(counters, func(i, j int) bool {
			ri, rj := rate(counters[i]), rate(counters[j])
			if ri != rj {
				return ri > rj // tasso d'errore decrescente
			}
			return counters[i].question < counters[j].question // pareggi: ordine stabile
		})
		ids := make([]string, len(counters))
		for i, c := range counters {
			ids[i] = c.question
		}

		report.Pagine = append(report.Pagine, ReportEntry{
			Pagina:   page,
			Colore:   colore,
			Esercizi: ids,
		})
	}

	// Sovrascrive la cache dell'ultimo report.
	if _, err := tx.ExecContext(ctx, `DELETE FROM semaforo_last_report`); err != nil {
		return Report{}, err
	}
	for _, e := range report.Pagine {
		blob, err := json.Marshal(e.Esercizi)
		if err != nil {
			return Report{}, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO semaforo_last_report (page_id, colore, esercizi, generato)
			 VALUES (?, ?, ?, ?)`,
			e.Pagina, e.Colore, string(blob), date); err != nil {
			return Report{}, err
		}
		// Log storico permanente: data, pagina, colore — mai numeri.
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO log_storico (data, page_id, colore) VALUES (?, ?, ?)`,
			date, e.Pagina, e.Colore); err != nil {
			return Report{}, err
		}
	}

	// Distrugge integralmente la tabella dei contatori grezzi.
	if _, err := tx.ExecContext(ctx, `DROP TABLE contatori`); err != nil {
		return Report{}, err
	}

	if err := tx.Commit(); err != nil {
		return Report{}, err
	}

	// Ricrea la tabella vuota per la settimana successiva (fuori dalla
	// transazione del report: a quel punto i vecchi numeri non esistono più).
	if err := s.ensureSchema(ctx); err != nil {
		return Report{}, err
	}
	return report, nil
}

// LastReport legge la cache semaforo_last_report.
func (s *Store) LastReport(ctx context.Context) (Report, error) {
	rows, err := s.db.QueryContext(ctx,
		`SELECT page_id, colore, esercizi, generato
		 FROM semaforo_last_report ORDER BY page_id`)
	if err != nil {
		return Report{}, err
	}
	defer rows.Close()

	var r Report
	for rows.Next() {
		var e ReportEntry
		var blob string
		if err := rows.Scan(&e.Pagina, &e.Colore, &blob, &r.Generato); err != nil {
			return Report{}, err
		}
		if err := json.Unmarshal([]byte(blob), &e.Esercizi); err != nil {
			return Report{}, fmt.Errorf("cache report corrotta per la pagina %q: %w", e.Pagina, err)
		}
		r.Pagine = append(r.Pagine, e)
	}
	return r, rows.Err()
}
