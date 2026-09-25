# AGENTS.md

Operating notes for AI agents working in this repo. Human-facing docs:
[README](README.md), [CONTRIBUTING](CONTRIBUTING.md),
[RELEASING](RELEASING.md), [docs/](docs/) for usage, configuration and
architecture.

## What this is

Go 1.26 CLI. Cobra + viper, `openai-go/v3` +
`microsoft/agent-framework-go` for LLM calls, Glamour for terminal Markdown,
SearXNG/Firecrawl (optional) for web search and scraping. Four-phase
pipeline: research → analyze → fact-check → summarize.

## The gate

`make verify` — fmt, vet, gocyclo <15, ineffassign, golangci-lint, deadcode,
`go test -race ./...`, and a gitignored-`.go` check. **Run it before
considering any change done.** A lint binary that is not installed is
reported and skipped, not a build failure; `make tools` installs all four.

Other targets: `make test`, `make build`, `make lint`,
`go test -race ./internal/ui/` for one package.
`./deep-research run "question"` to exercise the CLI; add
`DEEP_RESEARCH_OFFLINE=true` for a no-network run.

`make verify` does not cross-compile. CI does
(`.github/workflows/ci.yml`), and so should you after touching a
platform-split file: `GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...`.
`//go:build unix` includes darwin, so a Linux-only constant there breaks
every macOS build while the local build stays green — that has already
happened once.

## Code style

- Standard library `testing` only. No testify, no external test frameworks.
  `t.Fatalf` / `t.Errorf`, `t.Helper()`, and `httptest` servers for HTTP
  clients — copy the shape of the existing test doubles (see
  `newSearXNGServer` in `internal/tools/tools_test.go`).
- `<pkg>_test.go` holds unit tests; `regression_test.go` holds the suite that
  pins behaviour a shipped bug got wrong, each test naming the failure it
  prevents. These were once called `pending_test.go`, which read as
  unfinished work; don't reintroduce that name.
- Keep functions short — gocyclo ceiling is 15, enforced by `make verify`.
- Comments explain *why*, not *what*. Non-obvious decisions (timeout
  doubling, hash-suffixed filenames, deprecations) are documented inline;
  match that.

## Architecture rules

Full picture in [docs/architecture.md](docs/architecture.md). The
invariants:

- `internal/ui`'s `Driver` is the ONLY thing that sequences the pipeline
  (plans sub-topics, fans out parallel sub-agents, composes prompts).
  `internal/agent` talks to one model and parses its answers; it never
  decides what to ask or in what order. Don't move sequencing into agent.
- Each assistant phase is a single model call; no multi-turn loops inside an
  agent method.
- Layering: `cli` → {agent, config, store, ui}; `agent` → `tools`; `tools` is
  standalone HTTP clients. Don't import upward.
- Config precedence is env > `config/config.yaml` > embedded defaults, and
  every `config.yaml` key is overridable as `DEEP_RESEARCH_<KEY>`. New config
  keys must work through all three channels, and be documented in
  [docs/configuration.md](docs/configuration.md).
- The terminal UI is unix-only. Platform-split files use `//go:build unix` /
  `//go:build !unix` pairs (`term_unix.go`/`term_other.go`,
  `input_unix.go`/`input_other.go`), and the termios ioctl request numbers
  that differ between families live in `ioctl_linux.go`/`ioctl_bsd.go`. Every
  variant must compile; elsewhere the CLI degrades to headless output.

## Boundaries

- Never commit `.env` (API keys) or anything matching `.gitignore` (the
  `/deep-research` binary, `reports/`, `TODO.md`, `ISSUES.md`, `PENDING.md`).
  `.env.example` is the committed template. If you add a gitignore pattern
  for a directory of Go code, anchor it with a leading `/`, or `make verify`
  fails on hidden `.go` files.
- `store.ResearchResult.UnmarshalJSON` intentionally accepts legacy JSONL
  history schemas. Changing result fields must keep old records loading —
  test both schemas.
- Deprecated flags (`--depth`) and legacy env names (`OPENROUTER_*`) are kept
  on purpose for existing scripts; don't remove them without reading the
  inline deprecation comment.
- `PENDING.md` and `ISSUES.md` are local working notes, never committed.
- User-facing behaviour changes need the matching doc updated in the same
  commit: flags and keys in `docs/usage.md`, config keys in
  `docs/configuration.md`, package or layering changes in
  `docs/architecture.md`, and an entry under `## [Unreleased]` in
  `CHANGELOG.md`.

## Git

Commit on `main`. Never create a branch for work in this repo unless the user
explicitly asks for one — every commit in `git log` is on the main line and
that is deliberate.

Imperative, sentence-case commit messages describing the change and its
reason (see `git log`). No conventional-commit prefixes. One commit per
review/fix pass, with a body that explains each finding and why it mattered,
is the established shape here.

No Co-Author trailers in commits.
