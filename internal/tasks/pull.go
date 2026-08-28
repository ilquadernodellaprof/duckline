// SPDX-License-Identifier: AGPL-3.0-or-later

// Package tasks contiene le modalità -task= del binario. Ogni funzione
// restituisce un errore che main traduce in os.Exit(1), per sfruttare
// l'email di errore automatica degli Scheduled Tasks di alwaysdata.
package tasks

import (
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"

	"duckline/internal/actdb"
	"duckline/internal/authdb"
	"duckline/internal/config"
)

// Pull carica in act.db e auth.db i contenuti già sincronizzati su disco
// dalla pipeline esterna. Legge SOLO dal filesystem locale (content/):
// nessuna gestione di git dentro il binario. Resta invocabile a mano via
// SSH come leva per un sync fuori dal flusso automatico.
//
// ASSUNZIONE sul layout di content/ (non documentato altrove):
//
//	content/
//	├── act/    una pagina di esercizi per file (*.yaml, *.yml, *.json)
//	└── auth/   una pagina protetta per file (*.md; id = nome file
//	            senza estensione; titolo = primo heading di livello 1)
//
// Formato di una pagina ACT (YAML — il JSON, essendone un sottoinsieme,
// è letto dallo stesso parser):
//
//	id: storia1
//	titolo: La caduta dell'Impero
//	domande:
//	  - id: q1
//	    titolo: In che anno cadde l'Impero romano d'Occidente?
//	    corretta: b            # id dell'opzione giusta
//	    correzione: |
//	      Testo **Markdown** della correzione.
//	    opzioni:
//	      - { id: a, testo: "376 d.C." }
//	      - { id: b, testo: "476 d.C." }
func Pull(base string) error {
	contentDir := config.ContentDir(base)
	if _, err := os.Stat(contentDir); err != nil {
		return fmt.Errorf("cartella contenuti %s non accessibile: %w", contentDir, err)
	}
	if err := os.MkdirAll(config.DataDir(base), 0o755); err != nil {
		return err
	}

	ctx := context.Background()

	actPages, err := loadActPages(filepath.Join(contentDir, "act"))
	if err != nil {
		return err
	}
	authPages, err := loadAuthPages(filepath.Join(contentDir, "auth"))
	if err != nil {
		return err
	}

	if err := actdb.Rebuild(ctx, config.ActDBPath(base), actPages); err != nil {
		return fmt.Errorf("scrittura act.db: %w", err)
	}
	if err := authdb.Rebuild(ctx, config.AuthDBPath(base), authPages); err != nil {
		return fmt.Errorf("scrittura auth.db: %w", err)
	}

	log.Printf("pull completato: %d pagine ACT, %d pagine protette",
		len(actPages), len(authPages))
	return nil
}

// ---- Parsing pagine ACT ------------------------------------------------

// flexString accetta scalari YAML/JSON di qualunque tipo (stringhe, numeri)
// e li normalizza a stringa: gli ID sono SEMPRE stringhe nel database e
// negli struct, coerentemente con act.js — anche se l'autrice scrive
// `id: 3` senza virgolette.
type flexString string

func (s *flexString) UnmarshalYAML(node *yaml.Node) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("atteso un valore scalare, trovato %v", node.Tag)
	}
	*s = flexString(strings.TrimSpace(node.Value))
	return nil
}

type contentOption struct {
	ID    flexString `yaml:"id"`
	Testo string     `yaml:"testo"`
}

type contentQuestion struct {
	ID         flexString      `yaml:"id"`
	Titolo     string          `yaml:"titolo"`
	Testo      string          `yaml:"testo"` // alias accettato (come in act.js)
	Corretta   flexString      `yaml:"corretta"`
	Correzione string          `yaml:"correzione"`
	Opzioni    []contentOption `yaml:"opzioni"`
}

type contentPage struct {
	ID      flexString        `yaml:"id"`
	Titolo  string            `yaml:"titolo"`
	Domande []contentQuestion `yaml:"domande"`
}

