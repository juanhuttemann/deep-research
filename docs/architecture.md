# Architecture

## The pipeline

A run is four phases, in order, with one bounded follow-up round inside
analyze:

```
research → analyze → [follow-up research → analyze] → fact-check → summarize
```

Each assistant phase is a single model call. There are no multi-turn loops
inside an agent method. The follow-up round is the Driver's: it searches the
first three of the analysis's follow-up queries as extra sub-agents, and
analyzes once more only if they found evidence. It never repeats.

`ui.Driver` is the only thing that sequences the pipeline: it plans the
sub-topics, fans them out as parallel sub-agents, and composes the prompt for
each phase. The planner writes, for each sub-topic and in the question's
language, a keyword search query and the terms a relevant page must mention.
A sub-agent searches the planner's query first; if that does not fill the
branch's source budget, it falls back to the research question plus its own
facet. Both searches rank results by the branch's terms, and results that
mention none of them are reported off-topic and never fetched. A plan without
query or terms (the planner's fallback) searches the anchored question and
keeps the engines' order.

A page that several searches return is one source, counted and cited once;
the queries that found it are kept on it. Each phase's prompt caps a source at
1500 characters, and what fills them is the page's passages that match those
queries (`tools.Excerpt`), not its opening, which on a documentation site is
navigation and a cookie dialog.

The analysis is a decision, not only prose: its reading of the question,
atomic claims (each with the option and criterion it is about, its scope and
its sources), conditional recommendations naming the claims they rest on,
and resolved conflicts. The Driver holds it to the run in code
(`internal/ui/decision.go`):

- `checkAnalysis`: a claim keeps only sources that are pages the run
  fetched (one left with none is `unsourced`), and claim IDs are unique.
- `govern`: the fact-check returns one verdict per claim ID (`supported`,
  `partial`, `contradicted`, `disputed`, `insufficient`). A claim with no
  verdict, several, or one for an ID it does not have is `insufficient`; a
  `supported` verdict must quote a contiguous passage that `locate` finds in
  the cited page's stored text. A recommendation is approved only when every
  claim it names is supported, and is otherwise kept, `blocked`, for audit.
  A fact-check that did not run approves nothing.
- The summarizer gets only the checked decision: approved and blocked
  recommendations, claims by status, gaps and sources; not the analyzer's
  answer prose. The report's `## Answer` section is rendered from the
  approved recommendations in the analyzer's own words, and a report that
  cannot be written falls back to that section, not to the prose.

The `agent` package knows how to talk to one model and how to parse what
comes back. It never decides what to ask or in what order.

## Packages

```
cmd/deep-research   entrypoint; version is stamped in at link time
internal/
  agent     one LLM phase per method (ResearchDetail / Analyze / FactCheck /
            Summarize / Plan); real search via internal/tools when SearXNG
            is configured; Diagnose for `doctor`
  cli       cobra commands (run / list / init / doctor)
  config    config resolution (env > config dir > embedded defaults) + .env
  replay    --trace recorder and --replay player: wraps the assistant to
            record, or serve back, the plan and every search result
  store     append-only JSONL run history
  tools     SearXNG search (JSON or HTML, one or many instances, searx.space
            discovery) + Firecrawl scrape HTTP clients
  ui        live terminal frame, event sinks (TUI / JSONL), md+pdf+json export
config/     embedded default config.yaml and agent.yaml
```

The frame is clamped to the terminal and repainted in place; on a pipe it
degrades to one log line per event.

## Layering

```
cli → { agent, config, replay, store, ui }
replay → agent
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
