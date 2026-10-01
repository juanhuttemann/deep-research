# Changelog

All notable changes to this project are documented here. The format is based
on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

### Added

- `deep-research serve` runs a page on localhost that launches a run,
  streams its live frame (phase, sub-agent rows, the status line, source
  and token counters) and then shows what the fact-check decided: the
  answer, the statements it blocked and the claim that failed, each claim
  with its verdict and quotes, and the page text the check located each
  quote in, with every source marked fetched, snippet only or never
  fetched. Research opens the plan for review first, as the CLI's brief
  does: rename, add and remove sub-topics, or change the depth. The start
  screen lists past runs from the reports directory.
  The stream is the `--jsonl` one over SSE; a dropped connection resumes
  where it left off, a reload replays the run, and closing the tab does
  not cancel the run.
- `--plan-only` makes the plan, prints it as JSON and stops before any
  search; `--plan FILE` runs such a plan, edited or not, without planning
  again; `-o FILE` writes the plan there. Renaming, adding and deleting
  sub-topics was only possible in the interactive brief. Every `--jsonl` run now emits the plan it executes as
  a `plan` event before the first search.
- The `.json` artifact carries `pages`, the stored text of every fetched
  page, and `passages`, each fact-check quote resolved to its page and
  whether the check found it there. A quote could be read before, but
  not checked against the text it claims to come from.

### Changed

- A reasoning model's thinking is shown while it runs: the latest of it,
  wrapped in up to five rows under the status line. The status read
  "waiting for the first token" for the whole of it, often minutes,
  because the reasoning streams separately from the answer and was
  dropped. Logs and plain output keep the status line alone.
- The analysis status names the part being written ("drawing
  conclusions · 12 claims") instead of counting "sections".
- "waiting for the first token" is now "waiting for the model to start",
  and "no new text" is "no new output".
- A sub-agent row counts "12 sources" (or "1 source"), not "12 src".
- The follow-up round's rows sit under a "follow-up searches for the
  analysis's open questions" divider, and the footer splits the total
  ("29 sources (20 planned + 9 follow-up)"). A deep run over four
  sub-topics showed 29 sources with nothing saying why.
- The analysis and the fact-check are no longer requested in JSON mode
  (`response_format: json_object`). In that mode the provider held a
  reasoning model's thinking back and sent it in one piece after a minute
  of silence, so the wait showed nothing; without it the thinking streams
  from the first second. Output that comes back as broken JSON (a
  trailing comma, an unescaped quote) is repaired with
  `github.com/kaptinlin/jsonrepair` before it is given up on.

### Fixed

- A trace recorded the planner's first answer, which the brief then edited
  in place, so it could hold a half-edited plan, and a replay sent it back
  through the planner, which cut it to the tier's sub-topic count. A trace
  now records the plan the run used, with its tier and budget rather than
  the flags' (which a `--plan` run or a tier changed in the brief made
  wrong), and a replay runs it whole.

- A claim's sources are recorded as the URL the page was fetched as. The
  model's spelling was kept, so `https://x/a/` beside `#1` for the page
  fetched as `https://x/a` listed one page twice and matched none of the
  citations.
- A search server that is not running failed the run with the raw
  transport error, a URL-escaped query and `dial tcp [::1]:8888: connect:
  connection refused`. It now says `no search answered: cannot reach
  SearXNG at http://localhost:8888 (connection refused)` and to check
  `SEARXNG_URL` and that SearXNG is running, with the new search status
  `unreachable`. Each failed search no longer reads "search failed: search
  failed:".
- A run cancelled while its plan call was still out ended as `failed`
  with "plan: context canceled"; it ends as `cancelled`, like a cancel at
  any later point.

- Cancelling a run while a model call is still connecting ends it
  `cancelled`, not `failed` with "cannot reach the model provider".
- Credentials in `OPENAI_BASE_URL` no longer appear in provider or `doctor`
  errors, which reach stderr, `--jsonl` and the run history.
- One failed or cancelled lookup of public SearXNG instances no longer fails
  every later search in the run; the next query looks again. Each search is
  bounded to one minute across the lookup and every instance it tries.
- The repair pass keeps one revision per blocked statement. A second one was
  also approved, and the answer stated two replacements for one statement.
