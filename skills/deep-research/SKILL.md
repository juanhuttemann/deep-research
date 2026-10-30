---
name: deep-research
description: Researches a question across the web with the deep-research CLI and returns a fact-checked, cited report (Markdown, plus JSON with every claim, its verdict and the page text it was checked against). Use when the user asks to research, investigate or survey a topic, compare options with sources, or get a sourced answer that needs many web pages; also to answer follow-up questions about an earlier deep-research run.
license: MIT
compatibility: Requires the drsh binary on PATH, a model configured with drsh setup, and network access.
---

# deep-research

`drsh` plans sub-topics, searches them in parallel, analyzes the
evidence, fact-checks every claim against the fetched page text, and writes a
report. One run takes minutes and spends model requests; run it once per
question, not as a quick lookup.

## 0. The question

If the skill was invoked without a question, run the preflight below and
then ask for one, along with any constraints that change the answer (dates,
region, versions, budget). Do not invent a question to test the setup; a run
spends the user's model requests.

## 1. Preflight

```bash
drsh --version
```

If it is not installed, tell the user and offer the installer:

- Linux, macOS, WSL2: `curl -fsSL https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.sh | bash`
- Windows: `iex (irm https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.ps1)`

If `drsh` is missing but `deep-research --version` answers, it is an install
from before the command was renamed `drsh`. Its `update` cannot install the
renamed release; offer the installer above, which replaces it. Config, keys
and past runs are kept.

This skill needs version 0.13.0 or newer: older releases lack flags it uses
(`--domains`) and fail with "unknown flag". If the version is older, run
`drsh update` (it verifies the release checksum and replaces the
binary in place; no key or config needed), then check the version again.
`update` refuses a binary built from source (`dev` or a `git describe`
version); say so and leave that build alone.

Then check the model, key, search and scraper (no model tokens spent; the
search check can take up to a minute):

```bash
drsh doctor
```

Each line starts with `✓` (fine), `!` (works, at lower quality) or `✗`
(broken). A `→` line under a `!` or `✗` gives the next step: the command to
run or the setting to change.

- **Exit status non-zero (a `✗`)**: show the user each `✗` line and the `→`
  line under it, and stop. Do not start a run.
- **Exit status zero with `!` lines**: the run will work, but say what each
  warning costs and pass on its `→` line, then go on. The scraper is the
  exception below.
