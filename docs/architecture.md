# Architecture

## The pipeline

A run is four phases, in order, with one bounded follow-up round closing
research:

```
research → [follow-ups → follow-up research] → analyze → fact-check → [repair → fact-check] → summarize
```

Each agent method is a single model call. There are no multi-turn loops
inside an agent method. The fact-check is the one phase the Driver runs as
several calls at once (`ui/factcheck.go`): it is the longest call of a run,
and nearly all of it is the model reasoning through each claim in turn, so
its time grows with the claims it is given. The claims are split into at
most four shares of six or more, each judged in its own call against every
finding (a claim can be contradicted by any applicable page, not only by the
ones it cites), and the recommendations and conclusions are judged against
their premises alone, with no findings, up to four to a call and in at most
three calls. Each claim call sees every page excerpted by all the claims, as
one call would, so a passage another share's claim matched can still
contradict one of its own; and only its judgements of what it was asked are
kept. The calls run at once and their verdicts are merged; one that fails
leaves only its share unchecked and the run's confidence low, and with no
claim call answered the check did not run. The repair pass's recheck is one
such inference call, for the revisions: their claims already have verdicts.

The follow-up round is the Driver's. One call (`agent.FollowUps`, its own
short `follow_up_instructions`) reads the findings and names the searches
that would most improve the answer, and the Driver searches the first three
as extra sub-agents, each named by that call (a query given without a name
is its own name). The analysis then runs once, on everything found. The
searches used to come from a whole first analysis, replaced by a second
once their evidence was in, which put two full analyses on the critical
path of nine runs in ten; the call that only names the searches reasons a
tenth as long. A follow-up call that fails or names nothing leaves the
analysis to what the first round found, and with search refusing queries
it is not made. The round never repeats.

`ui.Driver` is the only thing that sequences the pipeline: it plans the
sub-topics, fans them out as parallel sub-agents, and composes the prompt for
each phase. The planner writes, for each sub-topic and in the question's
language, a keyword search query and the terms a relevant page must mention.
It also returns the question with its typos fixed (`agent.Outline.Question`);
`agent` takes it only when it keeps every word (case, hyphens and
punctuation aside) and spells each one at most a few letters differently, and the
`Plan` then carries the fix as its question and the original as `asked`.
A sub-agent searches the planner's query first; if that does not fill the
branch's source budget, it falls back to the research question plus its own
facet. Both searches rank results by the branch's terms, and results that
mention none of them are reported off-topic and never fetched. A plan without
query or terms (the planner's fallback) searches the anchored question and
keeps the engines' order.

A run given `--domains` checks its sites first, before the plan
(`ui.Options.checkSources`, through the optional `agent.SourceScoper` the
live assistant and the replay recorder and resumer implement). Each site is
searched on its own and its first page read (`tools.SearchTools.CheckDomains`).
The ones that pass are handed to the search (`SearchTools.Domains`): Exa and
Tavily filter by them natively (`tools.DomainSearcher`), SearXNG gets
`site:` operators in the query, and `SearchTools.Search` drops any result
from elsewhere before ranking, because an engine may ignore the operator.
A list where nothing passes ends the run before the planner's model call.

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
  failed. A fact-check that did not run approves nothing. One given claims
  that answers with no verdict and no inference has judged nothing: it is
  asked once more, and empty twice it is reported as not run, not read as
  every claim insufficient.
- The repair pass: when a recommendation is blocked and some claims passed,
  one analyzer call revises the blocked ones from the supported claims only
  (`repairPrompt`); code drops a revision that cites any other claim
  (`acceptRevisions`) and keeps only the first valid replacement per blocked
  ID; one fact-check call judges whether each revision follows from the
  claims it names, given those claims as premises and no pages: their
  verdicts are already in. An approved revision takes
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

## Providers

`agent.connect` is the one place that knows the provider. It builds the
agents the phases run on and the options a call needs on that provider (a
JSON mode, what a streamed call asks for); retries, deadlines, progress and
token usage above it are the same for every provider. `provider: claude`
picks its transport at startup: the Claude Code CLI when it is signed in to a
subscription, else the Messages API through the framework's
`anthropicprovider`. The CLI transport (`claudecode.go`) is a framework
provider that runs `claude -p` once per call with no tools, so a phase is
still one model call. When the subscription reports its usage limit, it
hands that call and every later one to the API agent built beside it.
`Chat` always takes the API, because its tools are Go functions.
`Config.ClaudeAPIOnly`, set when `login` picked Anthropic's API or an
Anthropic-compatible provider rather than the subscription, skips Claude
Code: `claude -p` talks to Anthropic, so it cannot serve a Kimi model.

## Packages

```
cmd/drsh            entrypoint; version is stamped in at link time
internal/
  agent     one LLM phase per method (ResearchDetail / Analyze / FactCheck /
            Summarize / Plan); real search via internal/tools when a
            search engine is configured; Diagnose for `doctor`; Chat for questions about
            a finished run; the providers (OpenAI-compatible, Claude
            through Claude Code or the Messages API)
  auth      auth.json (stored keys, 0600, atomic, each bound to its
            endpoint; "!command" keys; search services' keys as flat
            "service:<name>" entries) and model.json (the selection)
  catalog   the provider/model list for login: Catwalk's catalog, a
            snapshot embedded, refreshed and cached when online
  cli       cobra commands (run / ask / serve / list / setup / init /
            doctor / login / model / logout / update); setup's sequence of
            steps, its review, checks and the order files are saved in
  config    config resolution (env > login's selection > config dir >
            embedded defaults; stored key > env key) + the config dir's
            .env; never the working directory. Also what setup needs from
            it: a draft resolved as a run would, the variables that would
            override it, and config.yaml with only setup's keys changed
  setup     the Bubble Tea screens behind setup, login and model: every
            provider's models in one searchable list, then the masked key
            when needed, then the check; the search and scraper steps; the
            review prompt. Returns the choices, saves nothing
  replay    --trace recorder and --replay player: wraps the assistant to
            record, or serve back, the plan and every search result; the
            same recorder saves each run's checkpoint as it goes, and
            Resume serves a checkpoint's finished searches to --resume
  store     append-only JSONL run history
  tools     search engines behind Searcher (SearXNG: JSON or HTML, one or
            many instances, searx.space discovery; Exa; Tavily) and
            scrapers behind Scraper (Firecrawl, Exa, Tavily) — HTTP clients;
            the --domains list: parsing, scoping and the preflight check
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
cli → { agent, auth, catalog, config, replay, setup, store, ui, update, web }
config → { agent, auth }
setup → { auth, catalog }
replay → agent
agent → tools
tools → (standalone HTTP clients)
update → (nothing in this module)
web → (nothing in this module)
```

`auth` and `catalog` import nothing in this module. Imports go one way only. In particular `tools` must not import `agent`, and
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
