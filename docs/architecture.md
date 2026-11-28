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
it is not made. The round never repeats. It is the plan's to skip
(`Plan.NoFollowUps`, set from `follow_ups`/`--no-follow-ups` and toggled by
the terminal brief's `f` and the serve page's review alike), so a plan
written out, edited, checkpointed or replayed keeps it; skipped, no call is
made.

The report's shape is the plan's too (`Plan.Report`, an `agent.ReportSpec`
so the replay trace can carry it without importing `ui`). The Driver
appends a "Report shape" block, rendered from the format preset and its
overrides (`ui/report.go`), to the summarizer's input; for the standard
report the block is empty and the input is what it was. The shape changes
only the summarizer's prose: the program-written answer, the quote check,
the key figures and the cited sources are the same in every format. The
report carries none of the pipeline's working (the fact-check's verdicts
and reasons, the suggested searches, the sources not cited): the `.json`
keeps it and the serve page's Audit shows it.

An instant run (`Plan.Instant`, from the `instant` depth, `--mode instant`)
ends differently:

```
research → [follow-ups → follow-up research] → answer
```

After research the Driver composes the analysis's input (the question, the
findings, the sub-topics left uncovered) with the report shape and makes one
call, `agent.Assistant.Answer` (`answerer_instructions`), whose prose is the
report. There is no analysis, fact-check, repair or summarize call, so
nothing governs the answer: its confidence is `unchecked`
(`ui.ConfidenceUnchecked`), its report opens with a note saying so, and
`ResearchResult.Instant` and the export's `instant` mark it. The quote check
is the program's and still runs on it. A one-sub-topic instant plan is made
without the planner (`ui.InstantPlan`, the question searched as asked) and
is never put on review: `ui.Run` skips the brief and the page launches it
without its clarify and plan steps. The bit is the plan's, so a plan file, a
checkpoint and a trace carry it, and a resume or replay of an instant run
ends in the answer call. Instant is a depth tier (`ui.ModeInstant`) and not quick made faster:
quick is a checked run, and a run is instant only when it was started at
the instant tier; a saved plan, checkpoint or trace says so in its own bit,
never through its tier's name. The settings the tier implies
(`Option.Implies` on the depth's instant option) are applied by each
surface from that one table, over every other value: `RunSettings.WithImplied`
for the flags and the config, `/mode instant` and the page's depth control for the start
screens. On review (the brief's `d`, the page's review) `ApplyToPlan`
turns the plan instant, every option at its least, keeping the plan it
replaced in `Plan.Before`; another depth puts that plan back at that depth
(`fromInstant`), checked. `Before` travels through the page's JSON round
trip and is never run: an instant plan is launched through
`Plan.Instantly`; `ui.Run`
refuses an instant plan at launch with search off (`Options.SearchOff`),
since the brief can choose it after the CLI's check. With search off
the CLI refuses it, since the model would write the findings it answers
from.

Before the plan, a run someone can answer (the terminal, or a
`--clarify-only` caller such as the serve page) asks the clarifier
(`agent.Assistant.Clarify`, one call) what the question leaves open. Its
questions are put to the reader by `ui.Run` (the picker on the terminal,
radio groups on the page from the `clarify` event) and the answers
(`agent.Clarification`, aliased in `ui`) go on the plan beside the
questions (`Plan.Clarifications`, `Plan.Questions`). The planner gets them
as its own argument, never folded into the question, which the report
still prints under its title (the planner's `Outline.Title`, carried on
`Plan.Title` into `ResearchResult.Title`; `ui.ReportTitle` falls back to the
question for a run with none); the Driver appends them to the question it composes into the
analysis, follow-up, repair and summarizer prompts (`statedQuestion`), and
never into the fact-check's. With none, every prompt is what it was. A
brief that asks the questions again returns to `ui.Run`, which makes the
new plan: the brief's key loop calls no model. A run with no one to ask
makes no call.

`ui.Driver` is the only thing that sequences the pipeline: it plans the
sub-topics, fans them out as parallel sub-agents, and composes the prompt for
each phase. One thing goes on every call below it: `agent` opens each
prompt with today's date (`impl.dated`, in `callWith`), since a model
otherwise takes its training cutoff for the present and doubts anything
newer. Today's date does not tell it what changed since then, so with web
search on, the plan call also carries two searches of the question
(`impl.planLeadsBlock`, `tools.SearchTools.Leads`): as asked, and with
"archived deprecated discontinued" added, up to eight titles and snippets
each and no page fetched, with what GitHub says of each repository among
them (`tools.GitHubClient.Repo`: archived, last push). The planner must
first give each option's status as `options` beside `subtopics`, judged
from those results and the date, then plan only on the current ones; the
decision is the model's, and nothing in code removes a sub-topic. Shown
the results with only an instruction to leave out what they showed
archived, a planner at low effort still planned an archived project in six
plans of eight; made to write the judgement first, in none of eight. The
searches sit inside `Plan`, as `ResearchDetail`'s does, before the one
model call and bounded to 20 seconds; they are cached per question and
source list, so the brief's replans reuse them, and if both fail the plan
is made from memory as before. A trace records the plan, not the results
it was made from, so a replay cannot show why a plan chose what it did.
The planner is given a breadth (`agent.Breadth`): by default a
ceiling, the depth tier's sub-topic count. Under it the planner first
judges the question's scope (`agent.Outline.Scope`): `narrow` (one thing
about one subject, one or two sub-topics), `multi-part` (one per part) or
`comparison` (one per alternative, and one for the criteria only if they
need research). Told only "as few as needed" it filled the ceiling for
nearly every question; a label it must choose first gives it a decision,
and the Driver a rule: a narrow plan runs two sub-topics at most whatever
it wrote (`ui.scopeCap`), and the plan carries the scope for the brief and
the page's review to show. `sub_topics`/`--topics` asks for an exact count
instead, and no scope applies. Code cuts a plan to the ceiling either way, and the run's source
budget follows the sub-topics the plan has. The planner writes, for each sub-topic and in the question's
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

