// duckline — backend dinamico di "Coinhier" (il Quaderno della Prof).
//
// SPDX-License-Identifier: AGPL-3.0-or-later
//
// Dispatch a flag, mai sottocomandi posizionali:
//
//	duckline                        avvia il server HTTP (default)
//	duckline -task=pull             carica i contenuti da content/ in act.db/auth.db
//	duckline -task=semaforo-report  genera il report settimanale del Semaforo
//
// Ogni modalità -task= termina con exit code 1 in caso di fallimento, per
// sfruttare l'email di errore automatica degli Scheduled Tasks di alwaysdata.
package main

import (
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"duckline/internal/httpapi"
	"duckline/internal/tasks"
)

func main() {
	// Il log non deve mai contenere IP client o dati di richiesta:
	// qui si configura solo il prefisso, i singoli punti di log
	// scrivono esclusivamente informazioni applicative.
	log.SetPrefix("duckline: ")

	task := flag.String("task", "", `modalità: "" (server HTTP), "pull", "semaforo-report"`)
	dir := flag.String("dir", "", "cartella base del sito (default: la cartella sopra quella del binario)")
	flag.Parse()

	base, err := resolveBaseDir(*dir)
	if err != nil {
		log.Printf("impossibile determinare la cartella base: %v", err)
		os.Exit(1)
	}

	switch *task {
	case "":
		if err := httpapi.Run(base); err != nil {
			log.Printf("server terminato con errore: %v", err)
			os.Exit(1)
		}
	case "pull":
		if err := tasks.Pull(base); err != nil {
			log.Printf("pull fallito: %v", err)
			os.Exit(1)
		}
	case "semaforo-report":
		if err := tasks.SemaforoReport(base, time.Now()); err != nil {
			log.Printf("semaforo-report fallito: %v", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "duckline: task sconosciuto %q (validi: pull, semaforo-report)\n", *task)
		os.Exit(2)
	}
}

// resolveBaseDir individua la radice del sito
// (/home/[account]/coinhier/ in produzione).
//
// ASSUNZIONE (dichiarata anche nel README): in assenza del flag -dir, la
// base è dedotta dal percorso dell'eseguibile — il layout di produzione è
// coinhier/bin/duckline, quindi se il binario sta in una cartella "bin" la
// base è la cartella che la contiene; altrimenti è la cartella del binario
// stesso (comodo in sviluppo locale).
func resolveBaseDir(flagDir string) (string, error) {
	if flagDir != "" {
		return filepath.Abs(flagDir)
	}
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	d := filepath.Dir(exe)
	if filepath.Base(d) == "bin" {
		d = filepath.Dir(d)
	}
	return d, nil
}
