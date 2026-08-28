# duckline

Backend Go di **Coinhier**, lo spazio dinamico che affianca il sito statico
de "il Quaderno della Prof". Licenza **AGPL-3.0-or-later** (vedi `LICENSE`).

`duckline` è sia il nome del modulo Go sia il nome del binario compilato.
`coinhier` non è mai il nome di un file o di un comando: è solo il nome del
sito/cartella su alwaysdata (`coinhier.alwaysdata.net`).

## Modalità (un solo pattern, sempre a flag)

```
duckline                        # server HTTP (default)
duckline -task=pull             # carica content/ in act.db e auth.db
duckline -task=semaforo-report  # report settimanale del Semaforo
```

Ogni modalità `-task=` esce con codice `1` in caso di fallimento, così
l'email di errore automatica degli Scheduled Tasks di alwaysdata fa da
sistema di notifica. Flag opzionale `-dir` per indicare la cartella base
(default: dedotta dal percorso del binario, vedi Assunzioni).

## Build

```
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/duckline .
```

Driver SQLite **puro Go** (`modernc.org/sqlite`): nessun cgo, la
cross-compilazione da GitHub Actions funziona così com'è. Ogni connessione
è aperta con journal **WAL** e `busy_timeout` di 5 s (un solo scrittore per
database, più lettori concorrenti). Prima build: `go mod tidy` per
risolvere `go.sum` (le versioni in `go.mod` sono indicative).

## Layout su alwaysdata

```
/home/[account]/coinhier/
├── bin/duckline      ← binario in produzione
├── data/             ← act.db, semaforo.db, auth.db
└── content/          ← contenuti sincronizzati, letti da -task=pull
    ├── act/          ← *.yaml | *.yml | *.json (una pagina per file)
    └── auth/         ← *.md (una pagina protetta per file)
```

## Variabili d'ambiente (pannello del sito, mai costanti compilate)

| Variabile | Uso |
|---|---|
| `IP`, `PORT` | bind di rete, fornite dalla piattaforma |
| `SEMAFORO_REPORT_TOKEN` | token di `GET /api/v1/semaforo/report` |
| `CLASS_PASSWORD` | password unica delle Pagine Protette |
| `ALLOWED_ORIGINS` | origini browser ammesse dal CORS, separate da virgole (es. `https://esempio.it,https://www.esempio.it`) |
| `SEMAFORO_FORCHETTE` | forchette del Semaforo, formato `0.10-0.20,0.40-0.50` (prima GIALLO, poi ROSSO); assente → default |

Se `SEMAFORO_REPORT_TOKEN` o `CLASS_PASSWORD` non sono impostate, i
rispettivi endpoint rispondono sempre `401` (mai un default permissivo).
Se `ALLOWED_ORIGINS` non è impostata, **nessuna** origine browser è
ammessa: il server lo segnala nel log all'avvio e le fetch dal sito
vengono bloccate dal CORS — nessun dominio è cablato nel codice.

**Nota alwaysdata**: le variabili dell'Environment del sito valgono solo
per il processo del sito, non per gli Scheduled Tasks. `SEMAFORO_FORCHETTE`
serve al task del report, quindi va messa inline nel comando del task:

```
SEMAFORO_FORCHETTE=0.10-0.20,0.40-0.50 /home/ACCOUNT/coinhier/bin/duckline -task=semaforo-report
```

(oppure si omette e valgono i default compilati).

## Endpoint

| Rotta | Cosa |
|---|---|
| `POST /` | ACT: un solo endpoint, tre operazioni per forma del payload (contratto di `act.js`) |
| `POST /api/v1/protected` | Pagine Protette (`{ id, password }`) |
| `GET /api/v1/semaforo/report?token=…` | ultimo report del Semaforo |

Canale d'errore duale (da `act.js`): errori applicativi → HTTP 200 con
campo `"error"`; errori di infrastruttura/validazione → HTTP non-2xx.

Privacy: il codice non legge **mai** `X-Real-IP`, non scrive IP in log o
database, non usa cookie né sessioni.

## Formato dei contenuti

Pagina ACT (`content/act/storia1.yaml`):

```yaml
id: storia1
titolo: La caduta dell'Impero
domande:
  - id: q1
    titolo: In che anno cadde l'Impero romano d'Occidente?
    corretta: b                 # id dell'opzione giusta
    correzione: |
      La data convenzionale è il **476 d.C.**, con la deposizione di
      Romolo Augustolo.
    opzioni:
      - { id: a, testo: "376 d.C." }
      - { id: b, testo: "476 d.C." }
      - { id: c, testo: "576 d.C." }
```

Le correzioni restano **Markdown** nel database: la conversione in HTML
(goldmark) avviene a runtime, solo quando lo status è `stop`.

Pagina protetta (`content/auth/ripasso-storia.md`): l'id è il nome del file
senza estensione; il titolo è il primo heading `# `; il testo è l'intero
file Markdown, convertito in HTML a runtime.

## Semaforo — le forchette

Chi amministra non fissa soglie esatte ma **forchette**: a ogni ciclo
settimanale duckline estrae (con `crypto/rand`, quindi senza alcun seed
riproducibile) un valore dentro ciascuna forchetta, lo usa per mappare i
colori di quella settimana e lo scarta — mai salvato, mai loggato. Da un
colore non si può quindi risalire al punto esatto di taglio, né dedurre
conteggi precisi a partire da una percentuale. Con `min == max` una
forchetta degenera nella soglia fissa classica.

Configurazione via `SEMAFORO_FORCHETTE` (vedi tabella sopra): tasso
d'errore aggregato della pagina sotto l'estratto della prima forchetta →
GIALLO (troppo facile); dall'estratto della seconda in su → ROSSO
(troppo difficile); in mezzo → VERDE. Le forchette non possono
sovrapporsi né uscire da [0,1]: un valore malformato fa fallire il task
(exit 1 → email di alwaysdata) invece di produrre colori sbagliati in
silenzio. Senza variabile valgono i default
(`semaforo.ForchetteDefault`): `0.10-0.20,0.40-0.50`.

L'estrazione è una per esecuzione (stesse soglie per tutte le pagine della
settimana); un'estrazione indipendente per pagina sarebbe una variante
minima, se si volesse rendere l'inferenza ancora più difficile.

Il tetto di richieste in volo resta una costante isolata
(`internal/httpapi/middleware.go`, `MaxInFlight = 64`).

## Assunzioni dichiarate (da verificare con l'autrice)

1. **Rotta Pagine Protette** = `POST /api/v1/protected` (non specificata
   altrove; `act.js` non la usa). Costante `protectedPath` in
   `internal/httpapi/server.go`.
2. **Layout e formato di `content/`** come descritto sopra (non documentati
   altrove). Il campo `corretta` per marcare l'opzione giusta è
   un'invenzione necessaria: da qualche parte il dato deve vivere.
3. **Risposta delle Pagine Protette**: `{ "id", "titolo", "html" }`, con
   Markdown→HTML a runtime (coerente con le correzioni ACT).
4. **Cartella base**: dedotta dal binario (`bin/duckline` → base è la
   cartella sopra `bin/`), sovrascrivibile con `-dir`.
5. **Conferma del report finale ACT**: `200 {"ok":true}` (act.js richiede
   solo un oggetto JSON senza campo `"error"`).
6. **Markdown con HTML abilitato** (`html.WithUnsafe`): i sorgenti Markdown
   provengono esclusivamente dal repository privato dell'autrice, mai dagli
   studenti.
7. **Settimana senza traffico**: la cache del report viene comunque
   sovrascritta (vuota), il log storico non riceve righe.