A search that fails, or answers with nothing on the subject, is followed
inside the same `ResearchDetail` by URLs the model suggests
(`agent/pages.go`): one call with `search_instructions`, its URLs fetched by
`tools.SearchTools.Read` within the branch's budget, the source list and
`Skip`, never with the model's text as a snippet, so a URL that does not
load is dropped. With no scraper (`scraper: none`, or Exa and Tavily, which
bring their own text) they are fetched by the built-in reader
(`SearchTools.Named`), since a snippet-only search keeps nothing of a URL
with no snippet. The method still makes one model call. The pages come back
even when none loaded, since each was announced as its fetch began, with
the search's error on `ResearchDetail.SearchErr`; the Driver counts that
search as failed when nothing usable came back (`noteSearch`). With search
off the same call suggests every query's URLs and a reader
(`tools.NewReader`, the scraper with no engine) fetches them; there, a page
that cannot be fetched for a reason other than a `404` or `410` keeps the
model's description, `unverified`, as every such finding was before pages
were fetched. The built-in reader writes a page as Markdown through
html-to-markdown, after pruning what is not the page's text
(`tools.extractHTML`).

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
budget, so its scope survives the cut. Oversized multi-line blocks also
compete by their best matching line, using the same page-level term weights.
A Markdown table is also scored as the excerpt would show it, its header
and the rows that mention a query word, the length penalty charged on those
lines alone, so a table naming each subject asked for, a row each, is not
outranked by a shorter sentence naming some of them; a table whose rows
name nothing asked for scores nothing by this. A row answers by a query
word that tells rows apart (in no more than half of them) and weighs at
least half the weightiest such word, so a word every row shares does not
make every row an answer, and a subject named in one word answers like one
named in two. A table that fits the budget
is also shown that way, its header and answering rows with `| … |` for the
rows left out, so two tables of the same subjects both fit; and a table
takes its heading's score when that is higher, so a claim naming "Table 3"
is shown Table 3's rows, not only its heading.
Equal-tier ties favor the passage answering more queries, so metadata cannot
exclude an entire table needed by several claims. Their compact matching lines are budgeted
beside other passages before spare space is filled, retaining headers and
section context. A single overlong line still uses a conservative prefix.

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
  analyses (`internal/ui/testdata/checker`); see [evaluation](evaluation.md).