func loadActPages(dir string) ([]actdb.Page, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		log.Printf("avviso: %s assente, nessuna pagina ACT caricata", dir)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var pages []actdb.Page
	seen := map[string]string{} // pageID → file che lo ha definito

	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".yaml", ".yml", ".json":
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // ordine deterministico, indipendente dal filesystem

	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var cp contentPage
		if err := yaml.Unmarshal(raw, &cp); err != nil {
			return nil, fmt.Errorf("%s: parsing fallito: %w", name, err)
		}
		page, err := validatePage(name, cp)
		if err != nil {
			return nil, err
		}
		if prev, dup := seen[page.ID]; dup {
			return nil, fmt.Errorf("%s: id pagina %q già definito in %s", name, page.ID, prev)
		}
		seen[page.ID] = name
		pages = append(pages, page)
	}
	return pages, nil
}

func validatePage(file string, cp contentPage) (actdb.Page, error) {
	fail := func(format string, args ...any) (actdb.Page, error) {
		return actdb.Page{}, fmt.Errorf("%s: %s", file, fmt.Sprintf(format, args...))
	}

	pageID := string(cp.ID)
	if pageID == "" {
		return fail("manca l'id della pagina")
	}

	page := actdb.Page{ID: pageID, Titolo: cp.Titolo}
	qSeen := map[string]bool{}

	for qi, q := range cp.Domande {
		qID := string(q.ID)
		if qID == "" {
			return fail("domanda #%d senza id", qi+1)
		}
		if qSeen[qID] {
			return fail("id domanda %q duplicato", qID)
		}
		qSeen[qID] = true

		titolo := q.Titolo
		if titolo == "" {
			titolo = q.Testo
		}
		if titolo == "" {
			return fail("domanda %q senza titolo/testo", qID)
		}
		if len(q.Opzioni) == 0 {
			return fail("domanda %q senza opzioni", qID)
		}

		oSeen := map[string]bool{}
		var opts []actdb.Option
		for oi, o := range q.Opzioni {
			oID := string(o.ID)
			if oID == "" {
				return fail("domanda %q, opzione #%d senza id", qID, oi+1)
			}
			if oSeen[oID] {
				return fail("domanda %q, id opzione %q duplicato", qID, oID)
			}
			oSeen[oID] = true
			if o.Testo == "" {
				return fail("domanda %q, opzione %q senza testo", qID, oID)
			}
			opts = append(opts, actdb.Option{ID: oID, Testo: o.Testo})
		}

		corretta := string(q.Corretta)
		if corretta == "" {
			return fail("domanda %q senza campo 'corretta'", qID)
		}
		if !oSeen[corretta] {
			return fail("domanda %q: 'corretta' = %q non corrisponde ad alcuna opzione", qID, corretta)
		}
		if strings.TrimSpace(q.Correzione) == "" {
			// Non fatale: lo status "stop" arriverebbe con correzione vuota.
			log.Printf("avviso: %s, domanda %q senza correzione", file, qID)
		}

		page.Domande = append(page.Domande, actdb.PageQuestion{
			ID:         qID,
			Titolo:     titolo,
			Corretta:   corretta,
			Correzione: q.Correzione,
			Opzioni:    opts,
		})
	}
	return page, nil
}

// ---- Parsing pagine protette -------------------------------------------

func loadAuthPages(dir string) ([]authdb.Page, error) {
	entries, err := os.ReadDir(dir)
	if os.IsNotExist(err) {
		log.Printf("avviso: %s assente, nessuna pagina protetta caricata", dir)
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var pages []authdb.Page
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.EqualFold(filepath.Ext(e.Name()), ".md") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)

	for _, name := range names {
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		id := strings.TrimSuffix(name, filepath.Ext(name))
		text := string(raw)
		pages = append(pages, authdb.Page{
			ID:     id,
			Titolo: firstHeading(text),
			Testo:  text,
		})
	}
	return pages, nil
}

// firstHeading estrae il primo heading Markdown di livello 1, se presente.
func firstHeading(md string) string {
	for _, line := range strings.Split(md, "\n") {
		line = strings.TrimSpace(line)
		if h, ok := strings.CutPrefix(line, "# "); ok {
			return strings.TrimSpace(h)
		}
	}
	return ""
}
