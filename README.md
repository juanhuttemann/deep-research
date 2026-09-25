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
sub-topics, researches them in parallel, then runs a four-phase pipeline —
**research → analyze → fact-check → summarize** — and writes a cited report
as Markdown, PDF and JSON.

Any OpenAI-compatible endpoint works (OpenRouter, vLLM, llama.cpp,
LM Studio). Web search and scraping via SearXNG and Firecrawl are optional:
without them the model itself proposes the findings, and every source is
labelled as unverified.

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
./deep-research init          # writes config files + a .env template
```

Put your API key in the `.env` that `init` creates, or export it for the
session:

```bash
export OPENAI_API_KEY="sk-..."
```

Then:

```bash
./deep-research run "What are the latest advances in fusion energy?"
```

On a terminal this shows a research brief you can edit and confirm, then a
live frame with the pipeline stage, one row per parallel sub-agent, a rolling
activity tail and running source/token counters. The finished report is
rendered as styled Markdown, and the `.md` / `.pdf` / `.json` artifacts land
in `reports/`.

No API key handy? Run the whole pipeline against a stub:

```bash
DEEP_RESEARCH_OFFLINE=true ./deep-research run "question"
```

## Common commands

```bash
./deep-research run "question" --output report.md   # also save to a file
./deep-research run "question" --mode deep          # quick | standard | deep
./deep-research run "question" --silent             # just the report
./deep-research run "question" --jsonl              # machine-readable events
./deep-research list                                # past runs
```

Full flag reference, key bindings and export details: [docs/usage.md](docs/usage.md).

## Research modes

The mode is decided by what is configured, not by a flag:

| Mode | When | Sources |
| ---- | ---- | ------- |
| Web search | `SEARXNG_URL` **and** `FIRECRAWL_URL` set | real search + scraping; each source marked `✓ fetched`, `! snippet only` or `✗ dropped` |
| LLM search | the shipped default (both empty) | the model proposes findings and URLs; nothing is fetched, every source is `~ unverified` |
| Offline | `offline: true` | no network at all; a stub assistant carries the pipeline |

Only web-search mode produces verification against retrieved pages. In LLM
search the fact-check phase runs as a self-consistency pass and says so.

To enable web search, run SearXNG and Firecrawl locally and set `SEARXNG_URL`
and `FIRECRAWL_URL`. [docs/services.md](docs/services.md) has the setup for
both, with a check at each step.

## Configuration

Settings resolve environment > `config/config.yaml` > embedded defaults, and
every `config.yaml` key is also settable as `DEEP_RESEARCH_<KEY>`. The keys
that matter most:

| Env var | Meaning | Default |
| ------- | ------- | ------- |
| `OPENAI_API_KEY` | LLM API key | — (required for online mode) |
| `OPENAI_BASE_URL` | LLM endpoint | `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | model to use | `deepseek/deepseek-chat` |
| `SEARXNG_URL` | SearXNG instance for web search | — |
| `FIRECRAWL_URL` | Firecrawl instance for page scraping | — |

Timeouts, retries, parallelism, history location, the config-directory
lookup, the legacy `OPENROUTER_*` names and the scraper's trust boundary are
all covered in [docs/configuration.md](docs/configuration.md).

## Architecture

```
cmd/deep-research   entrypoint
internal/agent      one LLM phase per method (ResearchDetail/Analyze/FactCheck/
                    Summarize/Plan)
internal/cli        cobra commands (run/list/init)
internal/config     config resolution + .env
internal/store      append-only JSONL run history
internal/tools      SearXNG search + Firecrawl scrape HTTP clients
internal/ui         live terminal frame, event sinks, md+pdf+json export
```

`ui.Driver` is the only thing that sequences the pipeline; the agent package
talks to one model and parses what comes back. See
[docs/architecture.md](docs/architecture.md).

## Development

```bash
make verify   # fmt, vet, lint, deadcode, go test -race — the gate
make tools    # installs the lint binaries
```

Conventions, test style and the architecture rules a change has to respect
are in [CONTRIBUTING.md](CONTRIBUTING.md); cutting a release is
[RELEASING.md](RELEASING.md). Notable changes are recorded in
[CHANGELOG.md](CHANGELOG.md).

## License

MIT — see [LICENSE](LICENSE).
