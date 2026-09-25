# Usage

All commands are subcommands of the `deep-research` binary built by
`make build`. `deep-research --version` reports the version stamped in at
build time (`dev` for a plain `go build`).

## `run`

```bash
deep-research run "What are the latest advances in fusion energy?"
```

On a terminal this draws a live UI: a research brief you confirm, then a
frame showing the pipeline stage, one row per parallel sub-agent with its
progress meter, a rolling activity tail, and running source/token counters.

### Flags

| Flag | Default | Meaning |
| ---- | ------- | ------- |
| `--output`, `-o FILE` | — | write the report to a file as well |
| `--mode quick\|standard\|deep` | `standard` | research budget tier |
| `--sources N` | `0` | sources per sub-agent; `0` lets `--mode` decide (quick 3, standard 4, deep 5) |
| `--reports DIR` | `reports` | where the `.md` / `.pdf` / `.json` artifacts are written |
| `--jsonl` | off | machine-readable event stream instead of the live UI |
| `--silent`, `-s` | off | no live UI; print only the report |
| `--detach` | off | release the live display as soon as the run starts (same as pressing `b`) |
| `--no-color` | off | disable ANSI colour (`NO_COLOR=1` does the same) |
| `--depth N` | — | **deprecated** alias for `--sources`, kept for existing scripts |

`--jsonl` and `--silent` both write to stdout, so they cannot be combined.

### Examples

```bash
# Save the report next to the terminal output
deep-research run "question" --output report.md

# Widen the budget
deep-research run "question" --mode deep

# Feed another program or agent
deep-research run "question" --jsonl

# Try the CLI with no API calls at all
DEEP_RESEARCH_OFFLINE=true deep-research run "question"
```

## `list`

```bash
deep-research list
```

Shows each past run's findings, successfully fetched sources, and token
usage, read back from the JSONL history file (`data_file` in
`config/config.yaml`, default `~/.deep-research/research.jsonl`).

History keeps each source's text bounded — the first 4000 characters, marked
when cut — because the file is an append-only index that is read back whole.
The full text of every source is in that run's `.md` and `.json` artifacts.

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
into the working directory. Existing files are never clobbered. See
[configuration.md](configuration.md) for where those files are looked up.

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
| `r` | rename a sub-topic (pick its number, then edit) |
| `x` | delete a sub-topic (pick its number; one must stay) |
| `d` | cycle depth: quick → standard → deep |
| `q` / `Esc` / `Ctrl-C` | cancel |

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
readable text with link URLs preserved. Artifact filenames use a readable
question stem plus a 128-bit hash; reports created with the earlier short
hash keep their old filenames.

`--output FILE` writes the same document as the `.md` artifact: the question
as a title, the confidence line, the report body, and the citation lists
split into sources the report cites and sources the run only retrieved.

Token counts come from the usage the provider reports.
