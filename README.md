# deep-research

<div align="center">

[![Go version](https://img.shields.io/github/go-mod/go-version/juanhuttemann/deep-research?style=for-the-badge&logo=go&logoColor=white)](https://github.com/juanhuttemann/deep-research/blob/main/go.mod)
[![CI](https://img.shields.io/github/actions/workflow/status/juanhuttemann/deep-research/ci.yml?style=for-the-badge&logo=githubactions&logoColor=white)](https://github.com/juanhuttemann/deep-research/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/juanhuttemann/deep-research?style=for-the-badge&logo=github)](https://github.com/juanhuttemann/deep-research/releases)
[![golangci-lint](https://img.shields.io/badge/golangci--lint-enabled-3FB950?style=for-the-badge&logo=go&logoColor=white&labelColor=1B2127)](https://github.com/juanhuttemann/deep-research/actions/workflows/ci.yml)
[![MIT license](https://img.shields.io/badge/License-MIT-3FB950?style=for-the-badge&labelColor=1B2127)](https://github.com/juanhuttemann/deep-research/blob/main/LICENSE)
![Platform](https://img.shields.io/badge/platform-Linux%20%7C%20macOS%20%7C%20Windows-E8EDF2?style=for-the-badge&labelColor=1B2127)

</div>

An AI-powered deep research agent for the terminal. It plans a question into
sub-topics, researches them in parallel on the web, then runs
**research → analyze → fact-check → summarize** and writes a cited report as
Markdown, PDF and JSON.

The first run is free and needs no setup beyond one key: the default model is
OpenRouter's free router and search goes through public SearXNG instances.
Every source says how it was obtained, and nothing the model invents is ever
passed off as a retrieved page.

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
[releases](https://github.com/juanhuttemann/deep-research/releases).

From source (Go 1.26+):

```sh
git clone https://github.com/juanhuttemann/deep-research.git
cd deep-research
make build
```

The build stamps the version into a `deep-research` binary in the repo root.

The live terminal UI is unix-only. Windows binaries work, but the run
degrades to headless output — one log line per event, then the report.

## Quick start

```bash
./deep-research init          # config files, a .env template, where to get a key
```

Get a free OpenRouter key (no card) at <https://openrouter.ai/keys> and put it
in `.env` or your environment:

```bash
export OPENAI_API_KEY="sk-or-..."
./deep-research run "What are the latest advances in fusion energy?"
```

Something off? `./deep-research doctor` checks the model endpoint and key,
the search backend and the scraper — one cheap request each, no tokens — and
prints how many model requests a run spends.

No key at all? A run without one fails and says where to get one. To try the
pipeline with no network, ask for the stub explicitly:

```bash
./deep-research run --offline "question"
```

## What a run looks like

On a terminal you first get a **research brief**: the planned sub-topics,
which you can add to, rename, delete or re-budget before pressing Enter. Keys
pressed while the plan is still being made are ignored, and the brief says so.

Then a **live frame**: the pipeline stage, one row per parallel sub-agent,
the sources as they are read, and one status line for the model call in
flight — waiting for the first token, then sections, claims or words so far
with the change since the last update, and "no new text for 10s" if the
stream stalls.

The **report** is rendered in the terminal and saved to `reports/` as `.md`,
`.pdf` and `.json`. Besides the answer it carries:

- the model that wrote it — the configured one, and which models a router
  actually served;
- the evidence by topic, with the analysis's confidence for each;
- open questions and suggested follow-up searches;
- the fact-check, claim by claim: verified, not verified, unverified,
  contradicted;
- citations split into sources the report cites and sources it only read,
  each labelled `ok` (page fetched), `degraded` (search snippet only) or
  `unverified` (model-supplied, never fetched).

If the analysis or the report call fails late in a run — a provider error, or
the run deadline — what was gathered is still saved and exported, marked
incomplete, and the command exits non-zero.

## Search

| Setting (`SEARXNG_URL`) | What happens |
| ----------------------- | ------------ |
| unset (`auto`) — the default | public SearXNG instances from [searx.space](https://searx.space/), tried in turn; the one that answers is used first next time |
| your instance(s), comma-separated | your own SearXNG; add `FIRECRAWL_URL` to scrape full page text instead of snippets |
| `off` | no search: the model proposes findings from memory, every source is `~ unverified`, and the fact-check runs as a self-consistency pass |

Public instances mostly refuse SearXNG's JSON API, so their HTML result page
is read instead. A search that is refused — rate limit, bot challenge,
outage — is reported with that reason; it is never quietly replaced by
findings the model makes up, and a run whose every search failed stops
instead of writing a report about nothing.

With `auto`, every query goes to a third party you did not pick. To keep
queries on your machine:

```bash
./deep-research init --docker   # docker-compose.yml + SearXNG settings
docker compose up -d
echo 'SEARXNG_URL=http://localhost:8888' >> .env
```

Firecrawl, for full page text, runs from its own repository:
[docs/services.md](docs/services.md).

## Models

`openrouter/free` costs nothing and picks an available free model per request,
so runs are not reproducible model-for-model — which is why every report
records what served it. Free models are capped per minute and per UTC day; a
run is 4 model requests with search (plan, analyze, fact-check, summarize),
more with search off. Hitting the daily cap fails at once with a message
instead of burning retries; a per-minute limit waits for the provider's
`Retry-After`.

Any OpenAI-compatible endpoint works — paid OpenRouter, OpenAI, or a local
vLLM, llama.cpp or LM Studio server, which needs no key: set
`OPENAI_BASE_URL` and `OPENAI_MODEL`.

## Common commands

```bash
./deep-research run "question" --mode deep          # quick | standard | deep
./deep-research run "question" --output report.md   # also save to a file
./deep-research run "question" --silent             # just the report
./deep-research run "question" --jsonl              # machine-readable events
./deep-research run "question" --offline            # stub pipeline, no network
./deep-research list                                # past runs
./deep-research doctor                              # check model, key, search
./deep-research init --docker                       # local SearXNG setup
```

Full flag reference, key bindings and export details: [docs/usage.md](docs/usage.md).

## Configuration

Settings resolve environment > `config/config.yaml` > embedded defaults, and
every `config.yaml` key is also settable as `DEEP_RESEARCH_<KEY>`.

| Env var | Meaning | Default |
| ------- | ------- | ------- |
| `OPENAI_API_KEY` | LLM API key | — (required unless `--offline`) |
| `OPENAI_BASE_URL` | LLM endpoint | `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | model to use | `openrouter/free` |
| `SEARXNG_URL` | a URL, a comma-separated list, `auto` or `off` | `auto` |
| `FIRECRAWL_URL` | Firecrawl instance for page scraping | — (snippets only) |

Timeouts, retries, parallelism, history location, the legacy `OPENROUTER_*`
names and the trust boundaries for scraping and public search are in
[docs/configuration.md](docs/configuration.md).

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
