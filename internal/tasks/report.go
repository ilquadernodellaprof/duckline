// SPDX-License-Identifier: AGPL-3.0-or-later

package tasks

import (
	"context"
	"log"
	"os"
	"time"

	"duckline/internal/config"
	"duckline/internal/semaforo"
)

// SemaforoReport genera il report settimanale del Semaforo. È pensato per
// essere invocato da uno Scheduled Task di alwaysdata a orario fisso (es.
// domenica 03:10, TZ=Europe/Rome, impostata nel pannello): la data del
// report usa quindi il fuso del processo.
//
// In un'unica esecuzione (e un'unica transazione): aggrega i contatori
// grezzi per pagina, mappa il colore sulle soglie fisse, ordina gli
// esercizi per tasso d'errore decrescente, sovrascrive
// semaforo_last_report, appende al log storico (data, pagina, colore) e
// distrugge la tabella dei contatori. Nessun invio email: in caso di
// errore la funzione ritorna un errore e main esce con codice 1 — l'alert
// email lo fa alwaysdata.
func SemaforoReport(base string, now time.Time) error {
	ctx := context.Background()

	// Le forchette arrivano dall'ambiente (SEMAFORO_FORCHETTE, formato
	// "0.10-0.20,0.40-0.50"); assente → default. NOTA alwaysdata: le
	// variabili impostate sull'Environment del SITO valgono solo per il
	// processo del sito, non per gli Scheduled Tasks — per il task va
	// messa inline nel comando (vedi README).
	forchette, err := semaforo.ParseForchette(os.Getenv("SEMAFORO_FORCHETTE"))
	if err != nil {
		return err
	}

	store, err := semaforo.Open(ctx, config.SemaforoDBPath(base))
	if err != nil {
		return err
	}
	defer store.Close()

	report, err := store.GenerateReport(ctx, now, forchette)
	if err != nil {
		return err
	}

	// Riepilogo su stdout (visibile nel log dello Scheduled Task):
	// solo colori, mai numeri — i numeri a questo punto non esistono più.
	for _, e := range report.Pagine {
		log.Printf("%s — Pagina %s — %s", report.Generato, e.Pagina, e.Colore)
	}
	log.Printf("report semaforo generato: %d pagine", len(report.Pagine))
	return nil
}
