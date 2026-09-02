# duckline

A small, self-contained Go server for two things a static site often
needs but shouldn't have to build from scratch: **scored, multiple-choice
exercises** delivered over a tiny JSON API, and **password-protected
pages** rendered from Markdown. It ships as a single binary, stores its
data in embedded SQLite (pure Go, no cgo), and is configured entirely
through environment variables.

Licensed under **AGPL-3.0-or-later** (see `LICENSE`).

## Why

At its core, duckline does one generic thing: present a multiple-choice
prompt, grade the answer, optionally explain it, and record the
outcome — through three stateless JSON calls. That pattern covers
scored exercises, but it isn't limited to them: a trivia game, a
branching quiz, a "which of these fits you" questionnaire — anything
built around asking a multiple-choice question and reacting to the
answer can run on the same endpoint.

Alongside that, duckline gates arbitrary content behind a single shared
password — no accounts, no sessions, just a password re-checked on
every request.

You write both as plain files (YAML/JSON for the questions, Markdown
for the gated pages), run `-task=pull`, and the binary serves them over
a small JSON API. No admin UI, no database to edit by hand, no plugin
system — bring your own frontend.

## Privacy

No cookies, no sessions, ever. `/api/v1/protected` re-checks the
password on every request instead of keeping any state.

The code never reads `X-Real-IP` and never writes an IP address to a
log or a database. The outcome endpoint (esito) keeps only per-question 
tallies of right and wrong answers — anonymous scalars, one pair per exercise, 
never who answered them. What arrives over the wire is just 
the list of wrong answers; the right-answer count is derived, not sent.
The Semaforo's randomized threshold ranges exist specifically so a published color
can't be used to back out exact error counts.

## Features

- **Single binary, single process.** Pure-Go SQLite driver, no cgo — it
  cross-compiles trivially and runs anywhere that can execute a Linux
  binary and set a few environment variables. Each connection opens with
  WAL journaling and a 5s busy timeout: one writer, multiple concurrent
  readers, per database file.
- **Content as files.** Exercises are YAML/JSON, protected pages are
  Markdown. Running with `-task=pull` loads them into SQLite; nothing is
  ever hand-edited in the database.
- **A small, stable JSON contract.** One endpoint for exercises, three
  request shapes, enough to build a quiz frontend against without a
  client library.
- **Safe-by-default secrets.** If a required token or password isn't
  set, the corresponding endpoint always answers `401`. A missing
  environment variable can never open a door that should be locked.
- **A privacy-preserving difficulty report ("Semaforo", Italian for
  "traffic light").** A weekly job classifies each exercise as too easy,
  about right, or too hard — using randomized threshold *ranges* instead
  of fixed cutoffs, so the published color can't be used to
  reverse-engineer exact error counts.
- **A fixed concurrency cap.** In-flight requests are capped
  (`MaxInFlight = 64` in `internal/httpapi/middleware.go`) to protect the
  process from being overwhelmed. It's a source-level constant, not an
  environment variable — worth knowing if you're sizing this for heavy
  traffic.

## Quick start

```sh
git clone https://github.com/ilquadernodellaprof/duckline.git
cd duckline
go mod tidy
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags="-s -w" -o bin/duckline .
```

