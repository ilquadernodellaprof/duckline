// SPDX-License-Identifier: AGPL-3.0-or-later

// Package markdown converte in HTML, a runtime, i testi scritti in
// Markdown dall'autrice (correzioni ACT e Pagine Protette). La conversione
// avviene solo al momento della risposta, mai in fase di sincronizzazione
// dei contenuti.
package markdown

import (
	"bytes"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/renderer/html"
)

// WithUnsafe è deliberato: l'unica fonte dei sorgenti Markdown è il
// repository privato dell'autrice — contenuto fidato per costruzione, mai
// input degli studenti. Consente all'autrice di usare HTML puntuale
// (es. <sup>, <abbr>) dentro correzioni e pagine.
var md = goldmark.New(
	goldmark.WithExtensions(extension.GFM),
	goldmark.WithRendererOptions(html.WithUnsafe()),
)

// ToHTML converte una sorgente Markdown in HTML.
func ToHTML(src string) (string, error) {
	var buf bytes.Buffer
	if err := md.Convert([]byte(src), &buf); err != nil {
		return "", err
	}
	return buf.String(), nil
}
