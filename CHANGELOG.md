# Changelog

All notable changes to this project are documented here. The format is based
on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

### Changed

- A question is asked with `deep-research -p "question"`; the `run`
  subcommand is gone. The run flags (`--mode`, `--silent`, `--jsonl`, …)
  moved to the root command.

### Removed

- Offline mode (`--offline`, `offline:` / `DEEP_RESEARCH_OFFLINE`). Its stub
  assistant researched nothing, so a run with it only exercised the CLI;
  a stray `offline: true` in a config file now does nothing.

## [0.2.0] - 2026-09-25

Two defaults change what an existing setup does: a run with no API key now
fails instead of falling back to offline (use `--offline`), and a run with no
search configured now uses public SearXNG instances instead of model-supplied
sources (set `SEARXNG_URL=off` for the old behaviour).

### Changed

- A fresh checkout now does real research on its first run. The default model
  is `openrouter/free` (zero cost, one free key), and search defaults to
  public SearXNG instances discovered from searx.space (`searxng_url: auto`).
  `searxng_url: off` keeps the old model-as-search mode. `init` writes the
  free model into `.env` and prints where to get a key.
- A missing API key is an error that says how to get one. It used to fall
  back to offline mode with a warning, producing a RESEARCH COMPLETE card,
  three artifacts and a history record for a run that researched nothing.
  Offline is now asked for explicitly with `--offline`.
- SearXNG alone enables web search; Firecrawl is optional. Without it each
  source is its search snippet, labelled `snippet only` (`noservice`).
- A search that fails is an error, never a switch to the model inventing
  findings. It carries its status (`rate-limited`, `challenge`, `blocked`,
  `unavailable`) on the event, and a run whose every search failed stops
  before spending model calls on no evidence.
- Streamed phases report progress in units a reader can judge — sections,
  claims checked, words — with the delta, a ticking wait for the first token
  and a "no new text" stall notice, instead of a character count. In the
  live frame it is one status row replaced in place; it no longer floods the
  activity tail and evicts the source lines (`--jsonl` keeps every line,
  marked `"transient": true`).

### Added

- SearXNG's HTML result page is read when an instance refuses the JSON API,
  which almost every public instance does. `searxng_url` accepts a
  comma-separated list; a refusing instance is skipped and the one that
  answered is asked first next time. A bot-challenge page is reported as a
  challenge, not as a search that found nothing.
- `deep-research init --docker` writes a `docker-compose.yml` and SearXNG
  settings for a local instance on `127.0.0.1:8888` with the JSON API
  enabled. The mount is labelled `:z`, without which SELinux hosts deny the
  container its own settings file.
- `deep-research doctor`: one cheap check each for the model endpoint and
  key, the search backend and the scraper, plus the model requests a run
  spends per `--mode` tier. For an OpenRouter key it shows today's
  free-model requests used, the limit and what is left.
- Reports and the `.json` sidecar record the model and provider host, the
  models a router actually served (`served_by`, read off each response
  because the agent framework drops it), the analysis's evidence by topic
  with its confidence, and the fact-check verdicts claim by claim. Citation events in the exported timeline name
  their source.
- A free model's daily cap (`free-models-per-day`) fails at once with a
  message naming it, where it used to burn every retry on a limit that
  clears only at 00:00 UTC. A 429's `Retry-After` is honoured.

### Fixed

- A claim the fact-checker marked `"verified": false` was passed to the
  report writer as verified; the verdict flag now decides, not the array the
  claim arrived in.
- One fetched source made a whole run "verified against sources", including
  findings the model invented when search fell back per query. Every prompt
  now marks unfetched findings `[never fetched]`, and the phase line says how
  many there are.
- A scrape that succeeded with an empty page replaced the search snippet with
  nothing and labelled the source a clean 200. It now keeps the snippet as
  `snippet only` (`empty`).
- The planner fallback searched the question glued to itself ("What is X?
  What is X?").
- A question over 100 columns dropped every sub-topic's facet, so every
  branch issued the same query and all but one found only duplicates. The
  question now gives up its tail to keep each facet.
- A failed analyze or summarize call discarded the whole run. The run is now
  saved, exported and printed from what it gathered, marked incomplete, and
  the command exits non-zero.
- After releasing the display (`b`), the next event restarted the animation
  ticker, which then woke eight times a second for the rest of the run.
- Tabs in event text counted as zero columns, so a row holding one escaped
  the frame's clamp and scrolled the live display.
- A source with no URL was counted but left out of the citation list; it is
  now listed by title and marked `no URL`.
- Model-search findings with no text were counted as sources and sent to
  analysis as blank entries; they are dropped.
- Offline mode's "report" was the raw summarizer prompt; it is now a stub
  that says so.
- An Enter pressed while the plan was being made was flushed without a
  trace, so the brief sat waiting and looked hung. Keys typed before the
  brief are still not acted on, but they are counted and the brief says so.
- `run ""` planned nothing and still spent three model calls; an empty
  question is rejected.

## [0.1.0] - 2026-09-20

### Fixed

- The binary builds on macOS again. `internal/ui/input_unix.go` is tagged
  `//go:build unix`, which includes darwin, but reached for `unix.TCGETS` and
  `unix.TCSETSF` — Linux-only ioctl request numbers. Every Linux build stayed
  green while no macOS build compiled at all. The requests now come from
  per-family files (`ioctl_linux.go`, `ioctl_bsd.go`, which uses darwin's
  `TIOCGETA`/`TIOCSETAF`), and CI cross-compiles all six released targets so
  a compile-only break on a platform nobody develops on fails the run.

### Added

- `docs/services.md`: setup instructions for SearXNG and Firecrawl, the two
  services web-search mode needs. Previously the docs said web search
  required them but not how to run them. Covers enabling SearXNG's JSON API,
  which is off by default and which this CLI requires, and a verification
  command for each service.
- CI on GitHub Actions: fmt, vet, `go test -race`, a cross-compile of every
  release target, a smoke test that runs the real binary through an offline
  research run and all three exporters, golangci-lint, gocyclo and deadcode.
- Tagged releases draft automatically via GoReleaser, with archives and a
  checksums file for linux/darwin/windows on amd64 and arm64.
- One-line installers for Linux, macOS, WSL2 and Termux
  (`scripts/install.sh`) and Windows (`scripts/install.ps1`), both verifying
  the release checksum before installing.
- `CONTRIBUTING.md`, `RELEASING.md`, this changelog, an MIT `LICENSE`, and
  `.env.example`.

### Changed

- Documentation split for a public repo: the README is now what it is for —
  what the tool does, install, quick start, the research modes and the
  handful of settings most runs need. The reference material moved to
  `docs/usage.md`, `docs/configuration.md` and `docs/architecture.md`.

[Unreleased]: https://github.com/juanhuttemann/deep-research/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/juanhuttemann/deep-research/releases/tag/v0.1.0