duckline derives its base folder from its own binary's location — the
folder that contains `bin/`. Inside that base folder it expects two more
sibling directories: `content/` (your source files, split into `act/`
and `auth/` — see [Layout](#layout) below) and `data/` (created
automatically, holding the SQLite databases).

```sh
mkdir -p content/act content/auth
# write at least one exercise file and one protected page, then:

CLASS_PASSWORD=changeme SEMAFORO_REPORT_TOKEN=$(openssl rand -hex 32) \
  ./bin/duckline -task=pull

CLASS_PASSWORD=changeme SEMAFORO_REPORT_TOKEN=$(openssl rand -hex 32) \
  ./bin/duckline
# → listening on 127.0.0.1:3000 (defaults; override with IP / PORT)
```

## Modes

A single binary, always driven by flags — no subcommands:

```
duckline                        # HTTP server (default)
duckline -task=pull             # loads content/ into act.db and auth.db
duckline -task=semaforo-report  # runs the weekly Semaforo report
```

Every `-task=` run exits with code `1` on failure — convenient if you
schedule it with anything that emails or alerts on non-zero exit
(cron, systemd timers, your CI/CD provider's scheduled jobs...).

Optional `-dir` flag overrides the base folder (default: derived from
the binary's own path — see [Layout](#layout)).

## Layout

```
your-base-folder/
├── bin/duckline      ← the binary
├── data/             ← act.db, semaforo.db, auth.db (created at runtime)
└── content/          ← your source files, loaded by -task=pull
    ├── act/          ← *.yaml | *.yml | *.json — exercise pages
    └── auth/         ← *.md — one protected page per file
```

Two content types, two folders: put exercise files in content/act/ 
and protected-page files in content/auth/ — -task=pull reads both and populates act.db and auth.db respectively. 
The convention is one page per file, with the filename as the page id; 
for protected pages this is enforced (the id is the filename without .md), while for exercises the id comes from the id: 
field inside the file, so the filename is only a convention. 
By default, duckline infers the base folder from its own binary's location (bin/duckline → base is the folder above bin/). 
Use -dir if you need to run it from somewhere else.

## Configuration

All configuration is via environment variables — nothing is a compiled-in
constant.

| Variable | Purpose |
|---|---|
| `IP`, `PORT` | network bind address and port |
| `SEMAFORO_REPORT_TOKEN` | token required by `GET /api/v1/semaforo/report` |
| `CLASS_PASSWORD` | password for Protected Pages |
| `ALLOWED_ORIGINS` | comma-separated browser origins allowed by CORS (e.g. `https://example.com,https://www.example.com`) |
| `SEMAFORO_FORCHETTE` | Semaforo threshold ranges, format `0.10-0.20,0.40-0.50` (GIALLO range first, then ROSSO); optional |

If `SEMAFORO_REPORT_TOKEN` or `CLASS_PASSWORD` aren't set, the
corresponding endpoints always respond `401`. If `ALLOWED_ORIGINS` isn't
set, **no** browser origin is allowed — duckline logs this at startup
rather than silently allowing everything.

If you run `-task=semaforo-report` through a scheduler that doesn't
inherit your process's environment (a bare cron entry, for instance),
pass `SEMAFORO_FORCHETTE` inline in the command itself rather than
relying on it being set elsewhere:

```
SEMAFORO_FORCHETTE=0.10-0.20,0.40-0.50 /path/to/duckline -task=semaforo-report
```

Omit it entirely and the compiled-in defaults apply.

## API

The JSON wire format uses Italian for field names and for the Semaforo
color values (the project's original language). It's a short list, so
here it is once, up front, rather than repeated per example:

| Term | Meaning |
|---|---|
| `domande` | questions |
| `titolo` | title / prompt |
| `opzioni` | options |
| `testo` | option text |
| `correzione` | correction/explanation (HTML) |
| `esito` | outcome |
| `sbagliate` | wrong answers |
| `generato` | generated (date) |
| `pagine` / `pagina` | pages / page |
| `colore` | color |
| `esercizi` | exercises |
| `Semaforo` | "traffic light" — the difficulty-report feature's name |
| `GIALLO` / `ROSSO` / `VERDE` | Semaforo colors: yellow / red / green |

### Exercises — `POST /`

A single endpoint, three operations, chosen by the shape of the request
body.

**Load questions** — body with only an `id`:

```json
{ "id": "history1" }
```

```json
{
  "domande": [
    {
      "id": "q1",
      "titolo": "In what year did the Western Roman Empire fall?",
      "opzioni": [
        { "id": "a", "testo": "376 AD" },
        { "id": "b", "testo": "476 AD" }
      ]
    }
  ]
}
```

**Submit an answer** — body with `id`, `questionId`, `optionId`:

```json
{ "id": "history1", "questionId": "q1", "optionId": "a" }
```

Response is one of:

```json
{ "status": "next" }
```
```json
{ "status": "stop", "correzione": "<p>The conventional date is <strong>476 AD</strong>…</p>" }
```

`correzione` is already rendered to HTML. The client is never told which
option was correct — only the verdict.

**Report the outcome** — body with `id` and `esito` (even with an empty
list):

```json
{ "id": "history1", "esito": { "sbagliate": ["q1"] } }
```
```json
{ "ok": true }
```

`sbagliate` lists the IDs of questions answered incorrectly. This feeds
the Semaforo counters — nothing else is stored.

**Errors** come through two channels: a non-2xx HTTP status for
infrastructure/validation problems (malformed body, unknown page,
service overloaded), or an HTTP 200 whose body contains an `"error"`
field for application-level problems (page or question doesn't exist,
option doesn't belong to the question). Example:

```json
{ "error": "This exercise doesn't exist (yet)." }
```

### Protected pages — `POST /api/v1/protected`

```json
{ "id": "history-review", "password": "the one the user typed" }
```

Correct password (constant-time comparison against `CLASS_PASSWORD`):

```json
{ "id": "history-review", "titolo": "History review", "html": "<h1>…</h1>" }
```

Wrong password, or `CLASS_PASSWORD` unset → `401`. Unknown page (right
password) → `404`. No cookies, no sessions — every request carries the
password again.

### Semaforo report — `GET /api/v1/semaforo/report?token=…`

```json
{
  "generato": "2026-08-23",
  "pagine": [
    { "pagina": "history1", "colore": "VERDE", "esercizi": ["q1", "q2"] }
  ]
}
```

Within each page, esercizi is ordered by decreasing error rate — the sequence only, never raw counts. 
There is no ordering between pages: the report carries no numbers, so nothing ranks one page against another.

Wrong or missing token → `401`. duckline doesn't ship a bot, a
dashboard, or a client for this — it's just a token-gated JSON `GET`.
Point whatever you want at it: a cron job, a Slack or Telegram bot, a
scheduled task, curl from a terminal, in any language. Being outside
the browser also means it isn't subject to CORS.

### CORS

Browser requests are only accepted from the origins listed in
`ALLOWED_ORIGINS` (exact match, `Vary: Origin`, preflight `OPTIONS` →
`204`). Non-browser tools (curl, scripts, server-to-server calls) are
never subject to CORS.

## Content format

Exercise page (`content/act/history1.yaml`, `.yml` and `.json` also
accepted):

```yaml
id: history1                     # required, unique across all files
titolo: The Fall of the Empire   # optional, not sent to the client
domande:                         # display order = file order
  - id: q1                       # required, unique within the page
    titolo: In what year did the Western Roman Empire fall?
    corretta: b                  # required: id of the correct option
    correzione: |                # Markdown; if omitted, a generic notice is shown instead
      The conventional date is **476 AD**, with the deposition of
      Romulus Augustulus.
    opzioni:                     # at least one; ids unique within the question
      - { id: a, testo: "376 AD" }
      - { id: b, testo: "476 AD" }
      - { id: c, testo: "576 AD" }
```

`-task=pull` validates and refuses to load a page (exit code 1) if: an
id is missing or duplicated, a question has no title or no options, or
`corretta` is missing or doesn't match any option — always naming the
file and question at fault.

Protected page (`content/auth/history-review.md`): the page's id is the
filename without `.md` (so `history-review.md` → id `"history-review"`),
the title is the first `# ` heading if present, and the rest of the file
is the body. Markdown is rendered with GFM and inline HTML enabled —
fine for trusted, author-written sources; don't point this at
user-submitted Markdown.

The same rendering applies to exercise correzione fields — same GFM, same inline HTML, 
same trust assumption: both come from author-written files, never from anything a user submits.

## Semaforo — the threshold ranges, explained

Instead of fixing an exact error-rate cutoff, you configure **ranges**.
On each weekly run, duckline draws (via `crypto/rand`, no reproducible
seed) one value inside each configured range, uses those values to
classify every page's color for that week, and immediately discards
them — never stored, never logged. A color therefore can't be used to
infer the exact cutoff, and knowing a page's color doesn't let you back
out its precise error count. With `min == max` a range degenerates into
a classic fixed threshold, if you'd rather not randomize.

A page's aggregate error rate below that week's GIALLO draw → too easy
(GIALLO); at or above the ROSSO draw → too hard (ROSSO); in between →
fine (VERDE). Ranges can't overlap or fall outside `[0, 1]` — a
malformed `SEMAFORO_FORCHETTE` fails the task loudly (exit 1) rather
than producing silently wrong colors.

The draw happens once per run, so every page shares the same thresholds
for that week. Drawing independently per page is a straightforward
variant if you want the randomization to resist inference even harder.

## Contributing

Bug reports and pull requests are welcome — open an issue if something's
broken or unclear, or send a PR directly if you've already got a fix.

## License

AGPL-3.0-or-later. See `LICENSE`.
