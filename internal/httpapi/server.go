// SPDX-License-Identifier: AGPL-3.0-or-later

// Package httpapi espone i tre servizi di Coinhier:
//
//   - POST /                        ACT (un solo endpoint, tre operazioni
//     distinte per forma del payload — contratto vincolante di act.js)
//   - POST /api/v1/protected        Pagine Protette (ASSUNZIONE: la rotta
//     non è specificata da nessuna parte; act.js non la usa)
//   - GET  /api/v1/semaforo/report  lettura dell'ultimo report del Semaforo
//
// Vincoli di piattaforma rispettati qui: bind su IP/PORT dall'ambiente,
// nessuna lettura di X-Real-IP (mai, da nessuna parte), shutdown pulito su
// segnale, recover() su ogni handler, tetto di richieste in volo.
package httpapi

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"duckline/internal/actdb"
	"duckline/internal/authdb"
	"duckline/internal/config"
	"duckline/internal/semaforo"
)

// protectedPath è la rotta delle Pagine Protette (vedi assunzione sopra).
const protectedPath = "/api/v1/protected"

const shutdownGrace = 10 * time.Second

// Run avvia il server HTTP e blocca fino a shutdown o errore fatale.
func Run(base string) error {
	cfg := config.Load()

	if err := os.MkdirAll(config.DataDir(base), 0o755); err != nil {
		return err
	}

	// act.db e auth.db in sola lettura: l'unico scrittore è -task=pull,
	// che gira come processo separato. Se i file non esistono ancora
	// (primo avvio, sync mai eseguito) l'apertura è lazy e l'errore emerge
	// alla prima query: gli handler lo traducono in un 500 pulito, senza
	// impedire al processo di partire.
	actStore, err := actdb.Open(config.ActDBPath(base))
	if err != nil {
		return err
	}
	defer actStore.Close()

	authStore, err := authdb.Open(config.AuthDBPath(base))
	if err != nil {
		return err
	}
	defer authStore.Close()

	ctx := context.Background()
	semStore, err := semaforo.Open(ctx, config.SemaforoDBPath(base))
	if err != nil {
		return err
	}
	defer semStore.Close()

	h := &handlers{
		cfg:  cfg,
		act:  actStore,
		auth: authStore,
		sem:  semStore,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /{$}", h.actDispatch)
	mux.HandleFunc("POST "+protectedPath, h.protectedPage)
	mux.HandleFunc("GET /api/v1/semaforo/report", h.semaforoReport)

	// Catena middleware, dall'esterno verso l'interno:
	// recover (nessun panic abbatte il processo) →
	// admission (tetto richieste in volo) →
	// cors (origini ammesse + preflight) → mux.
	if len(cfg.AllowedOrigins) == 0 {
		log.Printf("ATTENZIONE: ALLOWED_ORIGINS non impostata — nessuna origine " +
			"browser ammessa, le fetch dal sito verranno bloccate dal CORS")
	}
	handler := recoverMiddleware(admissionMiddleware(corsMiddleware(cfg.AllowedOrigins, mux)))

	srv := &http.Server{
		Addr:              cfg.Addr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      30 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	// SIGTERM è il segnale di stop di default di alproxy; SIGUSR1 è il
	// candidato tipico del campo "Hot restart"; SIGINT serve in sviluppo.
	stopCtx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGTERM, syscall.SIGINT, syscall.SIGUSR1)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Printf("in ascolto su %s", cfg.Addr)
		errCh <- srv.ListenAndServe()
	}()

	select {
	case err := <-errCh:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			return err
		}
		return nil
	case <-stopCtx.Done():
		// Shutdown pulito: smette di accettare richieste, drena quelle in
		// corso, poi i defer sopra chiudono i tre database.
		log.Printf("segnale ricevuto: drenaggio richieste in corso")
		shutCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			return err
		}
		return nil
	}
}