- An `agent.yaml` from an older release warns on stderr. Its prompts were
  used silently; an old fact-check prompt returned no verdicts, and every
  answer said the evidence did not establish its conclusions.
- A report with text the PDF font cannot show (Cyrillic, Greek, CJK, ...)
  warns that the `.pdf` shows it as `?`; the `.md` and `.json` keep it.
- `http://host:80/x` and `http://host/x` are one source, not two.
- A finished desktop notification no longer leaves a zombie process.
- `.env` and the generated `searxng/settings.yml`, which holds a secret key,
  are created readable by their owner only.
- A trailing slash on `FIRECRAWL_URL` no longer doubles the slash in the
  scrape path.
- `make verify` reports unformatted files instead of rewriting them, so it
  fails where CI fails; `make fmt` rewrites.
- `doctor`'s request budgets for LLM search are documented as it prints
  them (quick 19, standard 21, deep 25).
- An answer cut off at the model's output limit fails the phase instead of
  being parsed as if complete. A cut-off report is still kept.
- A page already cited with its full text is not fetched again by later
  searches, and its slot goes to a page the run has not read. A run
  fetched 35 pages to cite 20. The later query is still recorded on the
  page, and a page judged off-topic for one sub-topic, or cited from its
  snippet alone, is still fetched by another.
