# Usage

A research run is `deep-research -p "question"`; `serve`, `init`, `doctor`
and `list` are subcommands of the same binary, built by `make build`. `deep-research --version` reports the version stamped in at
build time (`dev` for a plain `go build`).

## `-p` / `--prompt`

```bash
deep-research -p "What are the latest advances in fusion energy?"
```

On a terminal this draws a live UI: a research brief you confirm, then a
frame showing the pipeline stage, one row per parallel sub-agent with its
progress meter, a rolling activity tail, and running source/token counters.

### Flags

| Flag | Default | Meaning |
| ---- | ------- | ------- |
| `--prompt`, `-p QUESTION` | — | the question to research; without it the binary prints help |
| `--output`, `-o FILE` | — | write the report to a file as well |
| `--mode quick\|standard\|deep` | `standard` | research budget tier |
| `--sources N` | `0` | sources per sub-agent; `0` lets `--mode` decide (quick 3, standard 4, deep 5) |
| `--reports DIR` | `reports` | where the `.md` / `.pdf` / `.json` artifacts are written |
| `--jsonl` | off | machine-readable event stream instead of the live UI |
| `--silent`, `-s` | off | no live UI; print only the report |
| `--detach` | off | release the live display as soon as the run starts (same as pressing `b`) |
| `--no-color` | off | disable ANSI colour (`NO_COLOR=1` does the same) |
| `--trace` | off | also write `<report>.trace.json`: the plan, every search result with its full page text, and each model phase's prompt and output |
| `--replay TRACE` | — | re-run analyze, fact-check and summarize on a trace's recorded plan and search results, without searching; see below |
| `--plan-only` | off | make the plan, print it as JSON (under `--jsonl`, as the `plan` event) and stop before searching; see below |
| `--plan FILE` | — | run a plan `--plan-only` wrote, edited or not, without planning again; `-` reads stdin |
| `--depth N` | — | **deprecated** alias for `--sources`, kept for existing scripts |

`--jsonl` and `--silent` both write to stdout, so they cannot be combined.

### Editing the plan

The interactive brief lets you rename, add and delete sub-topics before a
run starts. `--plan-only` and `--plan` do the same from a file, for a
script, an editor or a terminal without the live UI:

```bash
deep-research -p "question" --plan-only > plan.json   # one model request
$EDITOR plan.json                                     # sub_topics: add, delete, rename
deep-research --plan plan.json
```

A plan carries its question, depth tier (`depth.key`) and any pinned
`pinned_per_topic` budget, so `--plan` cannot be combined with `-p`,
`--mode` or `--sources`. It is checked before it runs: the tier must exist,
at least one sub-topic must have a name, sub-topics are renumbered in
order, and the source budget is worked out again from the tier rather than
read from the file. When you rename a sub-topic, clear its `query` and
`terms`: they were written for the old name, and the run searches them as
written. A plan-only run writes no report and no history record; `-o FILE`
writes its plan to FILE instead of stdout.

### Replaying a run

A live run searches again and plans differently every time, so a prompt or
model change cannot be told apart from a change in what the web returned.
`--trace` records a run; `--replay` runs the model phases again on exactly
that evidence:

```bash
deep-research -p "question" --trace          # reports/<name>.trace.json
deep-research --replay reports/<name>.trace.json
OPENAI_MODEL=other/model deep-research --replay reports/<name>.trace.json
```

A replay takes its question, `--mode` and `--sources` from the trace (the
flags override them) and runs the recorded plan whole: the plan the run
used, with the sub-topics the brief added, not the planner's first answer.
It writes its report under `<reports>/replay/` so the
original is not overwritten, and is not saved to the history. It spends
model requests on analyze, fact-check and summarize only. Its follow-up
round searches the recorded run's follow-up queries, not the ones the
replayed analysis words, so both runs end on the same evidence; any other
search that was not recorded fails instead of searching live. Its
sub-agents run one at a time: in parallel they race for pages several
searches return, which changes the pages each counts and whether it runs its
fallback query. Two replays of one trace therefore see the same evidence,
but a replay can differ from the live run it was recorded from by a page or
so; compare replays with replays. `--replay` cannot be combined with `-p`.

An empty question is rejected, and so is a run with no API key; neither
writes anything.

If the analyze or report call fails — a provider error after its retries, or
`run_timeout` expiring mid-report — the run still saves what it gathered:
the history record and the `.md` / `.pdf` / `.json` artifacts, with a report
that says it could not be written and includes the analysis when there is
one. The command then exits non-zero, so a script does not mistake it for a
complete report.

