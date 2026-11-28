# Surfaces: the terminal and the serve page

drsh has two live views of a run: the terminal UI (`internal/ui`, the
`Renderer`) and the page `drsh serve` serves (`internal/web/index.html`).
They see the same events: the page receives the `--jsonl` stream the
renderer is driven by. So anything the terminal shows of a run, the page can
show too, and it does.

The two used to drift apart. The terminal gained topic groups, colours and
phase views, and the page went on showing a flat log, because nothing told
whoever changed one surface to change the other. This file is that rule,
and the table below is what the tests hold the two surfaces to.

## The rule

A change to what one surface shows of a run changes the other in the same
commit, and updates this table when it adds, renames or removes a feature.
This covers the live frame, the labels and colours, and the report's
heading. When the change cannot be the same on both, add it under
[Deliberate differences](#deliberate-differences) with the reason.

Run settings follow their own rule, which is older: one entry in
`ui.Settings`, drawn by every surface (see `AGENTS.md`).

## Features

`ID` is the feature's name in three places:
- this table;
- a `data-feature` attribute on the page element that holds the feature;
- a `[id]` tag in a test name in `internal/web/browser/parity.spec.js`.

`Terminal` names the Go functions (`Type.Method` or plain functions in
`internal/ui`) that draw the feature in the terminal. `Page` names the
JavaScript functions in `index.html` that draw it on the page.

| ID | What it shows | Terminal | Page |
|---|---|---|---|
| `topic-groups` | The activity tail files each topic's rows under one header. The header sits where the topic's first row fell and shows the latest row's time, the topic's number, ▹/▿, its name, its row count and, while shut, its latest row. A click or the topic's digit (1–9) opens it to its rows, with "… N earlier" when there are more. The run's own rows stand alone. | `Renderer.groupActs`, `Renderer.topicHead`, `Renderer.topicRows`, `Renderer.OpenTopic`, `Renderer.Click`, `frameKey` | `groupActs`, `topicGroup`, `toggleTopic` |
| `topic-colours` | Each topic has a colour by its place in the tree, the same in the tree, the tail and the plan and research views. | `agentHues`, `Renderer.hue` | `hue` (CSS `--h0`…`--h5`) |
| `topic-labels` | A topic is labelled by its place in the tree ("#1" on the page, the digit key in the terminal). Follow-up topics come after a divider. | `Renderer.topicNumber`, `isFollowUp` | `topicLabel`, `node` |
| `activity-toggle` | The activity tail starts hidden; `a` shows or hides it. | `Renderer.ToggleActivity` | `#activity` (details), key handler |
| `phase-nav` | A phase the run has reached is clickable on the phase strip. The click opens that phase's view in place of the tree and tail, and clicking it again closes the view. The open phase is underlined. Esc closes a view before it does anything else. | `Renderer.phaseStrip`, `Renderer.clickStripLocked`, `Renderer.ClosePhase` | `stepper`, `openPhase` |
| `phase-views` | What a phase did, under a title that says when it started and how long it took ("· running" while it runs). Plan shows the reading, the settings, the sites and each sub-topic in its colour with its notes, search and terms. Research shows every topic with its state, sources and rows. Every other phase shows its own rows. All of them end with the phase's reasoning, each call under its part's name. A view of the running phase follows new rows. | `Renderer.viewTitle`, `Renderer.phaseEnd`, `Renderer.viewContent`, `Renderer.planView`, `Renderer.researchView`, `Renderer.phaseRows`, `Renderer.reasoningView` | `drawView`, `nextPhaseAt`, `planView`, `researchView`, `phaseRows`, `reasoningView` |
| `read-rows` | A page's read is one row, named by host and path. It spins while the page is fetched, then shows a green, yellow or red dot (read in full, snippet only, dropped). Read and snippet rows link to the page. A page fetched past the topic's budget settles as a text row with no dot or link, naming its fetch's outcome and "not used: budget full" (`unused`). Reads still in flight when the topic's next search starts, or when the topic finishes, are removed. | `Renderer.pushRead`, `Renderer.settleRead`, `Renderer.dropReads`, `Renderer.activityText`, `Renderer.dot` | `pushRead`, `settleRead`, `dropReads`, `actText` |
| `row-styles` | Rows are told apart by kind: a phase starting is a cyan ▸ and bold text; an error a red "!"; a search a dim "search" label and its query; the run's notes, a filter's counts and "= already cited" rows are dim. | `Renderer.styleRow` | `actText` |
| `tree-lines` | A topic's tree row shows what it is doing now: its latest search, read or verify line, or the detail of its error. | `Renderer.applySourceEvent`, `Renderer.apply` | `sourceEvent`, `node` |
| `source-split` | The source total says how many came from the follow-up round: "N sources (X planned + Y follow-up)". | `Renderer.footerLines`, `Renderer.followUpTotal` | `footer` |
| `status-row` | The status row (the model being asked, its first line of reasoning) shows only outside the research, whose tree already moves. | `Renderer.statusShown` | `status` |
| `counts` | Counts read as 12.3k and 1.2M; times as m:ss, and h:mm:ss past an hour. | `formatCount`, `formatDuration` | `fmtNum`, `clock` |
| `report-title` | A run is headed by the planner's short title, with the question under it in smaller type. A run with no title (instant, older runs, a planner that gave none) is headed by its question alone. | `ReportTitle`, `writeTitle`, `Renderer.reportLines` | `showTitle` |

## Deliberate differences

- **Keys and clicks.** The terminal opens a topic with its digit and a phase
  with a click on the strip. The page takes both (digits and `a` are ignored
  while typing in a field). The terminal's scroll keys are the page's
  scrollbars.
- **How much is kept.** Both keep the run's last 5,000 activity rows. An open
  topic on the page lists up to 200 of them, where the terminal shows as many
  as fit on screen.
- **Thinking.** The page also has a whole-run "thinking" block with every
  call's reasoning, which the terminal shows only inside phase views. A phase
  view on the page hides that block, because the view shows the same text.
- **Web-only notices.** The site check before planning, the "snippets only"
  warning when the scraper seems down, and the notices for a reconnect, a
  server restart or lost early events have no terminal counterpart. The
  terminal is the process running the run, so it has none of those failures.
- **Claims in the audit.** The page's audit names claims "Claim #1",
  conclusions "Conclusion #1" and recommendations "Recommendation #1". The
  terminal has no audit view: its report names claims in words (`names.go`).
- **Citations.** Neither surface gives a citation a row of its own. The
  read's green dot already says the page was kept.

## Tests

- `TestSurfacesParity` (`internal/ui/surfaces_test.go`) reads this table. It
  fails when:
  - a feature's ID has no `data-feature` marker on the page;
  - the page has a marker the table does not list;
  - a feature has no tagged test in `parity.spec.js`;
  - a Go function the table names no longer exists in `internal/ui`.

  It checks structure only: a marker can sit on dead code. What a feature
  does is checked by the browser test.
- `make web` runs `internal/web/browser/parity.spec.js` in Chromium,
  hermetically. The page's requests are answered from
  `testdata/run.jsonl`, a recorded run's events. `make verify` runs it, and
  skips it when Node or Playwright's Chromium is missing. CI's "Browser
  tests" job always runs it.
- The terminal's side is tested in `internal/ui` (`tail_test.go`,
  `phaseview_test.go`, `render_test.go`, `screen_test.go`).