- Follow-up searches are named by the analysis ("LLM inference
  benchmarks"), not after their query, which was cut off at the name
  column beside the same query in full. A query given without a name, as
  older analyses and custom instructions do, is still its own name.
- Sub-topic names that all open with the question's subject ("Intel Arc
  Pro B70 vs AMD Radeon AI Pro R9700 …") lose it. The live rows were cut
  off before the part that differs and all read the same, and the search
  anchored on each name sent the subject twice and lost the facet to the
  query length limit.
- A rejected API key is reported as one line with the provider's reason
  ("API key expired"), not followed by the request URL and raw JSON body.
- The planning spinner is clipped to the terminal width. A status wider
  than the terminal wrapped, and every repaint left a stale copy behind.

## [0.3.0] - 2026-09-28

### Added

- `--jsonl` runs end on one `done` event, written after the report files:
  `status` (`complete`, `incomplete`, `failed`, `cancelled`), the error in
  `detail`, and the files written in `artifacts`. A calling program no
  longer has to guess report filenames or read fatal errors off stderr.
  See "Driving it from another program" in `docs/usage.md`.
- `make e2e` runs the built binary against the real provider and search. It
  is not part of `make verify`.
- `make eval-replay` measures the fact-check on its own: the live checker
  and the governance code run on frozen analyses whose right outcome is
  known (and/or, consider vs choose, attribution, jurisdiction, units, an
  added condition, a study's scope, negation) and it reports false
  approvals, false blocks, false accepts and false rejects. `EVAL_REPEAT`
  repeats each case to measure variance. Replaying a recorded run
  re-runs the analysis too, so it compares analyses as much as checkers.
- `search` events carry the `terms` their results were judged against, so
  the `.json` timeline and `--jsonl` show why a result was called off-topic.
- `--trace` writes `<report>.trace.json` with the plan, every search result
  (full page text) and each model phase's prompt and output, and
  `--replay TRACE` runs analyze, fact-check and summarize again on that
  recorded evidence without searching. A live run plans and searches anew
  each time, so a prompt or model change could not be told apart from a
  change in what the web returned. See "Replaying a run" in
  `docs/usage.md`.
- A repair pass for blocked recommendations. A recommendation blocked for
  an over-broad condition took an option the evidence supported out of the
  answer: a heating question's dual-fuel option said "or" where its claim
  said "and", a programming question's Python option added a condition no
  claim stated. When a recommendation is blocked and some claims passed,
  the analyzer revises it from the supported claims only, and the
  fact-check judges each revision; an approved one replaces its original
  in the answer, which stays in the `.json` as blocked. It costs an analyze
  and a fact-check call, only in a run where something was blocked.
- A follow-up round: after the first analysis, its first three follow-up
  queries are searched as extra sub-agents, and the run analyzes once more
  if they found evidence. The report used to list them as "suggested
  searches" and leave open a question one search would have settled. A run
  with search is now 4 or 5 model requests; `doctor` counts the fifth.

### Changed

- Every answer is checked, not only a choice: the analysis states its
  conclusions, each on the claims it names, and they are governed as
  recommendations are (premises supported, the check judging that the
  statement follows, the repair pass, the answer written from what passed).
  An explanation, an account of events or a forecast was answered in the
  summarizer's unchecked prose. A forecast has to state its assumptions and
  horizon, an opinion who holds it. An analysis with claims and no
  conclusion says so instead of falling back to prose. The explanation
  below the answer is still written by the summarizer from checked
  material; docs/architecture.md says what is and is not guaranteed.
- The prompts no longer assume a product comparison. How a source's
  authority is weighed is set per kind of claim (a treatment's effect,
  history, statistics, news, law, a product) instead of by a product's
  documentation and pricing page; a claim's scope can be a period, place,
  jurisdiction, population or definition, not only a product version; the
  report is organized as the question needs (a mechanism, a chronology,
  findings, scenarios, arguments) with a comparison table only for a
  comparison; recommendations are made only when a choice is asked for.
  Sub-topic names and notes are written in the question's language, not
  English, since a name is searched after the question when the query
  finds too little. Fact-check quotes stay in the source's language. With
  search off the search agent names pages it recalls and describes them,
  instead of writing "excerpts" from memory.
- The words the program writes into a report itself (the answer heading,
  "not established", status names, why nothing was approved) come in the
  question's language: the analysis supplies them, and English is used for
  any it leaves out or that carries Markdown. A Spanish report used to
  read "Not established in this run" between Spanish sentences.
- When nothing is approved the answer says why: the check did not run, no
  source was retrieved, or the evidence does not establish the proposed
  conclusions; it used to say only that no recommendation passed.
- The fact-check governs the report. It returns one verdict per claim ID,
  and code holds the answer to them: a recommendation is approved only when
  every claim it names is supported and the check judges that it follows
  from them (supported premises do not make a conclusion follow), and one
  whose deciding claim failed no longer stands on an incidental claim that
  passed. The answer names each recommendation that was not approved as
  not established, with the claim that failed, so a two-option question
  answered for one option no longer reads as a win for it. A verdict for a
  claim ID the analysis does not have, a missing or duplicated verdict, or
  a "supported" verdict with any quote that is not in the cited page
  counts as insufficient. Quotes are matched through what text extraction
  does to a page: Unicode normal forms and ligatures, HTML entities and
  `<br>` in table cells, citation markers such as "[43]", and words
  hyphenated across a PDF's line breaks; digits a PDF did not encode
  readably are not guessed. With search
  off no claim can be supported, since
  the findings are the model's own. A fact-check that did not run approves
  nothing, and neither does an analysis whose recommendations name no
  claim. The report's answer section is written from the approved
  recommendations in the analyzer's own words, or says that none passed;
  an answer section the summarizer writes anyway, or a paragraph above its
  first heading restating the answer, is dropped. The
  summarizer no longer sees the analyzer's unchecked answer prose, nor
  does the fallback report written when the summary fails.
- The analysis returns a decision: how it reads the question, atomic claims
  with the option, criterion, scope and sources of each, conditional
  recommendations ("choose X when Y") naming the claims they rest on, and
  disagreements resolved by which source is placed to know. Reports listed
  facts, called a point "contested" when a third-party blog disagreed with
  the product's own documentation, and stated no answer where the evidence
  supported one. Code, not the prompt, holds it to the run: a claim may cite
  only pages the run fetched, claim IDs are unique, and a recommendation
  stands only on claims that kept a source; one left with none is dropped
  and recorded as an open question. The fact-check now checks the claims,
  and scope as well as quotes.
- The report leads with the answer, then compares the options, the
  reasoning and the limits that could change the answer. The Markdown no
  longer appends the evidence by topic, the gaps and every confirmed claim
  after a body written from the same material: it lists the suggested
  searches, the fact-check counts and each claim not confirmed. The `.json`
  sidecar carries the whole `analysis`.
- A question is asked with `deep-research -p "question"`; the `run`
  subcommand is gone. The run flags (`--mode`, `--silent`, `--jsonl`, …)
  moved to the root command.
- Search queries and relevance ranking come from the planner, in the
  question's language: each sub-topic carries a keyword `query` and the
  `terms` a relevant page must mention. They replace two English stopword
  lists that built queries from the question's prose and scored results on
  its words. A non-English question no longer searches its own sentence with
  an English facet appended and the subject clipped off, and Japanese or
  other unspaced scripts are now ranked instead of left unfiltered or
  rejected wholesale. A term matches regardless of accents, a phrase matches
  on all of its words in any order, and a qualified identifier
  (`sync.RWMutex`) also matches bare (`RWMutex`). A custom `planner_instructions`
  should ask for the two new fields; without them a run searches the anchored
  question unranked. A sub-topic renamed or added in the brief is searched by
  its name, since the planner's query was written for the one it replaced.

### Removed

- Offline mode (`--offline`, `offline:` / `DEEP_RESEARCH_OFFLINE`). Its stub
  assistant researched nothing, so a run with it only exercised the CLI;
  a stray `offline: true` in a config file now does nothing.

### Fixed

- Claim IDs the analyzer wrote into a conclusion ("two deployment options
  (c1)") reached the answer; they are removed when it is printed. A
  conclusion no longer restates a recommendation beside it.
- An analysis that came back malformed (no claims, nothing to govern, only
  JSON: a model returned `{"/": "placeholder"}`) sent the report to the
  unchecked-prose path; it is asked for once more. A re-analysis after the
  follow-up round that comes back with no claims no longer replaces a first
  analysis that had them.
- Every web source was labelled "high" confidence, a snippet-only one
  included, and the report printed it by each citation. Sources now carry
  only how they were obtained.
- A `--trace` recorded the analysis after the run had edited it (sources
  dropped, recommendations blocked) as the model's output. Each phase's
  output is recorded as it was returned.
- The fact-check sees each page excerpted by the claims it checks: the
  passage bearing on each claim that cites the page first, then on each
  other claim, since a page a claim does not cite can contradict it. It
  used to see the passages the search that found the page asked for, and
  verified a blog's "Savings Plans do not apply" while the vendor's own
  pricing page, among the sources, said otherwise further down. Each
  recommendation is given to the check with the text of the claims it
  names, and judged against them alone: listed by ID beside dozens of
  claims, an "or" where its rule said "and" was approved in five checks of
  eleven once the excerpts carried more text on the topic.
- The model saw the first 1500 characters of each source, which on a
  documentation page were navigation and a cookie dialog. It now sees the
  passages that match the queries that found the page, and pages are
  scraped up to 40,000 bytes instead of 5,000 so there is text to choose
  from. Each passage keeps its section: the nearest heading above it (and
  the section heading above a bold sub-heading), with the sentence that
  opens the section, so a figure from one product's or engine's section is
  not read as applying to another. A page found by several searches is still one source, but each
  search's query now steers its excerpt instead of being discarded.
- The analysis's open questions never reached the summarizer, which wrote
  conclusions over the gaps the analysis had flagged. They are now passed
  to it, and the analyzer and summarizer are told that "not found" is not
  "no". That instruction is a prompt, not a guarantee.
- The planner is asked to put the question's subject among each sub-topic's
  relevance terms, since it wrote only words that set a sub-topic apart and a
  page about the subject worded differently was skipped as off-topic. It is
  also asked to name the options a comparison compares and research each;
  a plan could miss one side of a comparison entirely, and still can when
  the model ignores the instruction.
- `init` said it wrote every file to the config directory, `.env` included,
  when `.env` goes to the working directory. Each file is now listed by its
  absolute path.
- `--jsonl` carried none of the assistant's progress: no retries, none of the
  streamed status lines, and nothing at all while planning. A slow model call
  and a hung process looked the same on the stream. They now arrive as
  `info` events, the status lines marked `transient`.
- A rejected API key (401, e.g. an expired OpenRouter key) fails at once
  with a message naming `OPENAI_API_KEY`, where it used to retry through
  minutes of doubled deadlines before failing with the raw response.

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

[Unreleased]: https://github.com/juanhuttemann/deep-research/compare/v0.3.0...HEAD
[0.3.0]: https://github.com/juanhuttemann/deep-research/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/juanhuttemann/deep-research/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/juanhuttemann/deep-research/releases/tag/v0.1.0