While a model call streams, the live frame shows one status line for it,
replaced in place: how long it has waited for the model to start, then, for
a reasoning model, how long it has been thinking with the latest of its
reasoning wrapped in a few rows under the line, then progress in a unit that
fits the phase — the part of the analysis being written and its claims so
far, claims for the fact-check, words for the report — with the change since
the last line, and "no new output for Ns" once the stream stops growing.
These lines are not added to the activity tail, so they cannot push the
source lines out of it; `--jsonl` still carries every one
(`"transient": true`).
See [Driving it from another program](#driving-it-from-another-program).

### Examples

```bash
# Save the report next to the terminal output
deep-research -p "question" --output report.md

# Widen the budget
deep-research -p "question" --mode deep

# Feed another program or agent
deep-research -p "question" --jsonl
```

## Driving it from another program

`--jsonl` is the integration surface: one JSON event per line on stdout,
nothing else on stdout, warnings on stderr.

```bash
dir=$(mktemp -d)
deep-research -p "question" --jsonl --reports "$dir" > "$dir/events.jsonl"
tail -n 1 "$dir/events.jsonl"   # progress, and at the end the outcome
```

- **Every run ends on one `done` line**, written after the report files and
  the history record: `status` is `complete`, `incomplete` (a late phase
  failed; the partial report was saved), `failed` or `cancelled`, `detail`
  is the error, `artifacts` lists the `.md`, `.pdf` and `.json` it wrote.
  Validation errors (an empty question, a bad `--mode`, no key) end on it
  too. A stream that stops without one means the process was killed.
- During streamed planning, analysis, fact-check and summarization calls,
  a `transient` `info` line arrives at least every five seconds — waiting
  for the model to start, thinking, the count so far, or "no new output for Ns". While a reasoning
  model thinks, `detail` holds the status line, a newline, and the latest
  of its reasoning. Retries and which model is asked are `info` lines too,
  planning included. LLM-search calls are unstreamed: they emit a start line
  and can stay silent until the per-call timeout (doubled on retry). Search
  and scraping also have no periodic heartbeat; silence alone does not
  establish that the process is stuck.
- Cancelling during provider connection setup also ends with `cancelled`.
- The exit status is non-zero for `incomplete` and `failed`.
- Use a fresh `--reports` directory per run: artifact names come from the
  question, so the same question asked twice into one directory overwrites
  the first run's files.

The `plan` event arrives once, before the first search, and carries the
plan the run is about to execute in `plan`: the input `--plan` reads.
The other event types are `phase`, `subagent`, `search`, `read`, `verify`,
`citation`, `token`, `report`, `detach` and `error`; their fields are the
`Event` struct in `internal/ui/events.go`. The `.json` artifact carries the
structured result: analysis, fact-check, sources and the whole timeline.

## `serve`

```bash
deep-research serve                      # http://localhost:7777
deep-research serve --addr 127.0.0.1:0   # any free port
```

A page for launching a run and auditing what it decided. The form takes the
question, `--mode` and `--sources`. Research makes the plan first (the same
as `--plan-only`) and opens it for review, as the CLI's brief does: rename,
add and remove sub-topics, or change the depth. A renamed sub-topic drops
the query the planner wrote for its old name. Start research runs the
edited plan (the same as `--plan`, history record included). While it runs
the page shows the live frame. When it
ends it reads the run's `.json` artifact and shows the answer, the
statements the fact-check blocked with the claim that failed, every claim
with its verdict, and each quote inside the page text the check located it
in. Every source is badged `fetched`, `snippet only` or `never fetched`. A
quote is highlighted where it appears verbatim; one the check located only
after normalising markup and whitespace is marked located but not
highlighted. The start screen lists the 50 most recent runs in the reports
directory, and `/?run=<name>.json` opens any of them.
It needs no terminal, so it is also the live view where the terminal UI is
not available (Windows).

| Flag | Default | Meaning |
|---|---|---|
| `--addr HOST:PORT` | `127.0.0.1:7777` | address to listen on |
| `--reports DIR` | `reports` | where runs write their artifacts; served read-only at `/reports/`, with a directory listing |

- One run at a time: a second launch is refused until the first ends.
- Closing or reloading the tab does not cancel the run; the Cancel button
  does. A dropped connection resumes from the last event it saw
  (`Last-Event-ID`); a reloaded tab replays the run from the events the
  server kept, which are the current run's last 10000. A tab that needs
  events no longer kept gets a `gap` event and a frame marked incomplete,
  not a stream that silently starts mid-run. Event ids keep counting across runs and
  restarts, and each run begins with a `start` line carrying its request. A
  server that has not run anything yet sends `idle` on connect, so a tab
  left open across a restart stops showing the old run as live.
- It is a tool for this machine, with no authentication. It answers only
  requests addressed to `localhost`, `127.0.0.1` or `[::1]`, whatever
  `--addr` binds, so a page that rebinds its own name to your address is
  refused. It refuses cross-site POSTs, and a launch must be
  `application/json`, so another site cannot spend your API credits.

## `list`

```bash
deep-research list
```

Shows each past run's findings, successfully fetched sources, and token
usage, read back from the JSONL history file (`data_file` in
`config/config.yaml`, default `~/.deep-research/research.jsonl`).

History keeps each source's text bounded — the first 4000 characters, marked
when cut — because the file is an append-only index that is read back whole.
The stored text of every fetched source is in that run's `.json` artifact,
under `pages`.

New history records contain the full research result plus a run `id`:
structured `analysis`, `fact_check`, `summary`, source statuses and `tokens`.
Older records still load, with their string analysis and top-level confidence
and gaps converted on read; verification data that old records never carried
stays unknown.

## `init`

```bash
deep-research init
```

Writes `config.yaml` and `agent.yaml` into the config directory and a `.env`
into the working directory, and prints where to get a free API key. Existing
files are never clobbered.

`init --docker` also writes a `docker-compose.yml` and `searxng/settings.yml`
into the working directory: a SearXNG on `127.0.0.1:8888` with the JSON API
enabled and a generated secret. Start it with `docker compose up -d` and set
`SEARXNG_URL=http://localhost:8888`. Firecrawl is not included — it runs from
its own repository ([services.md](services.md)). See [configuration.md](configuration.md) for where
those files are looked up.

## `doctor`

```bash
deep-research doctor
```

One line per dependency a run has, each from one cheap request that spends
no model tokens: which `config.yaml` is read; whether the model endpoint
answers with your key (and, for OpenRouter, the key's usage, tier and
today's free-model requests used and left);
whether the search backend answers a real query, and which instance did;
whether the scraper can fetch a page; and how many model requests a run at
each `--mode` tier spends. It exits non-zero when any check fails.

## Keys

The interactive UI is unix-only. On other platforms (Windows) raw terminal
mode is unavailable, so the brief is skipped, the keys below do nothing, and
the run degrades to headless output: one log line per event, then the report.
Everything else — search, the pipeline, the exports and `list` — is unchanged.

In the brief:

| Key | Action |
| --- | ------ |
| `Enter` | launch the research |
| `e` | add a sub-topic (type it, `Enter` confirms) |
| `r` | rename a sub-topic (pick its number, then edit); it is then searched by its new name, not the planner's query |
| `x` | delete a sub-topic (pick its number; one must stay) |
| `d` | cycle depth: quick → standard → deep |
| `q` / `Esc` / `Ctrl-C` | cancel |

Keys pressed while the plan is still being made are discarded — a plan
cannot be confirmed before it is on screen — and the brief says how many were
ignored.

During a run:

| Key | Action |
| --- | ------ |
| `b` | release the display; the run continues (see below) |
| `Esc` / `Ctrl-C` | cancel the run |

`b` is not a true backgrounding: there is no second process to hand the work
to, so the run keeps the foreground until it has written its report. What it
releases is the live frame and the terminal mode — the terminal echoes what
you type again and the keys above stop being captured, so `Esc` no longer
cancels a released run. The report and the exports are written as usual.

## Output and exports

Reports are rendered as styled Markdown in the terminal — headings, tables,
quotes, links, syntax-highlighted code blocks — using
[Glamour](https://github.com/charmbracelet/glamour), the renderer behind Glow;
no separate Glow installation is needed. Rendering also applies to `--silent`
and wraps to the terminal width. Pipes, redirected output, saved reports and
JSONL stay unstyled. The default style is `dark`; set `GLAMOUR_STYLE=light`
for a light terminal, or point `GLAMOUR_STYLE` at a Glamour JSON stylesheet.

Each run writes `.md`, `.pdf` and `.json` artifacts into `--reports`.
Markdown and PDF exports include one executive summary, and PDFs render
readable text with link URLs preserved. The PDF uses Helvetica with
WinAnsi encoding: Latin-1 and supported typographic punctuation render,
while other scripts (including Cyrillic, Greek and CJK) become `?`. The CLI
warns on stderr when characters are lost; use `.md` or `.json` for the full
Unicode text. Artifact filenames use a readable
question stem plus a 128-bit hash; reports created with the earlier short
hash keep their old filenames.

`--output FILE` writes the same document as the `.md` artifact: the question
as a title, the confidence and model lines, the report body, the searches
the analysis suggests, the fact-check counts with each claim it did not
confirm, and the citation lists split into sources the report cites and
sources the run only retrieved. A source with no URL is listed by title and
marked `no URL`. The `.json` sidecar carries the same `model`, `provider`,
`served_by`, the whole `analysis` (its reading of the question, claims with
sources, recommendations, resolved conflicts, gaps and follow-ups), `topics`
and every `fact_check` verdict, plus `error` for an incomplete run, and each
`citation` event in its timeline names its source. `pages` maps each fetched
source's URL to its stored text, the text the fact-check located quotes in.
`passages` lists every quote the fact-check gave, with `verdict` (its index
in `fact_check.verdicts`), `page` (its key in `pages`, empty when the run
fetched no page the quote names) and `located`, whether the check found it
in that text. `located` proves only that the words are the page's; the
claim's `status` is what the check concluded.

Token counts come from the usage the provider reports.