- **No scraper** (the `scraper` line says "none"): do not start the run yet.
  Without one, every claim is checked against a search snippet, not the
  page, every source is marked degraded, and the report's confidence drops.
  Ask the user to choose, as a multiple-choice question when the client
  offers one:
  1. Self-hosted Firecrawl: free, no key, about 6 GB of Docker images. Offer
     to set it up following
     [docs/services.md](https://github.com/juanhuttemann/deep-research/blob/main/docs/services.md),
     and ask before running the commands.
  2. Exa or Tavily: the user runs `drsh setup scraper` themselves and picks
     one; it asks for the key.
  3. Run with snippets only, accepting lower confidence.

  Start only after they choose. With no one to ask (a non-interactive
  session), go on and say in the answer that no scraper was used.
- A missing or rejected model key is fixed by the user running
  `drsh setup`, which is interactive: do not run it for them, and
  never ask them to paste a key into the conversation. The same goes for any
  `→` step that needs an API key (Exa, Tavily, hosted Firecrawl): the user
  enters it in `drsh setup search` or `drsh setup scraper` themselves.
- **Search through public instances** (the `search` line says "a public
  instance"): recommend the user's own SearXNG in Docker. It is free, keeps
  queries on their machine, and queries more engines than public instances
  usually allow. Search engines still rate-limit by IP address, so a long
  run can be throttled even locally (see section 6). Setup is
  `drsh init --docker`, which writes the files into the config
  directory and points the config at them, then the
  `docker compose -f ... up -d` command it prints. Offer to run both; they
  change only deep-research's config directory and start a container, so
  ask first.
- Settings are read only from the config directory (the `config` line names
  it), never from the folder a run starts in. An `! config` line names a
  file in the working directory whose deep-research settings are no longer
  read: tell the user to move them, and do not edit or move a file that
  may hold their keys yourself.

## 2. Run

Always use `--jsonl` and a fresh reports directory. Artifact names come from
the question, so the same question into a shared directory overwrites the
earlier run.

```bash
dir=$(mktemp -d)
drsh -p "QUESTION" --jsonl --reports "$dir" > "$dir/events.jsonl"
```

PowerShell: redirect through `cmd`. Windows PowerShell's own `>` re-encodes
a program's output (UTF-16 or a byte-order mark), which leaves the first
line unparseable and mangles non-ASCII text. The same goes for any other
output saved to a file, such as `--plan-only`'s.

```powershell
$dir = (New-Item -ItemType Directory (Join-Path $env:TEMP "dr-$(Get-Random)")).FullName
cmd /c "drsh -p ""QUESTION"" --jsonl --reports ""$dir"" > ""$dir\events.jsonl"""
```

- Start it in the background and check on it, rather than blocking a
  foreground shell for minutes.
- Write the question the way the user means it, with its constraints (dates,
  region, versions, budget). The planner works from it alone.
- `--mode quick|standard|deep` sets the budget (default `standard`). Use
  `quick` for a narrow factual question, `deep` only when the user asks for
  thoroughness; it spends more requests.
- The planner chooses how many sub-topics the question needs, up to the
  tier's count. `--topics N` asks for exactly N (1-8) when the user names
  the facets to cover.
- `--no-follow-ups` skips the follow-up searches after the planned ones:
  one model request fewer, for a question the plan already covers.
- `--format brief|detailed|table` shapes the report (default `standard`):
  `brief` for a quick answer (about 300 words), `table` when the user wants
  a comparison laid out, `detailed` when they ask for depth. `--words N`
  and `--tables none|some|many` adjust it. The answer, fact-check and
  citations are in every format.
- `--domains go.dev,pkg.go.dev` confines the search to those sites. Each
  site is checked first; one that fails is dropped with a reason (a
  `source` event). `drsh doctor --domains LIST` checks a list
  without starting a run.
- `--jsonl` cannot be combined with `--silent`.

To let the user review sub-topics before searching, make the plan first:

```bash
drsh -p "QUESTION" --plan-only > plan.json   # one model request
# edit sub_topics; clear query and terms of a renamed one
drsh --plan plan.json --jsonl --reports "$dir" > "$dir/events.jsonl"
```

`--plan` carries its own question, tier, follow-up setting
(`no_follow_ups`), report shape (`report`) and clarifications, so it takes
no `-p`, `--mode`, `--sources`, `--topics`, `--no-follow-ups`, `--format`,
`--words`, `--tables`, `--clarify`, `--clarify-only` or `--answers`.

A `--jsonl` run asks no clarifying questions. When the question is
ambiguous (which market, product, period or audience) and the user can
answer, ask first, one model request:

```bash
drsh -p "QUESTION" --clarify-only > questions.json
```

Put its `questions` to the user, then pass their answers back in the same
JSON with `"answers": [{"question": "...", "answer": "..."}]` filled in:
`drsh -p "QUESTION" --answers "$(cat questions.json)" --jsonl ...`. An empty
`questions` list means the question is clear: run it as it is.

## 3. Wait for the `done` line

Read only the last line of `events.jsonl`. Progress arrives as `info`,
`phase`, `subagent`, `search` and `read` lines. Search and scraping have no
heartbeat, and an unstreamed search call can be silent until its timeout,
so silence alone does not mean the run is stuck.

Every run ends on exactly one line with `"type":"done"`:

| `status` | Meaning | What to do |
| --- | --- | --- |
| `complete` | report written | read it |
| `incomplete` | a late phase failed, or most searches found nothing; a report was saved | read it, and tell the user it is incomplete and why (`detail`) |
| `failed` | the run errored (`detail` says why) | report `detail`; resume if a checkpoint exists |
| `cancelled` | interrupted | resume if a checkpoint exists |

`artifacts` lists the `.md`, `.pdf` and `.json` written. The exit status is
non-zero for `incomplete` and `failed`. A stream that ends without a `done`
line means the process was killed.

Event fields: [references/jsonl-events.md](references/jsonl-events.md).

## 4. Report back

Answer from the `.md` artifact. Keep its citations; it already separates
sources the report cites from those it only retrieved.

- Say the confidence the report gives.
- Say which claims the fact-check did not confirm. Do not present an
  unconfirmed or blocked statement as fact.
- Do not add facts the run did not establish. If the user needs something
  the report does not cover, say so and offer a follow-up question or a new
  run.
- Give the user the artifact paths.

For anything the Markdown summarizes (per-claim verdicts, quotes, which
sources were fetched in full), read the `.json` artifact:
[references/result-json.md](references/result-json.md). Its `pages` field
holds the full text of every fetched page and can be large; search it
rather than reading it whole.

## 5. Follow-up questions

When the answer may already be in the pages the run fetched, ask the run
instead of starting a new one. Nothing is searched again:

```bash
drsh ask "$dir/<name>.json" -p "Which source gives the price?"
```

Answers from `ask` are not fact-checked. Pass that on. A question the run's
pages do not answer needs a new run. `ask` needs a model that supports tool
calls; if the first question fails saying so, tell the user rather than
switching their model.

## 6. Recovering a run

A run stopped after its plan was confirmed leaves a checkpoint beside its
reports. Resume it; finished searches are not repeated:

```bash
drsh --resume "$dir/<name>.<id>.partial.json" --jsonl --reports "$dir" > "$dir/events-resume.jsonl"
```

An `incomplete` run whose `detail` names failed searches (`rate-limited`,
`challenge`) also has a checkpoint. Search engines suspend a client for a
few minutes, so a resume right away meets the same limits. Tell the user,
and offer to resume after about ten minutes: only the failed searches run
again, and the model phases run once more (`doctor`'s budget line gives
their count).

`--resume` takes no `-p`, `--plan`, `--mode`, `--sources` or `--domains`;
the checkpoint carries them. Look for `*.partial.json` in the reports
directory after a `failed` or `cancelled` run. A run killed before its plan
was confirmed has no checkpoint: run it again.

## Other commands

- `drsh list`: past runs, their findings and tokens.
- `drsh serve`: a local page to launch runs and audit claims, for a
  user who wants to look at the evidence themselves.
- `--replay TRACE` re-runs only the model phases on a run recorded with
  `--trace`. It is for comparing models or prompts on the same evidence, not
  for answering a question.
