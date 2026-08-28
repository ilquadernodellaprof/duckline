// SPDX-License-Identifier: AGPL-3.0-or-later

package httpapi

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"duckline/internal/actdb"
	"duckline/internal/authdb"
	"duckline/internal/config"
	"duckline/internal/markdown"
	"duckline/internal/semaforo"
)

const maxBodyBytes = 1 << 20 // 1 MiB: molto oltre qualunque payload legittimo

type handlers struct {
	cfg  config.Config
	act  *actdb.Store
	auth *authdb.Store
	sem  *semaforo.Store
}

// ---- ACT: un solo endpoint POST, tre operazioni per forma del payload ---

// actRequest copre le tre forme di richiesta di act.js. L'operazione si
// deduce dai campi presenti nel body, non dalla rotta.
type actRequest struct {
	ID         string `json:"id"`
	QuestionID string `json:"questionId"`
	OptionID   string `json:"optionId"`
	// Esito è un puntatore: la sua sola presenza seleziona l'operazione
	// di report finale (§5.1c), anche con `sbagliate` vuoto.
	Esito *struct {
		Sbagliate []string `json:"sbagliate"`
	} `json:"esito"`
}

func (h *handlers) actDispatch(w http.ResponseWriter, r *http.Request) {
	var req actRequest
	if !decodeBody(w, r, &req) {
		return
	}
	// Un body senza "id" è una richiesta malformata: errore di
	// infrastruttura/validazione → canale non-2xx.
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "Richiesta senza id della pagina.")
		return
	}

	switch {
	case req.Esito != nil:
		h.actReport(w, r, req)
	case req.QuestionID != "" || req.OptionID != "":
		if req.QuestionID == "" || req.OptionID == "" {
			writeError(w, http.StatusBadRequest, "Risposta incompleta: servono sia la domanda sia l'opzione.")
			return
		}
		h.actAnswer(w, r, req)
	default:
		h.actLoad(w, r, req)
	}
}

// actLoad — §5.1a: { id } → { domande: [...] }.
func (h *handlers) actLoad(w http.ResponseWriter, r *http.Request, req actRequest) {
	questions, err := h.act.Questions(r.Context(), req.ID)
	if errors.Is(err, actdb.ErrNotFound) {
		// Pagina inesistente: errore applicativo (200 + "error").
		writeAppError(w, "Questa attività non esiste (ancora).")
		return
	}
	if err != nil {
		internalError(w, "lettura domande", err)
		return
	}

	type optionJSON struct {
		ID    string `json:"id"`
		Testo string `json:"testo"`
	}
	type questionJSON struct {
		ID      string       `json:"id"`
		Titolo  string       `json:"titolo"`
		Opzioni []optionJSON `json:"opzioni"`
	}

	out := make([]questionJSON, 0, len(questions))
	for _, q := range questions {
		qj := questionJSON{ID: q.ID, Titolo: q.Titolo, Opzioni: []optionJSON{}}
		for _, o := range q.Opzioni {
			qj.Opzioni = append(qj.Opzioni, optionJSON{ID: o.ID, Testo: o.Testo})
		}
		out = append(out, qj)
	}
	writeJSON(w, http.StatusOK, map[string]any{"domande": out})
}

// actAnswer — §5.1b: { id, questionId, optionId } →
// { status: "next" } oppure { status: "stop", correzione: "<HTML>" }.
func (h *handlers) actAnswer(w http.ResponseWriter, r *http.Request, req actRequest) {
	verdict, err := h.act.VerdictFor(r.Context(), req.ID, req.QuestionID)
	if errors.Is(err, actdb.ErrNotFound) {
		writeAppError(w, "Questa domanda non esiste in questa attività.")
		return
	}
	if err != nil {
		internalError(w, "lettura verdetto", err)
		return
	}

	// Un optionId che non appartiene alla domanda è un guasto del client,
	// non una risposta sbagliata dello studente.
	known, err := h.act.OptionExists(r.Context(), req.ID, req.QuestionID, req.OptionID)
	if err != nil {
		internalError(w, "verifica opzione", err)
		return
	}
	if !known {
		writeAppError(w, "Questa opzione non appartiene alla domanda.")
		return
	}

	if req.OptionID == verdict.Corretta {
		writeJSON(w, http.StatusOK, map[string]string{"status": "next"})
		return
	}

	// La correzione è Markdown in act.db: la conversione in HTML avviene
	// qui, a runtime, solo quando lo status è "stop".
	html, err := markdown.ToHTML(verdict.Correzione)
	if err != nil {
		internalError(w, "conversione correzione", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"status":     "stop",
		"correzione": html,
	})
}

