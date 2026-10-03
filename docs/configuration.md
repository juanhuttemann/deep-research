# Configuration

## Precedence

Settings resolve in one order, highest first:

1. environment variables
2. `config.yaml` in the config directory
3. the defaults embedded in the binary

The config directory is the first of these that exists:
`$DEEP_RESEARCH_CONFIG_DIR`, `./config/`, `~/.config/deep-research/`.
`deep-research init` writes the defaults there. Newly created `.env` files
use owner-only permissions (`0600`) on Unix; existing files are left alone.

`.env` is looked up from the working directory upwards, stopping at the first
one found and never climbing past the project root (a directory holding
`.git` or `go.mod`), so an unrelated `.env` in `$HOME` cannot leak into a run.

## Provider and services

| Env var | Meaning | Default |
| ------- | ------- | ------- |
| `OPENAI_API_KEY` | LLM API key; free at <https://openrouter.ai/keys> | — (required) |
| `OPENAI_BASE_URL` | LLM endpoint (any OpenAI-compatible API) | `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | model to use | `openrouter/free` |
| `SEARXNG_URL` | SearXNG for web search: one URL, a comma-separated list, `auto` (public instances) or `off` (LLM search) | `auto` |
| `FIRECRAWL_URL` | Firecrawl instance for page scraping | — (empty: search snippets only) |
| `FIRECRAWL_API_KEY` | bearer token for hosted Firecrawl (self-hosted needs none) | — |

A trailing slash on `FIRECRAWL_URL` is ignored when building API paths.

To run SearXNG and Firecrawl yourself, see [services.md](services.md).

### The default model

`openrouter/free` is OpenRouter's free-model router: it costs nothing and
picks an available free model per request, so no pinned `:free` id goes stale
when OpenRouter retires it. Two consequences:

- Runs are not reproducible model-for-model. Every artifact and history
  record names the configured model and provider host (`model`, `provider`)
  and the models the provider reported serving (`served_by`), read from each
  response's `model` field.
- Free models are capped per minute and per UTC day, and the daily ceiling
  depends on credits bought. A run is a handful of requests, not tokens:
  plan, analyze, fact-check and summarize — 4–7 with web search, including
  optional reanalysis, repair and revision checking. LLM search adds up to
  2 queries per sub-topic and per follow-up. Worst-case totals are quick 19,
  standard 21 and deep 25 before retries.
  `deep-research doctor` prints these worst-case budgets (7 with web
  search) and, for an OpenRouter key, its usage, tier and today's free-model requests (used, limit, left). A per-minute 429 is retried after the `Retry-After`
  it names; the daily cap (`free-models-per-day`) fails at once with a
  message instead of burning the retries on a limit that clears only at
  00:00 UTC.
- The router counts against the same caps as any `:free` model. OpenRouter's
  router guide says each request is forwarded to a free model and names it
  in the response as a `:free` variant (`upstage/solar-pro-3:free`), and its
  limits page applies the caps to `:free` variants: 20 requests a minute,
  and 50 a day — 1000 once at least 10 credits have been bought. That is
  documented behaviour (checked September 2026), not a measured one; the
  `:free` ids in a run's `served_by` and the "free-model requests today"
  line in `doctor` show it for a real key.

Provider errors omit URL userinfo from their messages, including doctor output.

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
| `model_call_timeout` | `120s` | one attempt at one model call |
| `model_call_retries` | `2` | extra attempts for a failed call; each retry doubles that timeout. A rejected key (401) is not retried |
| `run_timeout` | `30m` | the whole run, including search and scraping |
| `sources_per_topic` | `0` | sources per sub-agent; `0` lets the `--mode` tier decide. The older `max_depth` spelling still works |
| `parallelism` | `3` | sub-agents searching at once; a plan with more sub-topics queues them |

Every `config.yaml` key can also be set from the environment as
`DEEP_RESEARCH_<KEY>` in uppercase — `DEEP_RESEARCH_SOURCES_PER_TOPIC=9`, `DEEP_RESEARCH_PARALLELISM=6` — and the
environment wins over the file. This is a per-key override channel, so a
stray `DEEP_RESEARCH_*` variable left in a shell changes how runs behave;
`env | grep DEEP_RESEARCH` is the place to look when a config file seems to
be ignored.

Agent prompts live in `agent.yaml` in the same directory, one block per
phase, so they can be edited without touching Go code. `chat_instructions`
is not a phase: it steers `ask` and the page's follow-up questions. A block
left out falls back to the built-in prompt, so an `agent.yaml` written
before a block existed still gets it. `prompts_version: 1` marks the output
schema the parsers expect: a file with a missing or different version warns
on stderr and is still used, because an older analyzer or fact-check prompt
can return JSON this release cannot read. Compare the file with this
release's `config/agent.yaml`, bring your prompts to its output schemas, and
only then set the version. `init` writes the current version into new files
and leaves existing ones alone.

## Research modes

Which mode a run uses is decided by what is configured:

- **Web search** — `searxng_url` set, which it is by default (`auto`).
  Findings come from real web search. With `firecrawl_url` set too, each
  result page is scraped for its full text; without it the search snippet is
  the source. Firecrawl is given 60 seconds per page, and the request gives
  up at 70 whatever `model_call_timeout` says: a page it cannot fetch in
  that time falls back to its search snippet, like any failed scrape. Each source is labelled by how it was obtained: `✓ fetched`,
  `! snippet only` (with the reason: `noservice` when no scraper is
  configured, `empty` when the scraper returned a blank page, or the HTTP
  status), `✗ dropped`.
  - `auto` reads the public instance list from
    [searx.space](https://searx.space/), keeps the reachable, analytics-free
    instances with a search success rate, and tries the best ten in turn.
    The list is only candidates: searx.space's checker is whitelisted by most
    instances' limiters, so an instance proves itself by answering a real
    query, and the one that answered is asked first next time. Public
    instances rarely enable SearXNG's JSON API, so they are read through
    their HTML result page. Successful discovery is cached for the run; a
    failed or cancelled discovery is retried by the next query; queries that
    ask while one discovery runs wait for it rather than starting their own,
    and share its outcome unless it was cancelled. Each query
    has a one-minute deadline across discovery and all instance/JSON/HTML
    attempts (or an earlier caller deadline); each request is still capped
    at 30 seconds.
  - A URL, or a comma-separated list of URLs, pins your own instances. JSON is
    asked for first; an instance that refuses it is read through HTML.
  - A search that every instance refuses fails with its reason —
    `rate-limited`, `challenge`, `blocked`, `unreachable` (nothing answers at
    the address) or `unavailable` — on the event and in the error. It is never replaced by the model inventing findings,
    and a run whose every search failed stops before analysing nothing.
- **LLM search** — `searxng_url: off`. The model itself proposes the findings
  and their URLs. Nothing is fetched, so every source is labelled
  `~ unverified` and exported with that status. The fact-check phase says so
  too: with no retrieved page to check an answer against, it runs as a
  self-consistency pass ("Checking self-consistency (no page was retrieved)")
  and the report presents its claims as unverified recollection. When a run
  holds both kinds, every prompt marks each unfetched finding `[never
  fetched]` and the phase line says how many there are.
A missing API key is an error that says where to get one. It used to fall
back to a stub assistant with a warning, which produced a complete-looking
run that had researched nothing.

### Public search instances

With `auto`, every query and every result goes through a SearXNG instance run
by a third party you did not pick, and that party's rate limits and retention
apply. Public instances are donated capacity: queries use the same
`Parallelism` bound as any run, carry an honest `User-Agent`, and move to the
next instance on a refusal rather than retrying the one that refused. Set
`searxng_url` to your own instance to keep queries local.

DuckDuckGo is deliberately not a backend. Its HTML endpoints answered every
request from here with a `202` bot challenge and no results (September
2026), and the Go client for it (`duckduckgogo`) fails on that `202` and
pulls in goquery for one selector. A DuckDuckGo backend would need a
challenge-aware transport and should never be the default.

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
