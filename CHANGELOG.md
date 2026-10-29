# Changelog

All notable changes to this project are documented here. The format is based
on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

### Added

- `--topics N` and the `sub_topics` key ask the planner for exactly N
  sub-topics (1–8) in any mode; the serve page's form has a "Sub-topics"
  choice for it.
- `--no-follow-ups` and the `follow_ups` key skip the follow-up round. The
  terminal brief's `f` and a "follow-ups" switch in the serve page's plan
  review turn it on or off for one run; plans, checkpoints and traces carry
  it as `no_follow_ups`.

### Changed

- The run settings (depth, sub-topics, pages per topic, follow-ups) are
  described once (`ui.Settings`) and every surface draws them from it: the
  terminal start screen shows them under its box and changes them with
  `/mode`, `/topics`, `/sources` and `/follow-ups`; the brief's settings
  row, keys and hints come from it; and the serve page draws its form and
  plan-review controls from it (served with `/api/info`) instead of its own
  copies of the tiers, and asks the server (`/api/plan`) for the budget a
  review edit comes to.
- The planner chooses how many sub-topics a question needs, up to the
  mode's count (quick 3, standard 4, deep 6), instead of always that many,
  and a plan of fewer reads fewer pages. The follow-up call is told an empty
  list is the right answer when the findings already cover the question.
- The brief's key hints wrap onto a second row on a narrow terminal rather
  than losing the last hint.

- The live frame's activity tail is hidden at the start of a run; `a`
  shows it and hides it again, and the key hints say which.
- The start screen's key hints no longer list `[tab] use the example`;
  Tab still takes it.
- The planning spinner says how long it may wait as `2m`, not `2m0s`, and
  no longer names the plan call's reasoning effort.
- The brief no longer shows the question as typed (`typed as:`) under the
  planner's corrected one.

### Fixed

- The brief is drawn in place, under what the terminal shows, as the start
  screen is, and cancelling it clears it: the start screen comes back on
  the rows it had, with the `cancelled` notice on it, where the old brief
  stayed on the screen with the start screen drawn under it.

## [0.25.0] - 2026-10-27

### Added

- The `research ▸` and `ask ▸` boxes complete their commands: typing `/`
  opens a menu of `/resume` and `/model` under the box, the part typed
  bold, where Tab completes the highlighted one, the arrows move, and
  Enter on the start of one runs it. Their key hints no longer list
  `/resume`, which the menu shows.

### Changed

- The live frame's activity tail names each row's sub-agent as the tree
  does, in the colour its tree row now has, where it showed the
  sub-agent's ID (`1`, `f3`). A phase starting is bold and heads the rows
  under it, a search reads `search` and its query, and what the run
  reports of itself, the search engine's results, the model a phase uses,
  a filter's count, is gray.
- The `ask ▸` box is read a key at a time, as the `research ▸` box is,
  where the terminal used to read the line: a question longer than a row
  still wraps, under its first column, and Esc goes back as an empty line
  does. A terminal that will not go raw reads the line as before.
- The start screen and the `/resume` and `/model` lists are drawn in
  place, under what the terminal shows, as the `ask ▸` box is. The lists
  opened on the alternate screen and the start screen from the top of the
  screen, and in Warp, which shows output at the foot of the window, the
  input moved between the foot and the top. The lists are cleared when
  they close, so the report above them shows as it was.
- The live frame, the brief, the `ask ▸` box and the `/resume` and
  `/model` pickers end on a blank row under their key hints, as the start
  screen does.

### Fixed

- The `ask ▸` box drew its label twice, the second over its lower rule,
  in Warp after `/resume`: it moved back up to the label by a count of
  rows, which landed a row short there. It now asks the terminal where the
  cursor is and draws each row at its own position.

## [0.24.0] - 2026-10-26

### Added

- A run is a session. Its questions and answers are kept beside its
  `.json`, in `<name>.chat.jsonl`, and `/resume`, typed in the `research ▸`
  or `ask ▸` box, lists the runs in the reports directory, the last one run
  or asked about first, filtered as you type. Enter on a finished run
  prints its report and the conversation so far and takes the next
  question; on an interrupted one it continues the run, as `--resume`
  does. A question asked again writes over the earlier run's files, and
  its conversation goes with them.
- `/model`, in the same boxes, lists the model runs use and the ones picked
  before it, newest first; "other model…" opens `drsh model`'s picker. The
  pick is saved as `drsh model` saves it, and the next run and the open
  report's questions use it. `model.json` keeps the last ten picks under
  `recent`.

### Changed

- In a bare `drsh`, the questions about a report are the session: an
  empty line goes back to the `research ▸` box for the next run, where it
  used to end the questions and the screen came back on its own. After a
  `-p` run an empty line still ends them. Either way the question is typed
  in an `ask ▸` box drawn like the start screen's, the keys under it.
- A run that fails, or a command the screen does not know, is said on the
  start screen, whose first paint erased the error printed before it.

## [0.23.0] - 2026-10-25

### Added

- A bare `drsh` on a terminal opens a start screen instead of the help,
  as `serve`'s page does in the browser, and in the page's words: the
  title with the model and search in use, as the page's top bar names
  them, and the `research ▸` box with an example that Tab takes. It is
  drawn like the live frame, key hints included. Each question runs as `-p` would, brief and follow-up
  questions included, and the screen then asks for the next; Esc ends it.
  Piped, or with `--silent` or `--jsonl`, it still prints the help.

### Changed

- The page's start screen no longer lists the key hints and the setup
  (model, provider, search, page text) under the question box. The box's
  own button says Enter, and the top bar already names the model and the
  search, with the hosts on hover.

