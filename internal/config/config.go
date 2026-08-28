// SPDX-License-Identifier: AGPL-3.0-or-later

// Package config raccoglie le variabili d'ambiente (lette a runtime, mai
// costanti compilate) e i percorsi derivati dalla cartella base del sito.
package config

import (
	"net"
	"os"
	"path/filepath"
	"strings"
)

// Config è la configurazione del processo. Tutti i valori sensibili
// arrivano dall'ambiente, impostato nel pannello di alwaysdata.
type Config struct {
	// Addr è "IP:PORT" come forniti dalla piattaforma (tipo *User program*).
	Addr string
	// SemaforoToken protegge GET /api/v1/semaforo/report. Se vuoto,
	// l'endpoint risponde sempre 401 (mai un default permissivo).
	SemaforoToken string
	// ClassPassword è la password unica e globale delle Pagine Protette.
	// Se vuota, l'endpoint risponde sempre 401.
	ClassPassword string
	// AllowedOrigins sono le origini browser ammesse dal CORS, da
	// ALLOWED_ORIGINS (lista separata da virgole, es.
	// "https://esempio.it,https://www.esempio.it"). Vuota → nessuna
	// origine ammessa: le fetch dal browser verranno bloccate dal CORS
	// finché la variabile non viene impostata (mai un default permissivo,
	// mai un dominio di qualcun altro cablato nel codice).
	AllowedOrigins map[string]bool
}

// Load legge l'ambiente. I default di IP/PORT servono solo allo sviluppo
// locale (act.js in locale punta a http://localhost:3000); in produzione
// alwaysdata imposta sempre entrambe.
func Load() Config {
	return Config{
		Addr:           net.JoinHostPort(envOr("IP", "127.0.0.1"), envOr("PORT", "3000")),
		SemaforoToken:  os.Getenv("SEMAFORO_REPORT_TOKEN"),
		ClassPassword:  os.Getenv("CLASS_PASSWORD"),
		AllowedOrigins: parseOrigins(os.Getenv("ALLOWED_ORIGINS")),
	}
}

// parseOrigins normalizza la lista: spazi tollerati, eventuale slash
// finale rimosso (l'header Origin non lo porta mai), voci vuote ignorate.
func parseOrigins(raw string) map[string]bool {
	origins := map[string]bool{}
	for _, o := range strings.Split(raw, ",") {
		o = strings.TrimRight(strings.TrimSpace(o), "/")
		if o != "" {
			origins[o] = true
		}
	}
	return origins
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Percorsi derivati dalla cartella base (/home/[account]/coinhier/).

func DataDir(base string) string    { return filepath.Join(base, "data") }
func ContentDir(base string) string { return filepath.Join(base, "content") }

func ActDBPath(base string) string      { return filepath.Join(DataDir(base), "act.db") }
func SemaforoDBPath(base string) string { return filepath.Join(DataDir(base), "semaforo.db") }
func AuthDBPath(base string) string     { return filepath.Join(DataDir(base), "auth.db") }
