# deep-research

<div align="center">

[![Go version](https://img.shields.io/github/go-mod/go-version/juanhuttemann/deep-research?style=for-the-badge&logo=go&logoColor=white)](https://github.com/juanhuttemann/deep-research/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/juanhuttemann/deep-research/ci.yml?style=for-the-badge&logo=githubactions&logoColor=white)](https://github.com/juanhuttemann/deep-research/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanhuttemann/deep-research?style=for-the-badge&logo=github)](https://github.com/juanhuttemann/deep-research/releases)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-enabled-3FB950?style=for-the-badge&logo=go&logoColor=white&labelColor=1B2127)](https://github.com/juanhuttemann/deep-research/actions/workflows/ci.yml)
[![MIT license](https://img.shields.io/badge/License-MIT-3FB950?style=for-the-badge&labelColor=1B2127)](https://github.com/juanhuttemann/deep-research/blob/main/LICENSE)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-E8EDF2?style=for-the-badge&labelColor=1B2127)

</div>

An AI-powered deep research agent for the terminal and the browser. It plans
a question into sub-topics, researches them in parallel on the web, then runs
**research → analyze → fact-check → summarize** and writes a cited report as
Markdown, PDF and JSON. `drsh serve` opens the same run on a local
web page, with the plan to edit, the live progress, and an audit of every
claim the fact-check passed or blocked.

![deep-research planning, researching, fact-checking and reporting on a question about mixture-of-experts LLMs](demo.gif)

By default it costs nothing: OpenRouter's free models and public SearXNG
search, so a free key is all it needs. Every source says how it was obtained,
and nothing the model invents is passed off as a retrieved page.

## Quick install

Linux, macOS, WSL2, Termux (installs to `~/.local/bin`):

```sh
curl -fsSL https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.sh | bash
```

Windows (native, PowerShell):

```powershell
iex (irm https://raw.githubusercontent.com/juanhuttemann/deep-research/main/scripts/install.ps1)
```

Both fetch the latest release binary and verify its checksum; see
[releases](https://github.com/juanhuttemann/deep-research/releases). After
that, `drsh update` replaces it with each new release.

From source (Go 1.26+):

```sh
git clone https://github.com/juanhuttemann/deep-research.git
cd deep-research
make build
```

The build stamps the version into a `drsh` binary in the repo root.
Move it onto your `PATH` (or run it as `./drsh`) for the examples
below.

The live terminal UI is unix-only. Windows binaries work, but the run
degrades to headless output — one log line per event, then the report.

## Quick start

```bash
drsh setup
drsh -p "What are the latest advances in fusion energy?"
```

`drsh setup` asks three things, explains each choice, and saves nothing until
you confirm:

| Step | Choices |
| ---- | ------- |
| Model | any provider's model; a free OpenRouter key (no card) is at <https://openrouter.ai/keys> |
| Web search | public SearXNG (the default), a local SearXNG in Docker, your own SearXNG, Exa, Tavily |
| Page extraction | none (search snippets only), Firecrawl, Exa, Tavily |

To change one part later:

```bash
drsh setup model      # only the model
drsh setup search     # only the web search
drsh setup scraper    # only page extraction
```

Something off? `drsh doctor` checks the model, search and scraper with one
cheap request each.

## What a run looks like

In the terminal:

1. **Research brief**: the planned sub-topics. Add, rename, delete or
   re-budget them, then press Enter.
2. **Live frame**: the pipeline stage, one row per parallel sub-agent, the
   sources as they are read, and a status line for the model call in flight,
   with a reasoning model's latest thinking under it.
3. **Report**: rendered in the terminal and saved to `reports/` as `.md`,
   `.pdf` and `.json`.

The report opens with the answer (for a choice: which option, under which
conditions), then:

- the comparison and reasoning, each claim linked to its source;
- the limits that could change the answer, and searches that could settle them;
- the fact-check: how many claims were confirmed, and each one that was not;
- the sources, each one not read in full marked `snippet only` or
  `never fetched`;
- the model that wrote it, and which models a router actually served.

A run that fails late still saves what it gathered, marked incomplete. One
whose searches mostly came back empty says so up front, with confidence
`low`, and with fewer than three sources it stops before the analysis.

### In the browser

`drsh serve` runs the same pipeline from a page on localhost, in a light or a
dark theme. Type the question, pick the depth, then edit the plan before
anything is searched: rename, add or remove sub-topics, and change the search
each one runs:

![The serve page's plan review for "How close are we to a room-temperature superconductor after LK-99?": four sub-topics, each with the search it will run](docs/images/web-plan.jpg)

The live frame shows each sub-agent with its sources, the follow-up searches,
and the model's reasoning as it streams:

![The live frame mid-analysis: Plan and Research done, seven sub-agents complete including three follow-ups, and the model's reasoning streaming above them](docs/images/web-live.jpg)

When the run ends, the report opens below it, with the `.md`, `.pdf` and
`.json` downloads in the header (here in the light theme):

![A finished run in the light theme for "Have the Voyager probes really left the solar system?": every phase done, md, pdf and json downloads in the header, and the answer opening on the two readings of "left the solar system"](docs/images/web-report.jpg)

The audit shows what the fact-check decided: the statements that passed or
were blocked, every claim's verdict, and the quote and page each rests on:

![The audit view: statements passed and blocked, and the inspector showing a claim about LK-99's Cu2S impurity rated partial, with the checker's reason and the quote it located](docs/images/web-audit.jpg)

## Search

Choose with `drsh setup search`:

| Search | Cost | Notes |
| ------ | ---- | ----- |
| Public SearXNG (default) | free | nothing to install; third parties see the queries, and often refuse a run partway |
| Local SearXNG in Docker | free | **recommended**: private and more reliable; setup writes the files, you start it with one `docker compose` command |
| Your own SearXNG | free | one address or several |
| Exa, Tavily | per search | hosted; each result comes with its page text |
| No search | free | the model answers from memory, and every source is marked unverified |

Page extraction (`drsh setup scraper`) reads each result's full page, so a
claim is checked against the page and not just its search snippet: Firecrawl
(self-hosted and free, or hosted), Exa or Tavily. Self-hosting Firecrawl:
[docs/services.md](docs/services.md).

A refused search (rate limit, bot challenge, outage) is reported with its
reason, never replaced by findings the model makes up. A run cut short by a
limit can be finished later with `--resume`, which repeats only the searches
that failed.

## Models

`drsh setup model` lists OpenRouter, OpenAI, Anthropic and some thirty
compatible providers (from [Catwalk](https://github.com/charmbracelet/catwalk)'s
catalog), your Claude subscription through Claude Code, and a custom endpoint
for a local vLLM, llama.cpp or LM Studio server.

The default, `openrouter/free`, costs nothing and picks a free model per
request, so every report records what served it. Free models are capped per
minute and per day; a run with web search takes 4 to 7 model requests, and
`drsh doctor` shows how many free requests an OpenRouter key has left today. On Claude: [docs/configuration.md](docs/configuration.md#claude).

## Common commands

```bash
drsh -p "question" --mode deep          # quick | standard | deep
drsh -p "question" --output report.md   # also save to a file
drsh -p "question" --silent             # just the report
drsh -p "question" --jsonl              # machine-readable events
drsh serve                               # launch and audit runs in the browser
drsh --resume reports/<run>.partial.json # continue an interrupted run
drsh list                                # past runs
drsh setup                               # set up the model, search and page extraction
drsh setup search                        # change only the search (or: model, scraper)
drsh doctor                              # check model, key, search
drsh update                              # install the latest release
```

Full flag reference, key bindings and export details: [docs/usage.md](docs/usage.md).

## Use it from an agent

The repo ships an [Agent Skill](skills/deep-research/SKILL.md) that teaches a
coding agent to run `drsh`, wait for the result and report the
fact-checked answer with its sources. The binary must be installed first.

Claude Code:

```
/plugin install deep-research --marketplace juanhuttemann/deep-research
```

(Before Claude Code 2.1.275: `/plugin marketplace add juanhuttemann/deep-research`,
then `/plugin install deep-research@deep-research`.)

Codex, Cursor, Gemini CLI, OpenCode and other agents that read Agent
Skills (and Claude Code too):

```sh
npx skills add juanhuttemann/deep-research
```

Or copy `skills/deep-research/` into the agent's skills directory.

## Configuration

`drsh setup` saves everything in `~/.config/deep-research/`
(`$DEEP_RESEARCH_CONFIG_DIR` when set): keys in `auth.json`, readable only by
you, the model in `model.json`, the rest in `config.yaml`.

Environment variables override what setup saved, for CI and one-off runs:

```bash
OPENAI_MODEL=deepseek/deepseek-v4.1-flash drsh -p "question"
```

The variables, timeouts, retries, parallelism and the trust boundaries for
scraping and public search are in [docs/configuration.md](docs/configuration.md).

## Development

```bash
make verify   # fmt, vet, lint, deadcode, go test -race — the gate
make tools    # installs the lint binaries
```

Conventions, test style and the rules a change has to respect are in
[CONTRIBUTING.md](CONTRIBUTING.md) and [docs/architecture.md](docs/architecture.md);
cutting a release is [RELEASING.md](RELEASING.md). Notable changes are
recorded in [CHANGELOG.md](CHANGELOG.md).

## License

MIT — see [LICENSE](LICENSE).