- The activity tail gave each page two rows, `read www.example.com` and
  then how it went, so a branch of five sources pushed ten rows through it.
  A page is now one row: a spinner while it is read, which starts as the
  fetch does instead of once it is over, and then a dot in the same row,
  green when read in full, yellow for snippet only, red when dropped (with
  colour off, `✓`, `!` or `✗`). The row names the page, site and path,
  where it named only the site, so two pages of one site read as the same
  page twice. In `--jsonl`, `read` now arrives as a fetch starts rather
  than after it ends.
- A page read in full or as a snippet is a link to it in the activity tail
  (an OSC 8 hyperlink, which most terminals open on ctrl-click), so a
  source can be checked while the run goes on. Only `http`/`https`
  addresses are linked, and a URL with anything a terminal would read as a
  control is not. A path's percent-encoded control characters are dropped
  from the row: decoded, a `%1b` reached the terminal as an escape.
- The key hints were gray from end to end, so the keys were lost among the
  words around them. Each key (`[enter]`, `[esc]`, `[b]`, …) is now in the
  terminal's own text colour and what it does stays gray.
- The search tool logged every page's outcome too (`ok en.wikipedia.org`,
  `snippet …`, `known …`), so each page also got a third row in the tail,
  and a page fetched past the branch's budget, never used, got one of its
  own. The search's own line, how many results and from where, is the only
  one it logs now; the pages are reported once, by the run.

### Fixed

- In the brief's add and rename prompts an arrow key, which arrives as a
  sequence starting with Esc, was taken for Esc and threw away the typed
  name. It now does nothing, and Ctrl-C abandons the prompt as Esc does,
  where it was ignored.

## [0.22.0] - 2026-10-25

### Changed

- The report's confidence had no definition: the analyzer was asked for
  "high/medium/low" and nothing else. In the 14 saved runs it put eight of
  nine good answers at "medium", a run on how π's digits are computed
  among them, and an opinion question it barely answered at "high". The
  analyzer prompt now defines the scale as how well the answer covers the
  question asked and how well the evidence supports it, decided in order:
  low when the central request goes unanswered, high when every material
  part is answered with suitable evidence, medium between. Gaps about what
  the question did not ask lower nothing, a source counts by whether it
  can establish its claim rather than by its kind, and an opinion question
  is rated by how well the evidence reports its positions. A gap the
  analyzer lists that leaves the central request unanswered now makes the
  rating low, and the JSON field itself states that rule. On the 14 runs'
  evidence, re-analyzed three times per prompt and graded blind,
  deepseek-v4.1-flash's rating now separates good answers from the rest
  (AUROC 0.95, from 0.85) and rates π high and the opinion question low;
  gemini-3.5-flash still rates some answers high whose claims overstate
  the evidence (AUROC 0.80, from 0.73).

### Fixed

- The PDF printed every Greek letter as `?`: a report on π opened its
  section "How ?'s decimal digits are produced" and lost each of its thirty
  π. Greek letters and mathematical signs Helvetica lacks are now drawn
  from the standard Symbol font, which every viewer has, so nothing is
  embedded; `→`, `≤`, `≠` and `≈` print as themselves instead of `->`,
  `<=`, `!=` and `~`, and a styled letter such as `𝜋` prints as `π`.
  Accented Greek, Cyrillic and CJK still become `?`, with the warning.

## [0.21.1] - 2026-10-25

### Fixed

- The report's quote check matched a quote anywhere in a page's text,
  inside its words too, so a quoted "size is 10" stood on a page's "size is
  100 MB" and "secure for production" on its "insecure for production". A
  quote, and each piece of an elided one, now has to begin and end where
  the page's words do; a bracketed edit may still change part of a word
  ("[t]he", "reduce[]").
- A quote resumed after an elision at a recapitalised word ("… [t]he") lost
  its marks unless the two passages sat a few bytes apart on the page.
- The executive summary kept an invented quote's marks that the report had
  lost, so the JSON export's `executive_summary` still showed it as the
  source's words, and the export printed the paragraph twice.
