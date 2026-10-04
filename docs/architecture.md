# Architecture

## The pipeline

A run is four phases, in order, with one bounded follow-up round inside
analyze:

```
research → analyze → [follow-up research → analyze] → fact-check → [repair → fact-check] → summarize
```

Each assistant phase is a single model call. There are no multi-turn loops
inside an agent method. The follow-up round is the Driver's: it searches the
first three of the analysis's follow-up queries as extra sub-agents, each
named by the analysis (a query given without a name is its own name), and
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
the queries that found it are kept on it. Once a page is cited with its full
text, later searches do not fetch it again and spend their budget on other
pages (`tools.SearchTools.Skip`); a page only judged off-topic, cited from its
snippet, or fetched past a branch's budget is still fetched by a search it
suits. "The same page" is `tools.CanonicalURL`: scheme and host case, a
trailing slash, the fragment and a default port (80, 443) do not make a
second source; any other port does. Each phase's prompt caps a source at
1500 characters, and what fills them is the page's passages that match those
queries (`tools.ExcerptFor`), not its opening, which on a documentation site is
navigation and a cookie dialog. Each passage carries its section context,
the heading above it and the section's opening sentence, inside the same
budget, so its scope survives the cut.

The analysis is a decision, not only prose: its reading of the question,
atomic claims (each with the option and criterion it is about, its scope and
its sources), conclusions (the answer's statements) and, for a choice,
conditional recommendations, each naming the claims it rests on, and
resolved conflicts. Conclusions (k1, k2, ...) and recommendations (r1,
r2, ...) are governed alike; "recommendation" below means either. The Driver holds it to the run in code
(`internal/ui/decision.go`):

- `checkAnalysis`: a claim keeps only sources that are pages the run
  fetched (one left with none is `unsourced`), and claim IDs are unique.
- `govern`: the fact-check returns one verdict per claim ID (`supported`,
  `partial`, `contradicted`, `disputed`, `insufficient`). A claim with no
  verdict, several, or one for an ID it does not have is `insufficient`; a
  `supported` verdict must quote contiguous passages that `locate` finds, every
  one, in the cited pages' stored text, and with search off (no fetched page)
  nothing is supported. A recommendation is approved only when every
  claim it names is supported and the fact-check judges (by its ID, r1,
  r2, ...) that it follows from them; otherwise it is kept, `blocked`, for
  audit, and the answer names it as not established with the claim that
  failed. A fact-check that did not run approves nothing.
- The repair pass: when a recommendation is blocked and some claims passed,
  one analyzer call revises the blocked ones from the supported claims only
  (`repairPrompt`); code drops a revision that cites any other claim
  (`acceptRevisions`) and keeps only the first valid replacement per blocked
  ID; one fact-check call judges whether each revision follows, given only
  the pages its claims cite. An approved revision takes
  its original's place in the answer; the original stays, blocked.
- What the fact-check sees: each page excerpted by the claims under check,
  those citing it first, then the others (`tools.ExcerptFor`), so a
  contradiction on a page a claim does not cite reaches the check; and each
  recommendation followed by the text of the claims it names, judged first
  and against those alone. `make eval-replay` measures both on frozen
  analyses (`internal/ui/testdata/checker`).

What this guarantees, and what it does not: the report's answer section is
written by the program from what passed, so no statement the check blocked
is stated there, and every claim it stands on quotes a passage that is in a
page the run fetched. Whether a passage supports a claim, and whether claims
justify a statement, are the checking model's judgements. The explanation
the summarizer writes below the answer is model prose held only by its
instructions: it is given nothing but checked material, and told never to
assert what was not established.
- The summarizer gets only the checked decision: approved and blocked
  recommendations, claims by status, gaps and sources; not the analyzer's
  answer prose. The report's `## Answer` section is rendered from the
  approved recommendations in the analyzer's own words, or says none
  passed, and a report that cannot be written falls back to that section,
  not to the prose.

The `agent` package knows how to talk to one model and how to parse what
comes back. It never decides what to ask or in what order.

## Asking about a finished run

A finished run's `.json` artifact is everything a follow-up question needs:
the report, the claims with their status, and the stored text of every
fetched page. `ask` and the page's follow-up box answer from it alone,
without searching. This is not a pipeline phase and is held to nothing the
pipeline is held to: `agent.Chat` is a conversation whose model is given the
run's overview (`ui.RunOverview`: question, report, claims by status,
sources) and two tools over the pages (`ui.SourceTools`: `search_sources`
ranks pages as search results are ranked, `read_source` returns a page's
passages by `tools.ExcerptFor`). The agent framework runs the tool calls the
model makes until it answers. The conversation is carried as text in each
question, so a retried call leaves no half turn behind and the page can hand
back the conversation it shows; the server keeps none. Answers are not
fact-checked: the prompt tells the model to state as fact only supported
claims, and nothing checks that it did.

## Packages

```
cmd/deep-research   entrypoint; version is stamped in at link time
internal/
  agent     one LLM phase per method (ResearchDetail / Analyze / FactCheck /
            Summarize / Plan); real search via internal/tools when SearXNG
            is configured; Diagnose for `doctor`; Chat for questions about
            a finished run
  cli       cobra commands (run / ask / serve / list / init / doctor /
            update)
  config    config resolution (env > config dir > embedded defaults) + .env
  replay    --trace recorder and --replay player: wraps the assistant to
            record, or serve back, the plan and every search result; the
            same recorder saves each run's checkpoint as it goes, and
            Resume serves a checkpoint's finished searches to --resume
  store     append-only JSONL run history
  tools     SearXNG search (JSON or HTML, one or many instances, searx.space
            discovery) + Firecrawl scrape HTTP clients
  update    `update`: latest release from GitHub, checksum-verified,
            installed over the running binary
  ui        live terminal frame, event sinks (TUI / JSONL), md+pdf+json export;
            the .json read back, with the tools a question reads it by
  web       `serve`: one embedded page, the run's JSONL over SSE with
            Last-Event-ID replay, launch / cancel, the reports directory
            read-only, a run's report rendered without its citations,
            questions about a run; the run and the answer are functions cli
            injects
config/     embedded default config.yaml and agent.yaml
```

The frame is clamped to the terminal and repainted in place; on a pipe it
degrades to one log line per event.

## Layering

```
cli → { agent, config, replay, store, ui, update, web }
replay → agent
agent → tools
tools → (standalone HTTP clients)
update → (nothing in this module)
web → (nothing in this module)
```

Imports go one way only. In particular `tools` must not import `agent`, and
sequencing must not move into `agent`.

`web` passes the run's `--jsonl` lines through unparsed, and the page, not
the server, reads the result from the `.json` artifact; the server only
renders that artifact's report Markdown for reading. So `web` needs
nothing from `ui`; a launch is the cli's own `--jsonl` command run
in-process. Nothing in the driver exists
for the page's sake: what the page needs and lacks belongs in the event
stream or the artifact.

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