What this guarantees, and what it does not: the report's answer section is
written by the program from what passed, so no statement the check blocked
is stated there, and every claim it stands on quotes a passage that is in a
page the run fetched. Whether a passage supports a claim, and whether claims
justify a statement, are the checking model's judgements. The explanation
the summarizer writes below the answer is model prose held mostly by its
instructions: it is given nothing but checked material, and told never to
assert what was not established. A supported claim on the side of a
conflict the approved answer was decided against (the conflict names a
claim an approved statement rests on, and this one, which none does) is
given apart from the facts to state, as true of what a source says and
never to be stated as fact. One thing in it is held in code, its
quotations of sources (`ui/quotes.go`): a double-quoted passage of six
words or more with a link in its sentence that is in no fetched page, nor
in the question, loses its quotation marks and keeps its words, as does
the executive summary taken from it. A quote must begin and end where the
page's words do, so "size is 10" is not found in "size is 100". An elision
(its pieces on one page, in order), a bracketed letter, a capital the
sentence needed, trailing punctuation, either quotation mark nested inside,
bold and links are accepted, as a reader accepts them; a quote containing
code is compared with code kept as written on both sides, and with its
quotation marks as written in its prose too (a page that lost its
backticks shows its code as prose); quotation marks in code and in link
titles are left alone. Words that would open a block once their marks are
gone ("1. Install …" at a line's start) are escaped. Only the
summarizer's text is checked, after the program has put its answer at the
top. The analysis's own wording
is not a source: quoted beside a link, it reads as the source's words. The
check narrows what reaches the reader as a source's words, and does not
prove the rest: a quotation under six words, one with no link in its
sentence, one after a lone quotation mark on its line (an inch mark, the
close of a quote begun on the line above, which pairs with its opening
mark), one cut inside a number at its separator ("costs 1" from "costs
1,000", a comma that also parts a list's items), or one that matches a page only once Markdown is read as prose
(code quoted without its backticks) is left as the summarizer wrote it.
The run's confidence is the analyzer's own rating of how well its
conclusions and recommendations answer the question asked and how well the
evidence supports them (the prompt defines high, medium and low): a rating
of the answer's adequacy, not a probability that everything printed is
true. It is made before the check, so it is lowered to low when the check
did not run, left a share of the claims unchecked, or approved none of the
answer's statements, whether the report was written or the run fell back
to the checked decision.
- Computed figures are the program's: the analyzer marks a numeric claim
  with `value` and `unit`, names operations over claim IDs
  (`computations`) and series to draw (`charts`), and never states the
  arithmetic itself. `ui/compute.go` evaluates them with `compute` twice.
  Before the check it uses the stated values alone, so every claim call is
  shown what they add up to. After the check (`settleFigures`, in `govern`
  before the units are blocked) only supported inputs count, and only when
  the value is a whole number token in a located passage the claim was
  supported on. A claim with `computed` is left out of the fact-check and
  decided against its computation: supported at its own precision,
  contradicted otherwise, insufficient when an input failed. A computation
  whose result is one of its input claims by construction (the quarters
  summed after the last was derived from the year's total) is refused as
  checking nothing: `echo` redoes it exactly with each input claim nudged
  by one, and a result that moves one for one with a single input and not
  at all with the others is that input restated. It is asked only of
  computations made of sums, differences and means throughout: being
  linear, a nudge moves them by each input's coefficient at any values, so
  the test is an identity; a product or ratio moves by a value-dependent
  amount (product(0, 1) would look like its first input). A
  recommendation or conclusion that states a number none of its claims
  states (in their text, option, criterion or scope) is blocked in
  `blockUnits`: its words are printed as the answer, and the inference
  check only asks whether it follows, so arithmetic done in a statement's
  own words would reach the answer unchecked. Charts are the program's to
  draw when the numbers are there: `autoCharts` groups the claims with a
  value by criterion and unit and draws each group of two or more that the
  analyzer did not chart, and `inferValues` gives a claim with no value the
  one number its text states (years and its scope's numbers aside), so a
  model that skips the value fields does not leave a run with no figures.
  The Markdown and PDF reports cite no ID: `ui/names.go` names each
  claim by its subject (or year, or own words), each computation by its
  label and each of the checker's "finding N" by that source's title,
  wherever the report prints a formula, an input, a reason or a fact-check
  note; the IDs stay in the `.json`, for the audit and tools.
  The report's "Key figures" section (the valid figures and the charts;
  formulas, inputs and failures are the `.json`'s and the Audit's), the
  chart SVGs (named by their content
  digest) and the PDF's vector charts are written from the governed
  analysis. The PDF and the serve page draw or show only the charts the
  run's own list names, never a file a report links to. What this
  guarantees is the arithmetic and that each input is a figure a fetched
  page writes; that the figure means what the claim says (the period, the
  unit, the population) is still the checker's judgement. A token such as
  "61,200" reads as 61200 or 61.2, since nothing in a passage says which
  convention it uses; the checker judges which one the claim means. A run
  of digits that is no valid number in either convention is read as table
  cells run together ("2,866,1182,834,4721" as 2,866,118 and 2,834,472),
  split where a group passes three digits, but only when the split gives
  two or more whole comma-grouped numbers: a typo ("1,000,0001") reads as
  nothing.
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

The same conversation edits the report. `ui.Editor` holds one
conversation's edit of one run: the proposal not yet saved, made at the
history's head, and the saves and undos it asks for. Its tools
(`edit_report`, `discard_edit`, `save_edit`, `undo_save`) are the only
chat tools that change anything, and they change it through the editor:
the model never names a file. `edit_report` applies find-and-replace edits
to the pending proposal or the report in effect and refuses a link to a
page the run did not read. A save is written by `ui.SaveRevision` under a
lock file beside the run (the terminal and `serve` are separate processes
on one reports directory): the revision's `.md` and `.pdf` first, under
names no other revision has, then `<name>.edits`, whose `head` counts the
saves and undos so a proposal made at another head is refused as stale. A
proposal's digest makes a repeated save one save, while the revision it
became is the one in effect; after an undo the repeat is stale. The run's
own files are never rewritten; `ui.WriteMetadata` removes the edits with
the chat log when the same question writes over the run, under the same
lock, and a save or undo first checks that the `.json` is still the run
its editor read (its time, question and report), so an edit of the old
report is refused (`ui.ErrReplaced`) rather than saved onto the new run.

What a turn did is the editor's to say (`ui.Outcome`: the diff, the note,
the revision in effect), not the model's, whose answer is written before
the save it describes. Each turn begins by reading the edits again, so a
save in another session drops a stale proposal, and the conversation is
reconnected whenever the revision in effect changes, since the report is
in the model's instructions. `agent.Chat.State` puts the pending edit's
diff ahead of each question. The terminal session, `ask` and the page drive
one lifecycle (`cli.talk`); the page's apply and discard buttons call the
same editor methods the tools do, and the server keeps one editor per run
(`cli.webTalks`), read again when the run's `.json` changes. Its tabs share
that editor, so each question carries the proposal its conversation was
shown, and `ui.Editor.Shown` sets any other aside for the turn: "save it"
in one tab cannot save the edit another tab proposed since.

In the terminal a run is a session (`cli/session.go`): the questions asked
about it after the run, or after `/resume` reopens it, are appended to
`<name>.chat.jsonl` beside its `.json` (`ui.AppendTurn`), and handed back
to `agent.Chat` as its turns when it is reopened. The log is append-only so
the run's own files are never rewritten, and `ui.WriteMetadata` removes it
when the same question writes over the run. `ui.Sessions` lists the reports
directory's runs and checkpoints by the question at the head of each file.
`ask` and the page keep no session.

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
            "service:<name>" entries) and model.json (the selection, and
            the ones before it for /model)
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
  setup     the Bubble Tea screens behind setup, login and model: the
            providers, then the chosen one's models, then the masked key
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
            questions about a run, a reviewed plan re-derived; the run, the
            answer, the setup and the plan edit are functions cli injects.
            The page draws the live frame as ui's Renderer does, from the
            same events; docs/surfaces.md is the contract between the two,
            held by TestSurfacesParity and the browser tests in
            web/browser (Playwright, `make web`)
config/     embedded default config.yaml and agent.yaml
integrations/
  pi        the Pi extension (TypeScript, its own package): drives the
            drsh binary over --jsonl, and answers drsh's model calls
            through a local OpenAI-compatible bridge onto Pi's model
```

The frame is clamped to the terminal and repainted in place; on a pipe it
degrades to one log line per event.

The settings a run takes are described once, in `ui.Settings`: each one's
key (the flag it becomes, through `Setting.Flag`), label, choices (the
depth tiers built from `DepthModes`, the sub-topic counts from
`MaxSubTopics`), and whether it is chosen before the plan (`Start`) or can
still change on review (`Review`, with its brief key). Every surface draws
from that list: the terminal start screen's settings row and its `/mode`,
`/topics`, `/sources`, `/follow-ups` commands, the brief's settings row,
keys and hints, and the serve page, which gets the list from `/api/info`
and launches with a `settings` map that `cli.webArgs` turns into the same
flags. Outside the binary, `drsh settings` prints the same list with each
option's flag (`Option.Arg`), and the Pi extension builds its tool
parameters, `/research` flags and settings screen from it. A review change goes through `ui.ApplyToPlan`, from the brief's key
or the page's `/api/plan`, which also returns the budget, so the page keeps
no copy of the tiers or the budget rules. A new setting is one entry there,
its flag, and its effect on the run.

## Layering

```
cli → { agent, auth, catalog, config, replay, setup, store, ui, update, web }
config → { agent, auth }
setup → { auth, catalog }
replay → agent
store → agent
ui → { agent, compute, tools }
agent → tools
tools → (standalone HTTP clients)
compute → (nothing in this module)
update → (nothing in this module)
web → (nothing in this module)
```

`auth`, `catalog` and `compute` import nothing in this module: `compute` is
the run's arithmetic (numbers as a source writes them, the operations, and
the chart layout both the SVG and the PDF draw), pure and deterministic. Imports go one way only. In particular `tools` must not import `agent`, and
sequencing must not move into `agent`.

`web` passes the run's `--jsonl` lines through unparsed, and the page, not
the server, reads the result from the `.json` artifact; the server only
renders that artifact's report Markdown for reading. So `web` needs
nothing from `ui`; a launch is the cli's own `--jsonl` command run
in-process. Nothing in the driver exists
for the page's sake: what the page needs and lacks belongs in the event
stream or the artifact.

`integrations/pi` is outside the Go layering altogether: it imports
nothing from this module and knows drsh only through its command line,
the `--jsonl` events, the `.json` artifact and `drsh settings`. Its
bridge (`src/bridge.ts`) is a local endpoint speaking the OpenAI chat
API, so drsh's own OpenAI-compatible provider is what talks to Pi's model;
nothing in `agent` knows Pi exists. A Go test decodes the extension's
event fixtures into `ui.Event` with unknown fields refused, and the
extension's tests run a drsh built from the tree against the bridge.

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

## Evidence context and report editing

The tools layer's built-in `direct` reader extracts public HTML without a
service. It validates resolved addresses and redirect targets, retains
headings, table rows and historical corrections, and falls back through the
existing snippet-quality signals on unsupported pages. An explicit `none`
still disables extraction. The Driver gives analysis and the bounded
follow-up call the plan's required facets; it distinguishes snippet evidence
in every phase and distributes a bounded excerpt allowance across sources
(up to 8,000 bytes per source, with a 1,500-byte floor for wide runs).
Publisher, edition, scope and numerical qualifiers are checked as part of
the assertion, rather than inferred from similar document titles. Claims may
carry an optional `qualifier` (such as "over" or "up to"); qualified figures
remain prose evidence and cannot be exact computation inputs or chart points.
Old claims without this field keep their existing behavior.

The checked answer remains program-written: governance is not bypassed by
an unrestricted prose rewrite. Prompts ask for at most three concise
conclusions and keep supporting detail in claims. The opening no longer
repeats blocked conclusion prose; the audit retains it, while the explanation
states material limitations in reader terms. The summarizer adds explanatory
substance without repeating the opening or narrating the pipeline.