- A claim stating a position the answer was decided against ("one source
  claims the highest inclination is 72.4°") passed the check as an accurate
  report of the page and reached the summarizer among the only facts to
  state, while the analyzer's resolution against it was withheld. When a
  conflict names a claim an approved statement rests on and supported
  claims none does, those reach the summarizer apart, never to be stated as
  fact. Not measured on replays yet.
- When the report could not be written, the fallback kept the analyzer's
  confidence after a fact-check that left a share of the claims unchecked;
  it is now low, as for a written report.
- More of the quote check: a sentence opening with a lower-case quotation,
  or following "the answer is no.", is no longer read as part of the
  sentence before, whose link then attributed its quote; the prose of a
  quote containing code is normalised as prose is (a PDF's "ﬁle" is
  "file"); an elided quote of a PDF passage broken across lines is found
  as the whole one was; words that would open a Markdown block once their
  marks are gone ("1. Install …", "# of users") are escaped; quotes in an
  answer section the summarizer wrote anyway are no longer counted in the
  "n quoted passages" message, since the section is dropped; and the check
  reuses the fact-check's page texts instead of normalising every page
  again.

## [0.21.0] - 2026-10-24

### Changed

- A position the evidence resolves against stays out of the answer. Given
  one planted page stating a wrong answer outright (as DRNOISE, arXiv
  2607.17291, does), the fact-check was not fooled, but the analyzer wrote
  "a page states X" claims, rightly supported, and built answer statements
  on them: "One source claims the highest crewed inclination is 72.4°" was
  approved into an answer. `analyzer_instructions` now says to follow the
  records over a page that only states the answer, and to keep a rejected
  position in `conflicts`, out of the conclusions. On replays of five real
  runs with the planted page on a realistic domain (20 per side), an
  approved statement rested on the page in 15 before and 5 after, the true
  value was in the approved answer in 18 before and 20 after, and graded
  blind, 2 answers before and none after were damaged by the page (its
  value restated as the answer, the true one missing). On an obviously
  fake domain, 14 before and 1 after. Runs without the page stayed right.
- The summarizer's quotations of sources are checked against the pages. A
  double-quoted passage of six words or more, with a link in its sentence,
  that is in no fetched page nor in the question keeps its words and loses
  its quotation marks. Elisions (pieces on one page, in order), bracketed
  edits, a changed capital, either nested quotation mark, bold and links
  are accepted; a quote with code is compared with code as written;
  quotation marks in code and link titles are left alone. On 12 fresh runs
  the rules were not worked out on, it removed 6 of 184 such quotations, 5
  of them paraphrases or added words presented as a source's, and kept no
  linked one the pages do not hold. The check narrows what reaches the
  reader as a source's words and does not prove the rest: shorter or
  unlinked quotations are not checked, and one that matches a page only
  once Markdown is read as prose (code quoted without its backticks) keeps
  its marks.
- A report whose fact-check approved none of its statements says low
  confidence: the analyzer rates its confidence before the check. The case
  did not come up in the 68 A/B replays; it is a rule for it, not a
  measured change.
- `make eval-replay` with `EVAL_REPEAT` above 1 lists each claim or
  statement whose outcome changed between repeats, and the checker cases
  gain three DRNOISE cases (`drnoise-*`).

## [0.20.0] - 2026-10-22

### Changed

- The analysis runs once. The follow-up searches came from a whole first
  analysis, which a second one replaced once their evidence was in: two
  full analyses back to back in nine runs of ten, 63 to 128 seconds of the
  first kept only for its three queries. A short call now names the
  searches (13 seconds and a tenth of the reasoning on the same findings),
  and the one analysis reads everything found. Its prompt is
  `follow_up_instructions` in `agent.yaml`. A replay serves the recorded
  run's follow-up searches without asking the model.

- The fact-check runs as parallel calls. It was the longest call of a run
  (213 of 514 seconds in one, nearly all of it reasoning before the first
  verdict), and its time grows with the claims it judges: the claims are
  now split into up to four shares of six or more, each checked against
  every finding in its own call, and the recommendations and conclusions
  are judged against their premises, up to four to a call. A call that
  fails leaves only its share unverified, and the run's confidence low. A
  run spends up to 13 model requests instead of 7 before retries (`drsh
  doctor` prints the new budgets).
- The repair pass's recheck judges only whether each revision follows from
  its claims. It was given the claims and their pages again and re-verified
  them, a minute of reasoning for verdicts it then ignored.
- The fact-check's reasoning in the page is shown per call, and its
  `thinking` events under `--jsonl` name their call in `part`.

### Fixed

- The fact-check's progress line said "0 claims checked" until it ended; it
  now counts the statements judged.

## [0.19.0] - 2026-10-22

### Added

- `drsh update` and `update --check` print "What's new:": the changelog
  entries of every release between the installed one and the latest, so an
  update no longer ends on just a version number.

### Changed

- The web UI's question box no longer always suggests "heat pump or gas
  furnace for a house in a cold climate?". It shows a hot question from
  Stack Exchange's space, history or physics site, or one of ten built-in
  questions until that arrives or when offline. Tab in the empty box takes
  it as the question.

### Fixed

- The install scripts ended on "next: drsh init, then put your API key in
  .env", the setup `drsh setup` replaced in 0.16.0. They now say
  `next: drsh setup`.

## [0.18.2] - 2026-10-21

### Fixed

- A run warned that its PDF had replaced characters with "?", over one
  emoji in a source's title ("Top 20 Raspberry Pi Projects of 2025🤯!").
  The standard PDF fonts draw no emoji. Emoji are now left out of the PDF's
  text, so the warning is kept for text it cannot show: a script the fonts
  cannot draw (Cyrillic, Greek, CJK, Arabic, Hebrew) or a symbol that means
  something ("C♯", "★"). The Markdown's syntax and link targets are not
  touched.

## [0.18.1] - 2026-10-21

### Fixed

- The question's correction did nothing for "best selling modules for dev
  boards (arduino, raspberry pi, esp32, etc": the planner's title-cased fix
  was refused whole, since "Best-selling" joins two words and "etc.)" changed
  a three-letter word. Case, hyphens and the punctuation around words are no
  longer counted as changes, so capitalization, brackets and punctuation are
  fixed along with spelling; adding, dropping, reordering or replacing a
  word, or changing a number or a symbol in a word ("C++", "<5%", "−1000"),
  is still refused. The planner is asked to
  write the question as a title, and a refused fix is now named in the
  progress log instead of passing in silence. The browser's plan review
  lines the two up by the words they share, so a hyphenated pair is shown
  replaced rather than not at all.
- A report's file name gave each separator its own dash
  (`dev-boards--arduino--raspberry-pi`); a run of them is now one.

## [0.18.0] - 2026-10-21

### Added

- The planner fixes misspelled words in the question, which titled the
  report, its `.md`/`.pdf`/`.json` files and the history entry as typed:
  "Best cloud storage provider for personal files overalll" kept its extra
  "l" everywhere. The fix comes back in the planning call itself, so it costs
  no extra request, and is taken only word for word: the same words in the
  same order, each a few letters off, with words of three letters or fewer
  and any word with a digit unchanged. A correction that dropped "not" or
  turned "Go 1.26" into "Go 1.25" or "-1000" into "1000" would have
  researched another question, so it is ignored. The brief shows the
  question as typed under the fixed one, the browser's plan review strikes
  through each word replaced, and the plan JSON carries it as `asked`, which
  `--plan` keeps. A question typed over several lines
  is joined into one, and a trace records the question as planned, so its
  replay and resume search the same fallback queries. A custom
  `planner_instructions` that does not ask for `question` keeps the question
  as typed.

## [0.17.0] - 2026-10-21

### Added

- `reasoning_effort` sets how hard each phase's model reasons: `plan`,
  `search`, `analyze`, `fact_check`, `summarize` and `chat`, each `default`
  (send nothing), `none`, `minimal`, `low`, `medium`, `high`, `xhigh` or
  `max`, in `config.yaml` or as `DEEP_RESEARCH_REASONING_EFFORT_<PHASE>`.
  Thinking was about 80% of every long model call in the runs measured, and
  nothing could change it. The planner now asks for `low` and follow-up
  questions for `medium`; the analysis, fact-check and report keep the
  provider's default. Each pipeline call's progress line says the effort it
  sent. A level the endpoint refuses (Gemini refuses `none`) is asked again
  without it and dropped for the rest of the run, and Claude gets `low` for
  `none` and `minimal`, since it cannot turn thinking off. Claude Code is
  run without `CLAUDE_CODE_EFFORT_LEVEL`, which would override `--effort`.

### Fixed

- A fact-check that answered with no verdict at all was read as every claim
  insufficient: a replay reported "0 of 20 claims supported" and no
  established answer, with no error, after the checker had reasoned through
  every claim. A check given claims that judges none of them is now asked
  once more, and empty twice it is reported as unavailable (the report is
  unverified, at low confidence). What it answered is logged, since nothing
  else keeps it.

## [0.16.0] - 2026-10-19

### Added

- `drsh setup`: one command that walks through the model, web search and
  page extraction, explaining each choice. Setting up used to take `init`,
  `login`, and hand-editing `config.yaml` or `.env` for search and
  Firecrawl, and users found that confusing. Each step starts on what runs use
  now, the model step has a row to keep it, and nothing is saved until a
  review that names any environment variable that would override a choice
  and where it was set. Search and scraper are checked with one query and
  one page read before saving. `drsh setup model`, `setup search` and
  `setup scraper` rerun one part. `setup search` asks about page extraction
  too when the new engine needs it, and `setup search --engine` and
  `setup scraper --scraper` script them. Search settings go to `config.yaml`
  with the rest of the file left as it was; `.env` is never written.
- Exa, Tavily and hosted Firecrawl keys can be stored in `auth.json`
  (`service:exa` and so on), each bound to its service's address. A stored
  key wins over `EXA_API_KEY`, `TAVILY_API_KEY` or `FIRECRAWL_API_KEY`, as a
  stored model key does over `OPENAI_API_KEY`, and `drsh logout exa`
  removes one.

### Changed

- The README's quick start, the missing-key error and `doctor`'s hints
  point to `drsh setup` instead of `drsh login`, `init --docker` and
  environment variables. `init`, `login` and `model` work as before.
  Environment variables still override everything, and the docs now present
  them as the way to configure CI and one-off runs.
- The model picker's search box (`login`, `model`, `setup`) was barely
  visible: its prompt, cursor and placeholder were fixed grays, and on any
  terminal background one of them nearly vanished. They now use the
  picker's cyan accent and the terminal's own dimmed text.

- The page's top bar named the search engine alone (`searxng`), leaving out
  Firecrawl, which reads every page a claim is checked against. It now reads
  `searxng + firecrawl`, or `· snippets only` when nothing reads the pages;
  the hosts stay in the tooltip and on the start screen.
- Audit cards now open their supporting claims as full statements with
  verdicts and links to inspect the evidence, replacing internal IDs, claim
  chips and the label glossary. Blocking reasons link to the relevant claims,
  and revisions link to earlier statements. Verdicts use readable names;
  the inspector explains source availability and distinguishes quote matches
  in search snippets from matches in fetched page text. It also provides a
  return link to the selected card or list row.
- A run that read no page in full, as every run did while Firecrawl was
  down, looked like any other: its stats said `0/22` in passing. The report
  now says so above both views, and a live run puts up a notice after five
  snippets in a row with a scraper set up, while it can still be cancelled.
- The page showed a model's reasoning twice: the latest of it under the
  status row ("Analyzing — thinking · 0:12"), and all of it in the thinking
  block below. The status row now keeps its one line, and the reasoning is
  in the thinking block only. The terminal, which has no such block, still
  shows it under the status.
- Follow-up answers called snippet-only sources "degraded", a word the page
  never shows: the model is now told the page's own marks.

## [0.15.1] - 2026-10-18

### Changed

- A run limited to a list of sites stopped on "None of the listed domains
  can be researched: wikipedia.org search-failed" when SearXNG was not
  running. No site was checked at all: the search was down. When the search
  fails on every site, the run now says no search answered and what to check
  (`SEARXNG_URL`, that SearXNG is running, `drsh doctor`), as a run without a
  list does.
- docs/services.md gives every long-running service in Firecrawl's stack
  `restart: unless-stopped`, as SearXNG has. Firecrawl's compose file sets
  none, so after a reboot or a Docker restart SearXNG came back and Firecrawl
  did not, and runs read search snippets only until someone noticed. The
  Podman RabbitMQ fix now edits the override's `rabbitmq` entry instead of
  appending a second one.
- The 0.15.0 entry's PDF source-list item no longer sits after a stray blank
  line, which split that list in two.

## [0.15.0] - 2026-10-17

### Changed

- The command is `drsh`, not `deep-research`, which was long to type for
  every run. The project, its config directory, its `DEEP_RESEARCH_*`
  settings, stored keys, history and release archives keep their names, so
  nothing set up before is lost. An install from before this release cannot
  update itself to it (its `update` stops with "archive must contain only
  the deep-research binary"): run the installer again, which installs `drsh`
  and removes the old `deep-research` binary beside it.
- `serve` opens its page in the default browser once it is listening:
  `$BROWSER` if set, `wslview` or Windows' browser under WSL, nothing on a
  Linux session with no display. `--no-open` turns it off.
- The page's plan review shows each sub-topic's search query and lets you
  edit it, including on sub-topics you add. The description under the name
  was what the review showed before, and research never reads it: the query
  is what a sub-agent searches. A changed query drops the planner's terms
  for the old one, as a rename already did.
- The report's Citations and Other Sources Retrieved lists no longer end
  nearly every source in "— ok". A page read in full is left unmarked, and
  the rest say `snippet only` or `never fetched`, the page audit's words,
  where they said `degraded` or `unverified`. The `.json` keeps the status
  values as they were.

### Added

- The page's live frame has a thinking row, collapsed like Activity, that
  holds the model's whole reasoning, a section per model call. The status row
  showed only its last 150 words, rewritten every five seconds, which was
  too short a window to follow an analysis by. `--jsonl` carries the
  reasoning as `thinking` events.

## [0.14.2] - 2026-10-17

### Changed

- A streamed phase (plan, analysis, fact-check, report) is no longer cut off
  while the model is still sending it. `model_call_timeout` counts the time
  with nothing arriving, and every piece of reasoning or answer restarts it.
  As a fixed deadline it killed working calls: a real run's fact-check was
  stopped at 120 seconds, five seconds after its last reasoning, and its
  retry took as long again, a quarter of the run spent twice. One attempt
  still stops at five times the timeout (10 minutes at the default), and that
  is not retried, so a model repeating itself on a server with no output
  limit cannot hold a phase for good. Unstreamed calls keep the fixed
  deadline.
- Streamed phases spend about 98% less CPU and 99% less memory on their
  progress line: it rebuilt the whole answer and the whole reasoning on every
  token, about 185 ms and 425 MB per phase on a 16 KB analysis.
- The Agent Skill asks before running without a scraper: set up Firecrawl,
  set an Exa or Tavily key, or accept snippets only. It used to mention the
  warning and go on, and the run came back with every source degraded and
  low confidence, which the user only learned from the report.
- docs/services.md binds a self-hosted Firecrawl to localhost and gives its
  stack its own Compose project name. Firecrawl publishes its unauthenticated
  API on every interface, and a second checkout named `firecrawl` adopted an
  earlier one's containers, whose Postgres then refused the new password.

## [0.14.1] - 2026-10-16

### Changed

- `init --docker`'s SearXNG turns on Bing, Google, Yahoo and Qwant beside
  the default Brave, DuckDuckGo and Google CSE. Engines rate-limit by IP
  address, so a local SearXNG is throttled like any client. In a real run
  the default three were all suspended within a dozen searches and the rest
  of the run found nothing; with more engines, Yahoo and Bing still answer
  after the default three refuse. An existing `settings.yml` is not
  overwritten (docs/usage.md shows the block to add).
- An incomplete run's error names the command that resumes it, `deep-research
  --resume <checkpoint>`, which repeats only the searches that failed. The
  checkpoint was already kept, but nothing said so.
- The docs and the Agent Skill no longer say a local SearXNG cannot be
  refused partway through a run. The skill offers a resume, after a few
  minutes, for a run cut short by rate limits.

### Fixed

- A refused search (rate-limited, challenged or blocked) ends its
  sub-agent's queries, and the follow-up round is skipped while search is
  refusing. A single SearXNG never rests, so after its engines suspended it
  each sub-agent still sent its fallback query, then the follow-up round
  sent its own, seven more queries into the same limit.
- A follow-up search no longer has a fallback query. The fallback was the
  question with the follow-up's name glued on, clipped mid-phrase (`… (language,
  toolchain, Language changes`), which asked nothing the follow-up had not.

## [0.14.0] - 2026-10-16

### Changed

- **Settings are read only from the config directory**,
  `~/.config/deep-research/` (`$DEEP_RESEARCH_CONFIG_DIR` when set),
  whatever folder a run starts in. The working directory's
  `config/config.yaml` and the nearest `.env` above it are no longer read.
  A run inside a project took that project's `config/config.yaml` as its
  own settings and its `.env`'s `OPENAI_API_KEY` over the key deep-research
  was set up with, and the same command behaved differently in every
  folder. `init` writes the `.env` template into the config directory too.
  A working-directory file that holds deep-research settings is named in a
  warning on every run and by `doctor`, so a setup that relied on one says
  what to move instead of silently changing.
- `init --docker` sets SearXNG up in the config directory and adds
  `SEARXNG_URL=http://localhost:8888` to its `.env` (unless one is set).
  It then prints the one command left, `docker compose -f
  <config dir>/docker-compose.yml up -d`. It used to write into whatever
  folder it ran in and leave the `.env` line to the reader. It is now the
  first fix every search failure names, and `doctor` warns on public
  instances even when they answer, recommending it.

- `doctor` says what to do next. A failed or weak check is followed by an
  `→` line with the command to run or the setting to change. When public
  SearXNG instances refuse, it names both fixes, `deep-research init
  --docker` with `SEARXNG_URL`, or `DEEP_RESEARCH_SEARCH_ENGINE=exa`/`tavily`
  with its key. Before, it printed the failure and stopped there. A doctor
  with no failures ends on `ready to run`.
- `doctor` warns (`!`) instead of passing when there is no scraper, since
  every claim is then checked against a snippet, and when the search
  answers a common query with no results, since the run would then find
  nothing.
- `doctor`'s budget line says "at most 7, in every --mode" with web search.
  It used to read "quick 7, standard 7, deep 7", which looked like a bug.
  Its `config` line says where `init` would write a config when there is
  none.
- A run's advice when public SearXNG refuses it now names the exact
  settings, and links `docs/services.md` instead of giving a repo path that
  binary installs do not have.

### Fixed

- A public SearXNG instance whose own engines failed (Google suspended,
  say) served its HTML page with an empty result list. That was read as a
  search that found nothing, so the instance passed `doctor` with
  "0 results" and answered a run with nothing. The page's engine-error
  table is now read like the JSON API's `unresponsive_engines`: the search
  fails, names the engines, and the next instance is tried.
- The Agent Skill asks for the question when it is invoked without one. It
  requires 0.13.0 or newer and upgrades an older binary with
  `deep-research update`. It reads doctor's `✓`/`!`/`✗` marks and `→`
  lines, stopping on a failure and passing on a warning's cost.

## [0.13.0] - 2026-10-15

### Added

- An Agent Skill, `skills/deep-research`, that teaches a coding agent to run
  deep-research: check it with `doctor`, run with `--jsonl` into a fresh
  reports directory, wait for the `done` line, answer from the report
  without adding to it, and use `ask` and `--resume`. The repo is also a
  Claude Code plugin marketplace
  (`/plugin install deep-research --marketplace juanhuttemann/deep-research`),
  and `npx skills add juanhuttemann/deep-research` installs it for Codex,
  Cursor, Gemini CLI and other agents. A test fails when the skill names a
  flag the CLI does not define.

### Changed

- The `serve` page now looks like the terminal UI instead of a generic web
  app. It is monospace throughout and uses the same ANSI palette, light or
  dark with the system. Titles are bold cyan capitals and rules are thin.
  It has the same phase strip (`✓ Plan ─ ▸ Research ─ · Analyze`) and
  braille spinner, and each sub-agent gets a ten-cell `█░` meter with its
  own source count. The follow-up round is marked as it is in the
  terminal. The prompt is a magenta `research ▸` with key hints in brackets.
  Verdicts are coloured words with their glyphs, and claim references read
  `[c1]` as they do in the report. Gone: the pill badges, rounded cards,
  shadows, stat tiles, blur and decorative icons.
- The dark theme is a charcoal rather than near-black, and boxes sit a step
  lighter than the page, so their contents no longer blend into it.

### Added

- A sun/moon toggle in the `serve` page's top bar switches between light and
  dark. The choice is kept in the browser; without one, the system setting
  decides.
- The start screen lists the whole setup (model, provider, search, page
  text). The top bar keeps only the model and search names, which no longer
  get cut off.
- The "Only these sites" box wraps and grows with the list, up to five lines,
  instead of a one-line field that showed two domains and cut off the rest.
- A report on the `serve` page offers its `.md`, `.pdf` and `.json` downloads
  in its header, including a report opened from Recent runs or a permalink.
  Those had no downloads at all, and on a run that just ended they sat below
  the whole live frame.

### Fixed

- The `serve` page's token count stopped at the analysis's total: it read
  totals only from citation events, and missed the final one the report
  event carries (a run's frame said 21k where its report said 89k).
- The run frame's title says what the run is doing (planning, researching,
  complete) instead of repeating "Deep Research" under the top bar's.

## [0.12.0] - 2026-10-14

### Added

- `--domains go.dev,pkg.go.dev` confines a run's search to your own list of
  sites instead of the open web. Exa and Tavily filter by it natively,
  SearXNG gets `site:` operators, and any result from another site is
  dropped either way, since an engine may ignore the operator.
- Before planning, so before any model request, each listed site is proved
  usable: a search scoped to it must find pages there, and the first must
  be readable through the scraper. A site that fails is named with the
  reason (unindexed, unreadable, search failed) and dropped. A list where
  none pass stops the run. The plan, the checkpoint, the trace and the
  run's record keep the list, so `--plan` and `--resume` check it again.
- `doctor --domains` runs the same check on its own.
- The web page asks where to search, the web or "Only these sites", and
  shows a box for the list only when it is used. It shows each site's check
  as the run opens, and the list in the plan review. "Sources per topic" is
  now "Pages per topic": beside a list of sites, "sources" read as the same
  setting.

### Fixed

- A SearXNG instance that answers with no results while its own engines
  failed (suspended for too many requests, a CAPTCHA) is a failed search
  that names those engines, not an empty one. `doctor` said "answered (0
  results)" while every engine of a local instance was suspended, and a
  run took each search as one that found nothing; a public pool now moves
  on to the next instance instead.

## [0.11.0] - 2026-10-13

### Changed

- `init` no longer writes `agent.yaml`. The prompts are built into the
  binary, and a copy in the config directory froze them at install time:
  later releases' prompt changes never reached it, and once the schema moved
  every run warned while `init` said "already configured". Create
  `agent.yaml` only to override a prompt, with just the blocks you change.
- The outdated-prompts warning names the `agent.yaml` it read by its full
  path and says to move it aside to use the built-in prompts; it said only
  "agent.yaml", and three directories can hold the one read.
- The PDF export is typeset instead of dumped as plain text: headings in
  bold at their own sizes, lists with their bullets and numbers and a
  hanging indent, tables as columns under a repeated header row, code on a
  grey band, and page numbers. Lines are wrapped by the measured width of
  the font; wrapping at 78 characters filled two thirds of the page, list
  items stood three blank lines apart and tables were flattened into
  "cell — cell — cell".
- PDF links are clickable, and each link's URL is printed once, at its
  last link, which is the citation list for every source the report cites;
  printing it at every link buried the paragraphs and table cells in URLs.
- The fact-check marks print in the PDF (✗ as ×, ⚠ as !) instead of as
  `?`, and no longer set off the lost-characters warning on every run that
  left a claim unconfirmed. Arrows and comparison signs, which reports use
  often and WinAnsi has no glyph for, are spelled `->`, `<=`, `>=`, `!=`
  and `~` instead of `?`.

## [0.10.0] - 2026-10-13

### Changed

- A SearXNG instance that fails a query rests before it is asked again (ten
  minutes after a challenge or block, two after a limit or timeout), so once
  the public instances start refusing, a search fails at once instead of
  spending 30 seconds or more walking all ten again. A single configured
  instance never rests.
- A search every instance failed is reported as counts by kind
  (`9 challenged, 1 timed out`) instead of every instance's error, challenge
  scripts included, in one line.
- When the public instances refuse the run, it says once — and again in the
  run's error if no search answered — to run SearXNG locally or use Exa or
  Tavily.
- Once every instance is resting, a sub-agent stops sending queries and the
  follow-up round is skipped, instead of listing failures that asked nothing.
- A run where more searches gave nothing than gave something — refused, or
  answered with only off-topic results — is delivered marked incomplete
  (exit status non-zero, `done` status `incomplete`), its report opening with
  how many searches gave nothing and how many sources it rests on, at `low`
  confidence. With fewer than three sources it stops before the analysis,
  saying why, rather than spending five model calls on them. One that
  survived on a single off-topic snippet used to read like any finished
  report.
- A SearXNG instance whose answer was all off-topic rests like one that
  refused; one was seen answering every query with results for its first
  word alone. Each search's line names the instance that answered.
- Off-topic results are one line per search (`9 of 10 results off-topic:
  ruby-lang.org, …`) instead of two lines per result.
- A sub-agent's fallback query is the planner's terms for it, not the
  question clipped with the sub-topic name cut off after it.

## [0.9.0] - 2026-10-13

### Added

- Exa and Tavily as search engines (`search_engine: exa|tavily`, with
  `EXA_API_KEY` / `TAVILY_API_KEY`). Each search returns the result pages'
  text as well, so a query is one request and needs no scraper; a result that
  brought its text is never scraped. Either can also be the scraper behind
  SearXNG (`scraper: exa|tavily`). Nothing changes unless one is chosen: a key
  in the environment alone sends no query anywhere.
- A rejected key or exhausted credit on a hosted engine is reported as
  `unauthorized` or `quota`, and `doctor` checks the configured engine and
  scraper.
- `serve` shows the model and provider, and the search engine and page
  source, in the page's top bar.

## [0.8.0] - 2026-10-13

### Added

- `deep-research login` and `deep-research model` open one searchable list of
  every model of every provider, under a heading per provider that says
  whether its key is stored, from the environment or still needed. Picking a model whose provider has
  no key asks for it behind a mask of asterisks; one free request checks the
  pair before the key is stored in `auth.json` (0600, replaced atomically) and
  the choice in `model.json`. A model the catalog lacks can be typed in.
  `deep-research logout` removes a key. The list is Catwalk's catalog
  (built in, refreshed when online), plus the Claude subscription and a custom
  OpenAI-compatible endpoint. `login --provider --model --key-stdin` scripts
  it. A stored key can be `!command`, to keep it in a password manager.

### Changed

- A key stored by `login` wins over `OPENAI_API_KEY` / `ANTHROPIC_API_KEY`,
  which are now the fallback. Provider and model settings in the environment
  still override the stored choice. Every key goes only to its own endpoint:
  a stored key to the one it was entered for, an environment key to the one
  the environment names, so `OPENAI_API_KEY` for OpenRouter never reaches
  another provider picked in `login`.
- `doctor` says where the key came from, and a rejected stored key points at
  `deep-research login --new-key`.

## [0.7.0] - 2026-10-12

### Added

- `provider: claude` runs on Claude. With Claude Code signed in to a Claude
  subscription, each model call runs `claude -p`, with no tools and none of
  your CLAUDE.md, hooks or MCP servers. Otherwise it uses
  `ANTHROPIC_API_KEY` through the Messages API, which a run also falls back
  to when the subscription reaches its usage limit. `ask` and the page's
  follow-up questions need the key. The model is `claude_model`, by default
  `claude-opus-5-5`. `doctor` says which way a run would go. A `provider`
  other than `openai` or `claude` is an error.

### Changed

- A model that declines to answer fails the phase as "declined", instead of
  passing on an empty answer.
- A run watched in the terminal no longer exits non-zero when questions about
  its report cannot start; it says why and ends.

## [0.6.0] - 2026-10-04

### Added

- `ask <run>.json` answers follow-up questions about a finished run from what
  it saved: the report, the claims with their fact-check status, and the
  stored page texts, which the model reads through tools. Nothing is
  searched again. A run watched in the terminal takes questions once its
  report is printed. The answers are not fact-checked.
- `serve` asks the same follow-up questions below a finished run's report,
  and copies the report's Markdown with Copy report.
- `update` installs the latest release over the running binary, verified
  against the release's checksums and by running its `--version` before the
  swap. `update --check` only reports whether a newer release exists.

## [0.5.0] - 2026-10-03

### Added

- `serve` opens a finished run on a Reading view: the report's text without
  its inline citations and claim IDs. The fact-check audit moves to an Audit
  view beside it. A report that is empty, or only citations, shows the audit
  alone.

### Fixed

- The scraper refused `127.0.0.1` but let `127.1`, `10.1`, `192.168.1` and
  octal `0177.0.0.1` through, and Firecrawl's URL parser reads each as a
  private address. A host whose last label is a number is now refused.
- On a pipe the run printed only the completion card, not the one log line
  per event the docs describe: the renderer's log mode was never wired.
- An analysis that decoded as JSON with no `answer` key used the raw JSON as
  the answer, which could reach the summary and the report.
- Parallel sub-agents' findings were collected in completion order, so the
  same evidence reached the analysis prompt in a different order each run.
  They are now in plan order.
- A source with no URL was always marked cited, which kept a report that
  cites nothing from listing its sources under one heading.
- The escape-key timing on a terminal assumed 64-bit `select` bitmasks,
  which are 32-bit on macOS and 32-bit unix.
- `tools.ExcerptFor` panicked on a negative limit.
- Building the fact-check prompt for a wide run (fifty 40 KB pages,
  thirty-six claims) took about 13 seconds of CPU before the model call
  started, with the phase line up and Esc unable to interrupt it: every claim
  re-folded every block of every page, and each passage re-scanned the page
  for its section heading. It now takes about half a second.
- Checking that a fact-check quote is in its page normalised the whole page
  again for every quote, about 80 ms each on a scraped page. Each page is now
  normalised once per pass.
- With `auto` search, concurrent queries ran the searx.space discovery one
  after another under a lock no deadline reached, one attempt each when it
  failed. They now share one attempt, and a query waiting on it keeps its own
  deadline.
- A run's scraper was given the model-call timeout, so a Firecrawl that
  never answered held each scrape two minutes, or longer with a raised
  `model_call_timeout`. Firecrawl is now given 60 seconds per page and the
  request gives up at 70.
- `planner_instructions` in `agent.yaml` could not change what the planner
  was told: the built-in planner text was sent again as the user message on
  every call.
- A run killed from outside (`kill`, `timeout`, a closed terminal window)
  left the terminal raw, with no echo and no cursor. The terminal is now
  restored before the signal ends the process. The key reader also ends with
  the run instead of waiting on stdin.

## [0.4.0] - 2026-10-01

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
- An interrupted run can be resumed. From its confirmed plan on, every
  run saves a checkpoint after each step, `<reports>/<name>.<id>.partial.json`,
  removed only when it completes in full. `--resume` runs the saved plan
  again, serves the searches that finished from the checkpoint and
  searches live only what did not finish or failed; the model phases run
  again. `serve` lists interrupted runs with Resume and Discard, and a tab
  left open across a server restart offers to resume the run it showed.
  A killed run used to lose every search it had made.
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

[Unreleased]: https://github.com/juanhuttemann/deep-research/compare/v0.25.0...HEAD
[0.25.0]: https://github.com/juanhuttemann/deep-research/compare/v0.24.0...v0.25.0
[0.24.0]: https://github.com/juanhuttemann/deep-research/compare/v0.23.0...v0.24.0
[0.23.0]: https://github.com/juanhuttemann/deep-research/compare/v0.22.0...v0.23.0
[0.22.0]: https://github.com/juanhuttemann/deep-research/compare/v0.21.1...v0.22.0
[0.21.1]: https://github.com/juanhuttemann/deep-research/compare/v0.21.0...v0.21.1
[0.21.0]: https://github.com/juanhuttemann/deep-research/compare/v0.20.0...v0.21.0
[0.20.0]: https://github.com/juanhuttemann/deep-research/compare/v0.19.0...v0.20.0
[0.19.0]: https://github.com/juanhuttemann/deep-research/compare/v0.18.2...v0.19.0
[0.18.2]: https://github.com/juanhuttemann/deep-research/compare/v0.18.1...v0.18.2
[0.18.1]: https://github.com/juanhuttemann/deep-research/compare/v0.18.0...v0.18.1
[0.18.0]: https://github.com/juanhuttemann/deep-research/compare/v0.17.0...v0.18.0
[0.17.0]: https://github.com/juanhuttemann/deep-research/compare/v0.16.0...v0.17.0
[0.16.0]: https://github.com/juanhuttemann/deep-research/compare/v0.15.1...v0.16.0
[0.15.1]: https://github.com/juanhuttemann/deep-research/compare/v0.15.0...v0.15.1
[0.15.0]: https://github.com/juanhuttemann/deep-research/compare/v0.14.2...v0.15.0
[0.14.2]: https://github.com/juanhuttemann/deep-research/compare/v0.14.1...v0.14.2
[0.14.1]: https://github.com/juanhuttemann/deep-research/compare/v0.14.0...v0.14.1
[0.14.0]: https://github.com/juanhuttemann/deep-research/compare/v0.13.0...v0.14.0
[0.13.0]: https://github.com/juanhuttemann/deep-research/compare/v0.12.0...v0.13.0
[0.12.0]: https://github.com/juanhuttemann/deep-research/compare/v0.11.0...v0.12.0
[0.11.0]: https://github.com/juanhuttemann/deep-research/compare/v0.10.0...v0.11.0
[0.10.0]: https://github.com/juanhuttemann/deep-research/compare/v0.9.0...v0.10.0
[0.9.0]: https://github.com/juanhuttemann/deep-research/compare/v0.8.0...v0.9.0
[0.8.0]: https://github.com/juanhuttemann/deep-research/compare/v0.7.0...v0.8.0
[0.7.0]: https://github.com/juanhuttemann/deep-research/compare/v0.6.0...v0.7.0
[0.6.0]: https://github.com/juanhuttemann/deep-research/compare/v0.5.0...v0.6.0
[0.5.0]: https://github.com/juanhuttemann/deep-research/compare/v0.4.0...v0.5.0
[0.4.0]: https://github.com/juanhuttemann/deep-research/compare/v0.3.0...v0.4.0
[0.3.0]: https://github.com/juanhuttemann/deep-research/compare/v0.2.0...v0.3.0
[0.2.0]: https://github.com/juanhuttemann/deep-research/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/juanhuttemann/deep-research/releases/tag/v0.1.0
