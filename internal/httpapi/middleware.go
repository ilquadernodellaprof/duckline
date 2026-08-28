// SPDX-License-Identifier: AGPL-3.0-or-later

package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"log"
	"net/http"
	"runtime/debug"
	"sync/atomic"
)

// ---- CORS -------------------------------------------------------------

// corsMiddleware calcola Access-Control-Allow-Origin confrontando l'Origin
// della richiesta con le origini ammesse (da ALLOWED_ORIGINS — mai "*",
// mai un dominio cablato nel codice), con Vary: Origin, e risponde 204 ai
// preflight OPTIONS.
func corsMiddleware(origins map[string]bool, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Vary", "Origin")
		if origin := r.Header.Get("Origin"); origins[origin] {
			w.Header().Set("Access-Control-Allow-Origin", origin)
		}
		if r.Method == http.MethodOptions {
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Max-Age", "86400")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// ---- Controllo di ammissione ------------------------------------------

// MaxInFlight è il tetto di richieste servite contemporaneamente,
// protezione contro traffico anomalo in assenza di una CDN davanti.
// Valore lasciato alla discrezione di chi configura (§11 della specifica):
// isolato qui per essere modificato facilmente.
const MaxInFlight = 64

// Il messaggio è scritto per uno studente di scuola media, non per un log.
const busyMessage = "In questo momento ci sono troppe persone collegate. " +
	"Aspetta qualche secondo e riprova: l'attività non scappa!"

func admissionMiddleware(next http.Handler) http.Handler {
	var inFlight atomic.Int64
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if n := inFlight.Add(1); n > MaxInFlight {
			inFlight.Add(-1)
			writeError(w, http.StatusServiceUnavailable, busyMessage)
			return
		}
		defer inFlight.Add(-1)
		next.ServeHTTP(w, r)
	})
}

// ---- Recover ----------------------------------------------------------

// recoverMiddleware avvolge ogni handler: un panic non gestito
// terminerebbe l'intero processo, non solo la richiesta.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				// Nel log: solo il panic e lo stack. Mai IP, header o body.
				log.Printf("panic recuperato su %s %s: %v\n%s",
					r.Method, r.URL.Path, rec, debug.Stack())
				writeError(w, http.StatusInternalServerError,
					"C'è stato un problema inatteso. Riprova fra poco!")
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// ---- Risposte JSON ----------------------------------------------------

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// A header già inviati non resta che registrare l'accaduto.
		log.Printf("scrittura risposta fallita: %v", err)
	}
}

// writeError è il canale d'errore infrastrutturale: HTTP non-2xx.
func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

// writeAppError è il canale d'errore applicativo previsto da act.js:
// HTTP 200 con un campo "error" nel body.
func writeAppError(w http.ResponseWriter, msg string) {
	writeJSON(w, http.StatusOK, map[string]string{"error": msg})
}

// ---- Segreti ----------------------------------------------------------

// secretEquals confronta un valore fornito con un segreto d'ambiente in
// tempo costante. Il confronto avviene fra digest SHA-256, così
// ConstantTimeCompare opera sempre su lunghezze uguali e non trapela
// nemmeno la lunghezza del segreto. Un segreto vuoto (variabile non
// impostata) non è mai valido.
func secretEquals(given, expected string) bool {
	if expected == "" {
		return false
	}
	g := sha256.Sum256([]byte(given))
	e := sha256.Sum256([]byte(expected))
	return subtle.ConstantTimeCompare(g[:], e[:]) == 1
}
