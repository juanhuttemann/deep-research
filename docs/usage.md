# Usage

A research run is `drsh -p "question"`, or a bare `drsh` on a terminal,
which asks for the question ([below](#a-bare-drsh)); `setup`, `serve`, `ask`, `list`,
`init`, `doctor` and `update` are subcommands of the same binary, built by `make build`. `drsh --version` reports the version stamped in at
build time (`dev` for a plain `go build`).

First time, or changing the model, search or page extraction: [`drsh setup`](#setup).

## `-p` / `--prompt`

```bash
drsh -p "What are the latest advances in fusion energy?"
```

On a terminal this draws a live UI: a research brief you confirm, then a
frame showing the pipeline stage, one row per parallel sub-agent with its
progress meter, a rolling activity tail, and running source/token counters.
Each row of the tail names the sub-agent it is about as the tree does, in
the colour of its tree row, then what it did: a search's query, a page,
or a note on the results. A phase starting heads the rows under it, and
what the run reports of itself (the search engine, the model a phase
uses) is gray. Each page in the tail is one row naming it, site and path: a spinner while
it is read, then a dot, green when read in full, yellow for snippet only,
red when dropped (with `--no-color` or `NO_COLOR`, `✓`, `!` or `✗`). A
page read in full or as a snippet is a link to it: in a terminal that
supports OSC 8 hyperlinks, ctrl-click (cmd-click on macOS, plain click in
some) opens it. Only `http`/`https` addresses are linked.

The planner fixes misspelled words, capitalization, hyphenation, punctuation
and unclosed brackets in the question ("best selling modules (arduino, etc"
becomes "Best selling modules (Arduino, etc.)", "overalll" becomes
"overall"), and a question typed over several lines is joined into one. The
fixed question titles the report, its files and the history entry; the brief
shows what you typed under it (the browser's plan review strikes through each
word it replaced), so you can cancel before the run starts. Only a
word-for-word fix is taken: a correction that adds, drops, reorders or
replaces a word, or alters a word with a digit in it or one of three letters
or fewer ("not"), is ignored, the progress log says so, and the question runs
as typed. Grammar is left as typed.

### A bare `drsh`

On a terminal, `drsh` with no question opens a start screen, the
terminal's side of what [`serve`](#serve)'s page offers, headed as the
live frame is: the title with the model and search the runs will use, as
the page's top bar names them, and the page's `research ▸` box for the
question, with an example in it that Tab takes. It is drawn in place,
under what the terminal already shows, as the `ask ▸` box and the
`/resume` and `/model` lists are, so the three sit alike. Enter runs it as `-p` would, the brief and the
live frame included, and then takes questions about its report in an
`ask ▸` box drawn like the start screen's (see [`ask`](#ask)). An empty
line or Esc there brings the screen back for the next question; Esc or
Ctrl-C on the screen ends it, and Ctrl-C or Ctrl-D at `ask ▸` ends `drsh`
at once. A run that
fails, or that the brief cancels, returns to the screen, which says why.
Flags given with it (`--mode`, `--sources`, `--domains`, `--reports`, `-o`,
`--trace`) apply to every run it starts. With `--silent`, `--jsonl` or a
pipe on either side it prints the help, as it always did.

Each run is a session. Its questions and answers are kept beside its
`.json`, as `<name>.chat.jsonl`, one JSON line per question. Either box
takes two commands. In either box, a `/` opens a menu of the commands
what is typed can still become, that part of each bold: Tab completes the
highlighted one, the arrows move, and Enter on the start of one (`/r`)
runs it.

- `/resume` lists the runs in the reports directory by their question, the
  last one run or asked about first, and keeps those holding every word
  typed; the arrows move and Enter opens one. A finished run prints its
  report and its conversation so far and takes the next question, which
  the model is asked with the earlier ones ahead of it. An interrupted run,
  marked `stopped`, continues as `--resume` would. Esc goes back to where
  you were. Sessions are the `--reports` directory's, so a `drsh` started
  elsewhere lists that directory's. A question asked again writes over the
  earlier run's files, and its conversation goes with them.
- `/model` lists the model runs use and the ones picked before it, newest
  first; `other model…` opens the [`drsh model`](#setup) picker. The pick is
  saved as `drsh model` saves it (`model.json` keeps the last ten under
  `recent`), and the next run and the open report's questions use it. A
  model set in the environment (`OPENAI_MODEL`, `.env`) still wins, and the
  switch says so.

### Flags

| Flag | Default | Meaning |
| ---- | ------- | ------- |
| `--prompt`, `-p QUESTION` | — | the question to research; without it a terminal gets the start screen, and a pipe the help |
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
| `--domains LIST` | — | research only these sites (comma-separated hosts or URLs); each is checked before planning; see below |
| `--resume CHECKPOINT` | — | continue an interrupted run from its `<reports>/*.partial.json`, without searching again what it searched; see below |
| `--depth N` | — | **deprecated** alias for `--sources`, kept for existing scripts |

`--jsonl` and `--silent` both write to stdout, so they cannot be combined.

### Editing the plan

The interactive brief lets you rename, add and delete sub-topics before a
run starts. `--plan-only` and `--plan` do the same from a file, for a
script, an editor or a terminal without the live UI:

```bash
drsh -p "question" --plan-only > plan.json   # one model request
$EDITOR plan.json                                     # sub_topics: add, delete, rename
drsh --plan plan.json
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

### Researching only your own sites

`--domains` confines every search to a list of sites instead of the open
web:

```bash
drsh -p "how do Go generics constrain type parameters" \
  --domains go.dev,pkg.go.dev
```

Each entry is a host or a URL, from the flag, an edited plan or a
checkpoint alike. It is reduced to its lower-case host, without `www.`,
and covers the host's subdomains, so a path in it is dropped. Only public
names are accepted: an IP address, `localhost` or a single-label intranet
name is refused, as it would be as a scrape target. A list holds at most
20 sites.

Before planning, so before any model request, each site is checked the way
a run will use it. A search scoped to the site must return pages on it,
and one of the first three must be readable through the scraper (or come
with its text, on Exa and Tavily), with at least 200 characters of text. A
search that comes back empty or rate-limited is asked once more, a few
seconds later, before the site is judged: a throttled engine often answers
the second time. A site that fails is named with the reason and
dropped, and the run goes on with the rest. A run where none pass stops
there, naming each site and why. When the search failed on every site
(SearXNG not running, say), no site was really checked, so the run says
instead that no search answered and what to check, as a run without a list
does. The table goes to stderr, and under `--jsonl` each site is a `source`
event.

| Status | Meaning | Researched |
| ------ | ------- | ---------- |
| `ok` | pages found and one read in full | yes |
| `snippet-only` | pages found, but read only from their search snippets: no scraper is configured, or it could not read them | yes |
| `unindexed` | the search engine returned no pages on the site, asked twice; the detail says whether its home page loads | no |
| `unreadable` | pages found, but none of the first three could be read (blocked, an error status, or a JavaScript shell with no text) and none came with a snippet | no |
| `search-failed` | the search itself failed (a refusal, a rate limit, an outage, or SearXNG's own engines not answering) | no |

Exa and Tavily take the list as their own domain filter. SearXNG has no
such parameter, so the list goes into the query as `site:` operators, which
Google, Bing, DuckDuckGo and Brave honour and other engines ignore. Either
way, a result from a site not on the list is dropped before it is ranked,
so the scope holds even when an engine ignores it. A site that SearXNG's
engines do not index is reported `unindexed` before the run starts.

On SearXNG, the list works only as well as the instance's engines that
honour `site:` and still answer this machine. Google, Brave, DuckDuckGo and
Qwant suspend or challenge an address that searches in bursts, and Bing
can answer `site:` with nothing. Yahoo honours `site:` and an OR group of
them, but SearXNG's default settings leave it out of general search. If the
check reports `search-failed` naming suspended engines, enable it in
`searxng/settings.yml`:

```yaml
engines:
  - name: yahoo
    disabled: false
```

Exa and Tavily need none of this.

A plan made with `--domains` carries the list (`domains` in `--plan-only`
output), and so does a checkpoint. `--plan` and `--resume` check the sites
again and take no `--domains` of their own. `--replay` searches nothing and
rejects it. With search off there is nothing to confine, and the run
refuses the flag. The run's `.json` and history record list the sites it
used.

`drsh doctor --domains LIST` runs the same check on its own, so a
list can be proved before a run is started on it.

### Resuming an interrupted run

A run that is killed, cancelled, or stopped by an error (a search server
that was down, a timeout) keeps what it did. From the moment its plan is
confirmed it saves a checkpoint after each step, beside its reports:
`<reports>/<name>.<id>.partial.json`, rewritten whole so a kill mid-save
leaves the previous one. It holds the question, the confirmed plan with its
tier and budget, and every search that finished, with its page text. A run
that completes in full removes it; one that is cancelled, fails or ends
incomplete keeps it, and an incomplete run's error names the `--resume`
command for it. A run cut short by rate limits is best resumed a few minutes
later: search engines suspend a client for that long.

```bash
drsh --resume reports/<name>.<id>.partial.json
```

A resume runs the saved plan, without planning again or showing the brief.
It does not search again what finished: those searches are served from the
checkpoint. It searches live what did not finish, and what failed, often the
failure being recovered from. The model phases (analyze, fact-check,
summarize) always run again, and the run gets a fresh `run_timeout`. It
keeps saving to the same checkpoint, so it can be stopped and resumed again.
Its sub-agents may take saved results in another order than the first run
did, so its evidence is close to, not always identical with, what that run
would have reached. `--resume` cannot be combined with `-p`, `--plan`,
`--replay`, `--mode` or `--sources`: the checkpoint carries all of them.

A run killed before its plan was confirmed has nothing saved beyond the
question; run it again.

### Replaying a run

A live run searches again and plans differently every time, so a prompt or
model change cannot be told apart from a change in what the web returned.
`--trace` records a run; `--replay` runs the model phases again on exactly
that evidence:

```bash
drsh -p "question" --trace          # reports/<name>.trace.json
drsh --replay reports/<name>.trace.json
OPENAI_MODEL=other/model drsh --replay reports/<name>.trace.json
```

A replay takes its question, `--mode` and `--sources` from the trace (the
flags override them) and runs the recorded plan whole: the plan the run
used, with the sub-topics the brief added, not the planner's first answer.
It writes its report under `<reports>/replay/` so the
original is not overwritten, and is not saved to the history. It spends
model requests on analyze, fact-check and summarize only. Its follow-up
round searches the recorded run's follow-up queries without asking the
model for new ones, so both runs end on the same evidence; any other
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
far, statements judged for the fact-check, words for the report — with the
change since the last line, and "no new output for Ns" once the stream stops
growing. The fact-check runs as several calls at once, each with its own
line naming its share ("fact-checking claims c1-c6", "fact-checking
inferences k1-k4"), so the line shows whichever reported last.
These lines are not added to the activity tail, so they cannot push the
source lines out of it; `--jsonl` still carries every one
(`"transient": true`).
See [Driving it from another program](#driving-it-from-another-program).

### Examples

```bash
# Save the report next to the terminal output
drsh -p "question" --output report.md

# Widen the budget
drsh -p "question" --mode deep

# Feed another program or agent
drsh -p "question" --jsonl
```

## Driving it from another program

`--jsonl` is the integration surface: one JSON event per line on stdout,
nothing else on stdout, warnings on stderr.

```bash
dir=$(mktemp -d)
drsh -p "question" --jsonl --reports "$dir" > "$dir/events.jsonl"
tail -n 1 "$dir/events.jsonl"   # progress, and at the end the outcome
```

- **Every run ends on one `done` line**, written after the report files and
  the history record: `status` is `complete`, `incomplete` (a late phase
  failed, or more searches gave nothing than gave something; the report was
  saved), `failed` or `cancelled`, `detail`
  is the error, `artifacts` lists the `.md`, `.pdf` and `.json` it wrote.
  Validation errors (an empty question, a bad `--mode`, no key) end on it
  too. A stream that stops without one means the process was killed.
- During streamed planning, analysis, fact-check and summarization calls,
  a `transient` `info` line arrives at least every five seconds — waiting
  for the model to start, thinking, the count so far, or "no new output for Ns". While a reasoning
  model thinks, `detail` holds the status line, a newline, and the latest
  of its reasoning. The reasoning itself comes as `thinking` events, at most
  one a second: `detail` is the text added since the last one, line breaks
  kept, so the `detail`s joined are the whole reasoning, and `status` is
  `start` on each model call's first. They carry the `phase` like any other
  event, and the fact-check's, whose calls run at once and interleave, carry
  the call they belong to in `part` ("claims c1-c6", "inferences k1-k4"). Retries and which model is asked are `info` lines too,
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
A run with `--domains` first sends one `source` event per site, before
planning, with the check's result in `check` (`domain`, `status`, `pages`,
`sample`, `chars`, `detail`).
The other event types are `phase`, `subagent`, `search`, `read`, `verify`,
`citation`, `token`, `report`, `detach`, `thinking` and `error`; their fields are the
`Event` struct in `internal/ui/events.go`. The `.json` artifact carries the
structured result: analysis, fact-check, sources and the whole timeline.

For a coding agent, [`skills/deep-research`](../skills/deep-research/SKILL.md)
is an [Agent Skill](https://agentskills.io) that follows this contract: it
checks the install with `doctor`, asks before running without a scraper,
runs with `--jsonl` into a fresh
`--reports` directory, waits for `done`, answers from the artifacts, and
uses `ask` and `--resume`. Install it with
`/plugin install deep-research --marketplace juanhuttemann/deep-research` in
Claude Code or `npx skills add juanhuttemann/deep-research` elsewhere. A
test fails if it mentions a flag no command defines.

## `serve`

```bash
drsh serve                      # http://localhost:7777
drsh serve --addr 127.0.0.1:0   # any free port
```

A page for launching a run and auditing what it decided. `serve` opens it
in the default browser once it is listening (`$BROWSER` if set; `wslview` or
Windows' browser under WSL; nothing on a Linux session with no display, such
as SSH, where the URL it prints is the way in); `--no-open` leaves that to
you. It follows the system's light or dark setting until the toggle in its top bar picks one, which the browser keeps. The form takes the
question, `--mode` and `--sources` (as "Pages per topic"), and Search: the
web, or "Only these sites", which opens a box for the list (`--domains`)
and keeps the last one typed. The empty question box shows an example, and
Tab takes it as the question: a hot question from Stack Exchange's space,
history or physics site, which the browser fetches from
`api.stackexchange.com` on each load, or one of ten built-in ones until
that answers or when it cannot. The run then opens with a checklist of the
sites, each marked usable, from snippets only, or dropped with the reason,
and the plan review shows them. Research makes the plan first (the same
as `--plan-only`) and opens it for review, as the CLI's brief does: rename,
add and remove sub-topics, change the search each one runs, or change the
depth. Under each name is the planner's description of the sub-topic, which
is for reading: research never sends it. Below that is the query the
sub-agent searches, editable in place; a sub-topic added here can be given
one too. A renamed sub-topic drops the query the planner wrote for its old
name unless you type one, and a sub-topic with no query searches the question
with its name. A query you change drops the terms the planner wrote to judge
results for its own, so that sub-topic keeps every result its search returns. Start research runs the
edited plan (the same as `--plan`, history record included). While it runs
the page shows the live frame. While a model reasons, the status row says
which phase is thinking and for how long, and a thinking row below the
sub-agents holds the reasoning, a section per model call headed by its
phase; open it to read along. It follows the newest text unless you scroll up. Interrupted runs are listed on the start
screen with Resume (the same as `--resume`) and Discard, which deletes the
checkpoint. A tab left open while the server was killed and started again
offers to resume the run it was showing. When it ends it opens on
the Reading view: the report's text without its inline citations or claim
IDs. It is not the `.md` or `.pdf` download, which adds the title,
confidence, the fact-check section and the source lists, and keeps
the citations. Audit, beside it, reads the run's `.json` artifact and shows
the answer, the statements the fact-check blocked and why, every claim with
its verdict, and each quote inside the saved source text the check located
it in. Every source is marked `fetched`, `snippet only` or
`never fetched`. Cards use readable kinds (Conclusion or Recommendation)
instead of internal IDs. Open “Based on … claims” to read the full supporting
statements and their verdicts, then select “Inspect evidence” to see why each
claim received its result. Claim references in blocking reasons open the same
inspector; revision links return to the earlier statement. The inspector
explains the selected verdict and source availability beside the evidence,
and distinguishes a quote found in a search snippet from one found in fetched
page text. “Back to selection” returns to the card or list row you opened.
A run in which no page was read in full says so above both views: every claim was then
checked against a search snippet. While a run is going, five snippets in a
row with no page read, with a scraper set up, put up a notice that the
scraper may be down, while the run can still be cancelled. A quote is highlighted where it appears verbatim; one
the check located only after normalising markup and whitespace is marked
located but not highlighted. The start screen lists the 50 most recent runs
in the reports directory, and `/?run=<name>.json` opens any of them.
It needs no terminal, so it is also the live view where the terminal UI is
not available (Windows). Copy report, beside the views, copies the run's
`.md` report to the clipboard. Below them, Ask about this report takes
follow-up questions, answered as `ask` answers them; the conversation lives
in the tab and starts again when another run is opened.

| Flag | Default | Meaning |
|---|---|---|
| `--addr HOST:PORT` | `127.0.0.1:7777` | address to listen on |
| `--reports DIR` | `reports` | where runs write their artifacts; served read-only at `/reports/`, with a directory listing |
| `--no-open` | off | do not open the page in the default browser |

- The top bar shows what the next run uses: the model, and the search
  engine with what reads the pages (`searxng + firecrawl`), without their
  hosts. Page text comes from `firecrawl`,
  `exa`, `tavily`, `page text included` when the engine sends it with each
  result, or `snippets only`; a search setting a run would refuse shows
  `not usable:` with the reason. It is the configuration `serve` loaded
  when it started (`/api/info`), which is what every run it launches uses:
  restart `serve` after `drsh model` to change it. Hover a chip for
  the full text.
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
  refused. It refuses cross-site POSTs, and a launch or a question must be
  `application/json`, so another site cannot spend your API credits.
- A question does not wait for the run slot: you can ask about one run
  while another runs.

## `ask`

```bash
drsh ask reports/<run>.json -p "Which source gives the price?"
drsh ask reports/<run>.json        # one question per line; an empty line ends it
```

Answers follow-up questions about a finished run from what the run saved:
its report, its claims with the status the fact-check gave each, and the
stored text of every page it fetched (the `.json` artifact's `pages`). The
model reads the pages through two tools, `search_sources` (which pages
mention something) and `read_source` (one page's passages about it), so a
question can reach a passage the report never quoted. Nothing is searched
again; a question the run's pages do not answer is answered as such, with
the suggestion to research it as a new question.

Each question carries the conversation before it, so "and the second one?"
works. The answers are not fact-checked: the model is told to state as fact
only what the run established and to give the status of a claim that was
not, but nothing holds it to that the way the report's answer is held.
Asking needs a model that supports tool calls; with one that does not, the
first question fails at once and says to set `OPENAI_MODEL` to one that does.
With `provider: claude` it needs `ANTHROPIC_API_KEY`, even when Claude Code is
signed in: `claude -p` cannot call the two tools.

A run watched in the terminal UI takes questions under "Ask about this
report" once its report is printed, in an `ask ▸` box with its keys under
it; an empty line, Esc, Ctrl-C or Ctrl-D ends them, and `/resume` and `/model` work as
in [a bare `drsh`](#a-bare-drsh). Those questions, unlike `ask`'s, are kept
in the run's session.

| Flag | Default | Meaning |
|---|---|---|
| `-p`, `--prompt` | | one question; without it, questions are read one per line from stdin |
| `--no-color` | `false` | print the answer's Markdown as is |

## `list`

```bash
drsh list
```

Shows each past run's findings, successfully fetched sources, and token
usage, read back from the JSONL history file (`data_file` in
`config.yaml`, default `~/.deep-research/research.jsonl`).

History keeps each source's text bounded — the first 4000 characters, marked
when cut — because the file is an append-only index that is read back whole.
The stored text of every fetched source is in that run's `.json` artifact,
under `pages`.

New history records contain the full research result plus a run `id`:
structured `analysis`, `fact_check`, `summary`, source statuses and `tokens`.
Older records still load, with their string analysis and top-level confidence
and gaps converted on read; verification data that old records never carried
stays unknown.

## `setup`

| To set up or change | Run |
| ------------------- | --- |
| everything (the first time) | `drsh setup` |
| only the model | `drsh setup model` |
| only the web search | `drsh setup search` |
| only page extraction | `drsh setup scraper` |

Each step opens on what you have now, so Enter keeps it. Nothing is saved
until you confirm the review screen at the end; Esc quits without saving.

The choices:

- **Model**: any provider's model, searchable; it asks for the key when the
  provider needs one, and checks it with one free request.
- **Web search**: public SearXNG instances (free, the default), a local
  SearXNG in Docker, your own SearXNG, Exa, Tavily, or none.
- **Page extraction**: none (search snippets only), Firecrawl self-hosted,
  Firecrawl hosted, Exa or Tavily.

`setup search` also asks about page extraction when the new search needs it,
e.g. switching to Exa with nothing reading pages.

The review screen also warns when an environment variable (from your shell
or `.env`) will override a choice, and says where to remove it.

### Without a terminal

A check runs before saving; if it fails, nothing is saved (`--no-check`
skips it).

```bash
echo "$KEY" | drsh login --provider openrouter --model openrouter/free --key-stdin
drsh setup search --engine searxng                       # public instances
drsh setup search --engine searxng --url http://localhost:8888
echo "$EXA_KEY" | drsh setup search --engine exa --key-stdin
drsh setup search --engine off
drsh setup scraper --scraper firecrawl --url http://localhost:3002
echo "$FC_KEY" | drsh setup scraper --scraper firecrawl --url https://api.firecrawl.dev --key-stdin
drsh setup scraper --scraper none
```

| Flag | Meaning |
| ---- | ------- |
| `--engine` | `setup search`: `searxng`, `exa`, `tavily` or `off` |
| `--scraper` | `setup scraper`: `none`, `firecrawl`, `exa` or `tavily` |
| `--url` | the SearXNG address(es), or Firecrawl's |
| `--key-stdin` | read the service's key from stdin |
| `--no-check` | save without checking |

### Where it saves

All in the config directory: keys in `auth.json` (readable only by you), the
model in `model.json`, search settings in `config.yaml` (only those keys
change; the rest of the file is left as you wrote it). It never writes
`.env`. Details: [configuration](configuration.md#what-drsh-setup-writes).

## `init`

```bash
drsh init
```

Writes `config.yaml` and a `.env` into the config directory, and points at `drsh setup` and where to get a free API key. `setup` does
what `init` does and more; `init` stays for scripts that write the files. Existing
files are never clobbered. The agent prompts are built into the binary, so
no `agent.yaml` is written; create one only to override a prompt
([configuration](configuration.md#agent-prompts)).

`init --docker` also writes a `docker-compose.yml` and `searxng/settings.yml`
into the config directory: a SearXNG on `127.0.0.1:8888` with the JSON API
enabled and a generated secret. It adds `SEARXNG_URL=http://localhost:8888`
to the config directory's `.env` unless that already sets one, and prints the
one command left, `docker compose -f <config dir>/docker-compose.yml up -d`;
Docker restarts the container from then on. This is the recommended search
setup; `drsh setup search` writes the same files when "local SearXNG in
Docker" is chosen, with the address in `config.yaml` instead of `.env`. Its settings turn on Bing, Google, Yahoo and Qwant beside SearXNG's
default Brave, DuckDuckGo and Google CSE: engines rate-limit by IP address,
so a local SearXNG is still throttled after many searches, and more engines
keep one engine's limit from leaving a search unanswered. An existing
`searxng/settings.yml` is never overwritten; add its `engines:` block by
hand to get them. Firecrawl is not included — it runs from its own repository
([services.md](services.md)).

## `login`, `model`, `logout`

The model step of [`setup`](#setup) on its own, and the way to script it.

```bash
drsh login              # pick a model, paste its provider's key if it needs one
drsh login --new-key    # the same, replacing the stored key
drsh model              # the same picker; for switching models
drsh logout openrouter  # remove a stored key
drsh logout exa         # or a search service's
```

Both open one list of every model of every provider, grouped under a heading
per provider that says where its key comes from: stored, from your
environment (only for the endpoint the environment names), not needed (the
Claude subscription), or "needs a key", asked for when you pick one of its
models. Each model shows its context size and price per million tokens
(not for the subscription, which your plan pays for). Type to search
provider and model together: `deep` lists DeepSeek's own models first, then
the DeepSeek models other providers serve; `deepseek v4.1` narrows to that
model wherever it is offered.
While searching, each matching provider also has an "other model ID…" row,
for a model the catalog does not list yet.

Pick a row with Enter. If its provider has no key, the key is asked for next,
shown as `*`. Then one free request — the same one `doctor` makes — checks the
pair before anything is saved; when it fails you can enter the key again,
pick another model, or save anyway. `login --new-key` asks for a key even
when one is stored. The current model is shown at the top and preselected.

"Custom endpoint" asks for a base URL (the stored one filled in), takes an
optional key, and lists the models its `/models` serves; a stored key counts
only for the URL it was entered with, so a new URL asks for its own.

`logout` with no argument removes the only stored key, or lists them when
there are several. Removing a search service's key (`exa`, `tavily`,
`firecrawl`) leaves a run on its `*_API_KEY` from the environment or
`config.yaml`, if one is set.

Without a terminal, script it; the key comes from stdin, so it stays out of
shell history and the process list, and nothing is saved when the check fails:

```bash
echo "$KEY" | drsh login --provider openrouter --model openrouter/free --key-stdin
echo "" | drsh login --provider custom --base-url http://localhost:8000/v1 --model qwen --key-stdin
```

| Flag | Meaning |
| ---- | ------- |
| `--new-key` | ask for the key even when one is stored |
| `--provider` | provider ID (as in the picker; `custom` for your own endpoint) |
| `--model` | model ID |
| `--base-url` | endpoint for `--provider custom` |
| `--key-stdin` | read the key from stdin; without it the stored or environment key is used (switching models by script) |

Where the files go and how they rank against the environment:
[configuration.md](configuration.md#stored-keys-drsh-login).

## `doctor`

```bash
drsh doctor
drsh doctor --domains go.dev,pkg.go.dev   # also check a site list
```

One line per dependency a run has, each from one cheap request that spends
no model tokens: which `config.yaml` is read; whether the model endpoint
answers with your key (and, for OpenRouter, the key's usage, tier and
today's free-model requests used and left), or, with `provider: claude`,
whether runs go through your Claude Code subscription or the key, checked
by looking the model up;
and where the key came from (`auth.json`, the environment or `config.yaml`);
whether the search backend answers a real query, and which instance did;
whether the scraper can fetch a page; and how many model requests a run at
each `--mode` tier spends. With `--domains`, one more line per site says
whether a run could search and read it (see "Researching only your own
sites"). It exits non-zero when any check fails.

Each line is marked `✓` (fine), `!` (works, at lower quality) or `✗`
(broken), and an `→` line under a `!` or `✗` gives the next step: the
command to run or the setting to change. Two setups warn rather than pass:
a search that answers with no results for a common query, and no scraper,
which leaves every claim checked against a search snippet instead of the
page. When public SearXNG instances refuse the check, the `→` line names
both ways out, both chosen with `drsh setup search`: a local SearXNG in
Docker, or a hosted engine (Exa or Tavily). Public instances warn even when
they answer, since they often refuse a run's later searches; the `→` line
recommends the local SearXNG. A missing key points at `drsh setup`, no
scraper at `drsh setup scraper`. The `config` line names the `config.yaml` read, or where
`drsh init` would write one, and an `! config` line names a
`config/config.yaml` or `.env` in the working directory that holds
deep-research settings and is no longer read. A doctor with no failures ends
on `ready to run`.

## `update`

```bash
drsh update          # install the latest release over this binary
drsh update --check  # only say whether one exists
```

Finds the latest GitHub release and, when it is newer, replaces the running
binary with it — where it actually lives, if you run it through a symlink.
The archive is checked against the release's `deep-research_checksums.txt`
as the install scripts check it, and the new binary must run and report the
expected `--version` before it replaces the old one; any failure leaves the
installed binary untouched. It reads no config and needs no API key.
`--check` changes nothing.

Both print "What's new:", the changelog entries of the releases between
yours and the latest, read from that release's `CHANGELOG.md`. When the
changelog cannot be read the update goes ahead without it.

It needs write access to the binary's directory: a root install in
`/usr/local/bin` needs `sudo drsh update`. A binary built from
source (`dev`, or a `git describe` version from `make build`) is refused:
replacing it with a release could install an older version than it is.
On Windows the running `.exe` cannot be deleted, so the previous version is
left beside it as `drsh.exe.old-*`; the next update removes it.

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
Markdown and PDF exports include one executive summary. The PDF is typeset
from the Markdown: headings, bulleted and numbered lists, tables with their
header repeated on each page, code blocks and page numbers. Links are
clickable, and each link's URL is printed once, at its last link, so a
source the report cites shows its URL in the citation list rather than in
every paragraph that cites it. The PDF uses Helvetica with
WinAnsi encoding, and the standard Symbol font for what that lacks: Latin-1,
supported typographic punctuation, Greek letters (π, α, Δ, without accents)
and mathematical signs (→, ≤, ≥, ≠, ≈, −, ∞, √, ∑) render, and other scripts
(including Cyrillic, accented Greek and CJK) become `?`. The CLI
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
