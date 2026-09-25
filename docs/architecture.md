# Architecture

## The pipeline

A run is four phases, in order:

```
research → analyze → fact-check → summarize
```

Each assistant phase is a single model call. There are no multi-turn loops
inside an agent method.

`ui.Driver` is the only thing that sequences the pipeline: it plans the
sub-topics, fans them out as parallel sub-agents, and composes the prompt for
each phase. A sub-agent anchors its search on the research question plus its
own facet; if that query does not fill the branch's source budget, it issues
one re-formulation built from the distinctive terms of the planner's note for
that branch.

The `agent` package knows how to talk to one model and how to parse what
comes back. It never decides what to ask or in what order.

## Packages

```
cmd/deep-research   entrypoint; version is stamped in at link time
internal/
  agent     one LLM phase per method (ResearchDetail / Analyze / FactCheck /
            Summarize / Plan); real search via internal/tools when
            SearXNG + Firecrawl are configured
  cli       cobra commands (run / list / init)
  config    config resolution (env > config dir > embedded defaults) + .env
  store     append-only JSONL run history
  tools     SearXNG search + Firecrawl scrape HTTP clients
  ui        live terminal frame, event sinks (TUI / JSONL), md+pdf+json export
config/     embedded default config.yaml and agent.yaml
```

The frame is clamped to the terminal and repainted in place; on a pipe it
degrades to one log line per event.

## Layering

```
cli → { agent, config, store, ui }
agent → tools
tools → (standalone HTTP clients)
```

Imports go one way only. In particular `tools` must not import `agent`, and
sequencing must not move into `agent`.

## Platform split

The terminal UI is unix-only. Platform-split files come in
`//go:build unix` / `//go:build !unix` pairs — `term_unix.go` /
`term_other.go`, `input_unix.go` / `input_other.go`. On other platforms the
CLI degrades to headless output.

`unix` is not one platform: the termios ioctl request numbers differ between
Linux and the BSDs, and `x/sys/unix` only defines each family's names on that
family. Those two constants live in `ioctl_linux.go` and `ioctl_bsd.go` so
the shared `input_unix.go` stays family-neutral. Every variant must compile,
which CI checks by cross-compiling all six released targets — a Linux-only
constant under a `unix` tag is invisible to a local `go build`.

## Compatibility constraints

- `store.ResearchResult.UnmarshalJSON` intentionally accepts legacy JSONL
  history schemas. Changing result fields must keep old records loading, and
  both schemas are covered by tests.
- Deprecated flags (`--depth`) and legacy env names (`OPENROUTER_*`) are kept
  on purpose for existing scripts; each carries an inline comment saying why.
- New config keys must work through all three channels: environment,
  `config.yaml`, and the embedded defaults.