// actReport — §5.1c: { id, esito: { sbagliate } }. Alimenta solo i
// contatori del Semaforo; nessun contenuto in risposta.
func (h *handlers) actReport(w http.ResponseWriter, r *http.Request, req actRequest) {
	ids, err := h.act.QuestionIDs(r.Context(), req.ID)
	if errors.Is(err, actdb.ErrNotFound) {
		writeAppError(w, "Questa attività non esiste.")
		return
	}
	if err != nil {
		internalError(w, "lettura esercizi per il semaforo", err)
		return
	}

	if err := h.sem.RecordEsito(r.Context(), req.ID, ids, req.Esito.Sbagliate); err != nil {
		internalError(w, "aggiornamento contatori semaforo", err)
		return
	}
	// Solo conferma di ricezione: nessun body significativo.
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

// ---- Pagine Protette ---------------------------------------------------

// protectedRequest — §5.3: { id, password }.
type protectedRequest struct {
	ID       string `json:"id"`
	Password string `json:"password"`
}

// protectedPage confronta la password a tempo costante con CLASS_PASSWORD
// (unica, globale). Nessun cookie, nessun token di sessione, nessun log
// dell'accesso, nessuna associazione con l'IP.
func (h *handlers) protectedPage(w http.ResponseWriter, r *http.Request) {
	var req protectedRequest
	if !decodeBody(w, r, &req) {
		return
	}
	if req.ID == "" {
		writeError(w, http.StatusBadRequest, "Richiesta senza id della pagina.")
		return
	}

	if !secretEquals(req.Password, h.cfg.ClassPassword) {
		writeError(w, http.StatusUnauthorized,
			"La password non è giusta. Controlla e riprova!")
		return
	}

	page, err := h.auth.Page(r.Context(), req.ID)
	if errors.Is(err, authdb.ErrNotFound) {
		writeError(w, http.StatusNotFound, "Questa pagina non esiste.")
		return
	}
	if err != nil {
		internalError(w, "lettura pagina protetta", err)
		return
	}

	// ASSUNZIONE (forma della risposta non specificata): il testo è
	// Markdown in auth.db e viene convertito in HTML a runtime, come le
	// correzioni ACT, per la resa diretta nel DOM.
	html, err := markdown.ToHTML(page.Testo)
	if err != nil {
		internalError(w, "conversione pagina protetta", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{
		"id":     page.ID,
		"titolo": page.Titolo,
		"html":   html,
	})
}

// ---- Semaforo: endpoint di lettura del report ---------------------------

// semaforoReport — GET /api/v1/semaforo/report?token=...
// Confronto del token a tempo costante contro SEMAFORO_REPORT_TOKEN;
// 401 se assente o errato. Il servizio è agnostico su chi consuma il dato.
func (h *handlers) semaforoReport(w http.ResponseWriter, r *http.Request) {
	token := r.URL.Query().Get("token")
	if !secretEquals(token, h.cfg.SemaforoToken) {
		writeError(w, http.StatusUnauthorized, "Token mancante o non valido.")
		return
	}

	report, err := h.sem.LastReport(r.Context())
	if err != nil {
		internalError(w, "lettura report semaforo", err)
		return
	}
	if report.Pagine == nil {
		report.Pagine = []semaforo.ReportEntry{}
	}
	writeJSON(w, http.StatusOK, report)
}

// ---- Helper -------------------------------------------------------------

// decodeBody legge e decodifica il body JSON; in caso di problemi risponde
// da sé (canale infrastrutturale, non-2xx) e restituisce false.
func decodeBody(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	if err := dec.Decode(dst); err != nil {
		writeError(w, http.StatusBadRequest, "Non riesco a leggere la richiesta.")
		return false
	}
	return true
}

// internalError logga il dettaglio tecnico (senza IP, header o body) e
// risponde con un 500 dal messaggio comprensibile.
func internalError(w http.ResponseWriter, what string, err error) {
	log.Printf("errore interno (%s): %v", what, err)
	writeError(w, http.StatusInternalServerError,
		"C'è un problema con il servizio in questo momento. Riprova fra poco!")
}
