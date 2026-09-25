# Configuration

## Precedence

Settings resolve in one order, highest first:

1. environment variables
2. `config.yaml` in the config directory
3. the defaults embedded in the binary

The config directory is the first of these that exists:
`$DEEP_RESEARCH_CONFIG_DIR`, `./config/`, `~/.config/deep-research/`.
`deep-research init` writes the defaults there.

`.env` is looked up from the working directory upwards, stopping at the first
one found and never climbing past the project root (a directory holding
`.git` or `go.mod`), so an unrelated `.env` in `$HOME` cannot leak into a run.

## Provider and services

| Env var | Meaning | Default |
| ------- | ------- | ------- |
| `OPENAI_API_KEY` | LLM API key | — (required for online mode) |
| `OPENAI_BASE_URL` | LLM endpoint (any OpenAI-compatible API) | `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | model to use | `deepseek/deepseek-chat` |
| `SEARXNG_URL` | SearXNG instance for web search | — (empty: use the LLM search agent) |
| `FIRECRAWL_URL` | Firecrawl instance for page scraping | — (empty: use the LLM search agent) |
| `FIRECRAWL_API_KEY` | bearer token for hosted Firecrawl (self-hosted needs none) | — |

To run SearXNG and Firecrawl yourself, see [services.md](services.md).

The legacy `OPENROUTER_API_KEY` / `OPENROUTER_BASE_URL` / `OPENROUTER_MODEL`
names are still read as a fallback, so existing `.env` files keep working.

Each of these can also be written in `config.yaml` under the lower-case key
(`openai_base_url`, `openai_model`, `searxng_url`, `firecrawl_url`,
`firecrawl_api_key`); the environment variable wins where both are set. Keep
the API key in `.env`, not in the config file.

## `config.yaml` keys

| Key | Default | Meaning |
| --- | ------- | ------- |
| `data_file` | `~/.deep-research/research.jsonl` | append-only run history |
| `offline` | `false` | run with no network calls at all |
| `model_call_timeout` | `120s` | one attempt at one model call |
| `model_call_retries` | `2` | extra attempts for a failed call; each retry doubles that timeout |
| `run_timeout` | `30m` | the whole run, including search and scraping |
| `sources_per_topic` | `0` | sources per sub-agent; `0` lets the `--mode` tier decide. The older `max_depth` spelling still works |
| `parallelism` | `3` | sub-agents searching at once; a plan with more sub-topics queues them |

Every `config.yaml` key can also be set from the environment as
`DEEP_RESEARCH_<KEY>` in uppercase — `DEEP_RESEARCH_OFFLINE=true`,
`DEEP_RESEARCH_SOURCES_PER_TOPIC=9`, `DEEP_RESEARCH_PARALLELISM=6` — and the
environment wins over the file. This is a per-key override channel, so a
stray `DEEP_RESEARCH_*` variable left in a shell changes how runs behave;
`env | grep DEEP_RESEARCH` is the place to look when a config file seems to
be ignored.

Agent prompts live in `agent.yaml` in the same directory, one block per
phase, so they can be edited without touching Go code.

## Research modes

Which of the three modes a run uses is decided by what is configured, not by
a flag:

- **Web search** — `searxng_url` *and* `firecrawl_url` both set. Findings
  come from real web search plus page scraping. Each source is labelled by
  how it was obtained: `✓ fetched`, `! snippet only`, `✗ dropped`. Both URLs
  must be set; with only one, runs use LLM search. See
  [services.md](services.md) to set the services up.
- **LLM search** — the shipped default, since both URLs are empty. The model
  itself proposes the findings and their URLs. Nothing is fetched, so every
  source is labelled `~ unverified` and exported with that status. The
  fact-check phase says so too: with no retrieved page to check an answer
  against, it runs as a self-consistency pass ("Checking self-consistency (no
  page was retrieved)") and the report presents its claims as unverified
  recollection. Only web-search mode produces verification against sources.
- **Offline** — `offline: true`. No network calls at all; a stub assistant
  carries the pipeline so the CLI, store and report path can be exercised.

If the online assistant cannot be built (e.g. missing API key), the run falls
back to offline mode with a warning.

## Scraping trust boundary

In web-search mode the scraper is handed URLs that came from search results —
and in LLM-search mode, from the model — so they are outside input to a
server-side fetcher that runs inside your network. Only `http`/`https`
targets are scraped, and targets that name the fetcher's own host, a private
or link-local address, or the cloud metadata service are refused before any
request is made (the source is reported with a `blocked` code).

Hostnames are resolved by the scraper itself, not here, so a name that
resolves into private space is not caught by this check: run a self-hosted
Firecrawl where you would run any other service that fetches arbitrary URLs,
not somewhere it can reach admin interfaces.
