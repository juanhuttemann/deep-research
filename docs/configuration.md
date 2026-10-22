# Configuration

## What `drsh setup` writes

[`drsh setup`](usage.md#setup) is the way to configure a run; the files and
variables below are what it writes, for reading and for scripts. All of them
live in the config directory:

| File | Holds | Written by |
| ---- | ----- | ---------- |
| `auth.json` | API keys: the model provider's, and Exa's, Tavily's and hosted Firecrawl's | `setup`, `login` |
| `model.json` | the chosen provider, endpoint and model; no secrets | `setup`, `setup model`, `login`, `model` |
| `config.yaml` | everything else; setup writes `search_engine`, `searxng_url`, `scraper` and `firecrawl_url` and leaves the rest as you wrote it | `setup search`, `setup scraper`, `init`, you |
| `.env` | environment variables, read at start | you (`init` writes a commented template) |

Environment variables still override all of it: for CI, and for a one-off
run (`OPENAI_MODEL=x drsh -p ...`). `setup`'s review says when one will win
over a choice and where it was set.

## Precedence

Settings resolve in one order, highest first:

1. environment variables
2. the provider and model `drsh login` picked (`model.json`)
3. `config.yaml` in the config directory
4. the defaults embedded in the binary

API keys resolve differently: a key stored by `drsh setup` or `drsh login`
wins, and the environment (`OPENAI_API_KEY`, `ANTHROPIC_API_KEY`,
`EXA_API_KEY`, `TAVILY_API_KEY`, `FIRECRAWL_API_KEY`) is the fallback, then
`config.yaml`. See [Stored keys](#stored-keys-drsh-login).

The config directory is `$DEEP_RESEARCH_CONFIG_DIR` when set, else
`~/.config/deep-research/` (`%AppData%\deep-research` on Windows,
`~/Library/Application Support/deep-research` on macOS). It is the only place
`config.yaml` and `.env` are read from, so a run behaves the same whatever
folder it starts in. A project's own `config/config.yaml` or `.env` is never
read: most projects' `.env` holds an `OPENAI_API_KEY` of its own, which would
replace yours. When the working directory has either file with deep-research
settings in it (from a release that read them), every run warns and `doctor`
names the file to move.

`drsh setup` writes `config.yaml` there when it saves search settings and
there is none; `drsh init` writes a default `config.yaml` and a `.env`
template. Newly created `.env` files use owner-only permissions (`0600`) on
Unix; existing files are left alone.

## Stored keys (`drsh login`)

`drsh setup` (and `drsh login` and `drsh model`, its model step on its own)
picks a model from one list of every provider's models and stores the key
when the provider needs one; `drsh logout` removes one
([usage](usage.md#login-model-logout)).
Two files, in `$DEEP_RESEARCH_CONFIG_DIR` when set, else
`~/.config/deep-research/`:

- `auth.json` holds the keys, one per provider, each with the endpoint it was
  entered for. It is created `0600` in a `0700` directory and replaced
  atomically. A group- or world-readable file loads with a warning.
- `model.json` holds the selection: provider, transport, endpoint and model.
  It has no secrets.

A search service's key is stored as its own entry, `service:exa`,
`service:tavily` or `service:firecrawl`, of type `service`, with the address
it was entered for: Exa's and Tavily's APIs, or the Firecrawl it was typed for.
A hosted Firecrawl key is not sent to a `FIRECRAWL_URL` pointed at another
server. The entries are flat, so a release from before `setup` keeps them
when it rewrites the file; such a release does not know to keep them apart
from model keys, though, and would send one to a model endpoint at the very
same address.

Every key goes only to its own endpoint. A stored key goes to the endpoint it
was entered for: with `OPENAI_BASE_URL` pointed somewhere else for a run, the
OpenRouter key stays home and `OPENAI_API_KEY` applies instead. A key from the
environment or `config.yaml` goes to the endpoint the environment and
`config.yaml` name (OpenRouter when they name none): with DeepSeek picked in
`login`, an `OPENAI_API_KEY` meant for OpenRouter is not sent to DeepSeek, and
`login` asks for DeepSeek's key. The environment still overrides the
selection: `OPENAI_MODEL=x drsh -p ...` is a one-off run on `x`, and
`login` warns when something in the environment overrides what it just saved.

A key that starts with `!` is a command whose output is the key, run once per
process with a 10-second limit, so the secret can stay in a password manager
or the OS keychain:

```json
{"openrouter": {"type": "api_key", "key": "!pass show openrouter", "base_url": "https://openrouter.ai/api/v1"}}
```

The provider list is [Catwalk](https://github.com/charmbracelet/catwalk)'s
catalog, the one Crush uses, kept to the providers deep-research has a client
for: OpenAI, OpenRouter and the OpenAI-compatible ones; Anthropic and the
Anthropic-compatible ones (Kimi, MiniMax). Two entries are deep-research's
own: the Claude subscription through Claude Code, which needs no key, and a
custom OpenAI-compatible endpoint (vLLM, LM Studio, llama.cpp), whose models
are read from its `/models`. A copy is built in; `login` refreshes it from
Catwalk when online and caches it under the user cache directory.

Picking Anthropic, Kimi or MiniMax runs on the key alone: Claude Code is used
only for the Claude subscription entry (or `provider: claude` set without
login), so a run never bills the plan when you chose the API.

## Environment variables (CI and one-off overrides)

`drsh setup` covers everything in this section. Each setting also has an
environment variable, read from the shell or the config directory's `.env`,
that wins over what setup saved: the way to configure CI, or to change one run
without touching the saved setup. A variable left set by mistake overrides a
choice made later in setup, which is why setup's review lists them.

| Env var | Meaning | Default |
| ------- | ------- | ------- |
| `OPENAI_API_KEY` | LLM API key, when `drsh setup` stored none for the endpoint; free at <https://openrouter.ai/keys> | — (required) |
| `OPENAI_BASE_URL` | LLM endpoint (any OpenAI-compatible API) | `https://openrouter.ai/api/v1` |
| `OPENAI_MODEL` | model to use | `openrouter/free` |
| `SEARXNG_URL` | SearXNG for web search: one URL, a comma-separated list, `auto` (public instances) or `off` (LLM search) | `auto` |
| `FIRECRAWL_URL` | Firecrawl instance for page scraping | — (empty: search snippets only) |
| `FIRECRAWL_API_KEY` | bearer token for hosted Firecrawl (self-hosted needs none), when `setup` stored none for that address | — |
| `DEEP_RESEARCH_SEARCH_ENGINE` | `searxng`, `exa`, `tavily` or `off` (LLM search) | `searxng` |
| `DEEP_RESEARCH_SCRAPER` | `firecrawl`, `exa`, `tavily` or `none` | `firecrawl` when `FIRECRAWL_URL` is set, else `none` |
| `EXA_API_KEY` | key for [Exa](https://exa.ai), as engine or scraper, when `setup` stored none | — |
| `TAVILY_API_KEY` | key for [Tavily](https://tavily.com), as engine or scraper, when `setup` stored none | — |

A trailing slash on `FIRECRAWL_URL` is ignored when building API paths.

To run SearXNG and Firecrawl yourself, see [services.md](services.md).

### Hosted search: Exa and Tavily

`search_engine: exa` or `search_engine: tavily` searches through that service
instead of SearXNG. Each search asks for every result's page text too (Exa
`contents.text`, Tavily `include_raw_content`), so a query is one request and
no scraper is needed: a result that brought its text is labelled `✓ fetched`
and never scraped. A scraper, when one is set, only covers a result that came
back without text.

`scraper: exa` or `scraper: tavily` uses the service's extract endpoint
(`/contents`, `/extract`) as the scraper instead of Firecrawl, behind any
engine, SearXNG included.

The engine is chosen, never inferred from an exported key: with a hosted
engine every query goes to that company and spends its credit. Measured
October 2026: Exa charged a flat $0.007 per search, the ten results' text and
highlights included, and about $0.001 per extracted page; Tavily charges one credit per basic search and one
per five extracted pages. An engine chosen without its key fails at start, and
a rejected key or exhausted credit is reported as `unauthorized` or `quota`
rather than falling back to SearXNG or to the model.

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
  plan, follow-ups, analyze, fact-check and summarize — 4–13 with web
  search, including optional repair and revision checking. The fact-check
  runs as parallel calls: one per share of six or more claims, at most four,
  and one per four recommendations and conclusions, at most three, so a run
  with many of both spends up to seven requests on it at once. LLM search
  adds up to 2 queries per sub-topic and per follow-up. Worst-case totals are
  quick 25, standard 27 and deep 31 before retries.
  `drsh doctor` prints these worst-case budgets (13 with web
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
`firecrawl_api_key`, `search_engine`, `scraper`, `exa_api_key`,
`tavily_api_key`); the environment variable wins where both are set. Keep
keys in `auth.json` (`drsh setup` puts them there) or `.env`, not in the
config file.

### Claude

`provider: claude` (or `DEEP_RESEARCH_PROVIDER=claude`) runs the model on
Claude instead of the OpenAI-compatible endpoint. The run reaches it one of
two ways, decided when it starts:

1. **Your Claude subscription, through Claude Code.** When `claude` is on
   `PATH` and `claude auth status` reports a claude.ai sign-in, each model
   call runs `claude -p` with no tools, with the phase's prompt as the system
   prompt, and with none of your CLAUDE.md, hooks, MCP servers or plugins
   (`--safe-mode`). The usage counts against the plan's limits; a deep run
   sends every source through the model several times. Anthropic's terms let
   a tool run the unmodified Claude Code binary under the user's own sign-in,
   and do not let it read that sign-in's credentials, so deep-research never
   touches them.
2. **`ANTHROPIC_API_KEY`, through the Messages API**, when Claude Code is
   missing, signed out, or signed in to API billing.

With both, a run that reaches the subscription's usage limit says so and
continues on the key for the rest of the run. Without a key the limit fails
the call at once, and the error says when it resets.

`ask` and the page's follow-up questions always need the key: they call
deep-research's own tools, which `claude -p` cannot reach.

Opus can think for more than two minutes before the first word of a large
analysis or fact-check. Claude Code shows none of that thinking, so the
status reads "waiting for the model to start". If nothing at all arrives for
the default `model_call_timeout` of `120s`, the call is cut off and retried
with twice the time, and on a subscription the cut-off attempt still counts
against the plan. Set `model_call_timeout: 300s` to avoid that.

| Key | Env var | Default |
| --- | ------- | ------- |
| `provider` | `DEEP_RESEARCH_PROVIDER` | `openai`; `claude` is the other, and anything else is an error |
| `claude_model` | `DEEP_RESEARCH_CLAUDE_MODEL` | `claude-opus-5-5` |
| `anthropic_api_key` | `ANTHROPIC_API_KEY` | — |
| `anthropic_base_url` | `ANTHROPIC_BASE_URL` | `https://api.anthropic.com` |

The model has no `ANTHROPIC_MODEL` name: Claude Code reads that variable,
often set to an alias the API does not know. The `ANTHROPIC_*` variables are
removed from the environment `claude -p` runs in, so a key kept for the
fallback is never what Claude Code bills.

On the API, the streamed phases (plan, analysis, fact-check, report) allow
64000 output tokens with adaptive thinking shown on the status line; the
others allow 16000. JSON is asked for in the prompt only, because the
Messages API has no JSON mode without a schema. On `api.anthropic.com` with
Opus 5.5, Opus 5, Sonnet 5.5 or Fable 5.1, a request a safety classifier
declines is answered by another model within the same call (the server-side
fallback beta). If the API refuses the fallback itself (an account with none
configured, or a retired beta), the run sends its requests without it, as
Claude Code does. A refusal that still comes back, from either provider,
fails the phase as "declined" instead of passing on an empty answer. Models
older than the 4.6 generation, such as Haiku 4.5, are not supported on the
API: they reject adaptive thinking.

## `config.yaml` keys

| Key | Default | Meaning |
| --- | ------- | ------- |
| `data_file` | `~/.deep-research/research.jsonl` | append-only run history |
| `model_call_timeout` | `120s` | one attempt at one model call. For the streamed phases (plan, analysis, fact-check, report) it is how long the model may send nothing: each piece of reasoning or answer restarts it, so a model still working is not cut off. A streamed attempt stops at five times this (10 minutes at the default) however steadily it streams, and is not retried: the same prompt would run as long again |
| `model_call_retries` | `2` | extra attempts for a failed call; each retry doubles that timeout. A rejected key (401) is not retried |
| `run_timeout` | `30m` | the whole run, including search and scraping |
| `sources_per_topic` | `0` | sources per sub-agent; `0` lets the `--mode` tier decide. The older `max_depth` spelling still works |
| `parallelism` | `3` | sub-agents searching at once; a plan with more sub-topics queues them |
| `reasoning_effort.<phase>` | see below | how hard each phase's model reasons: `default`, `none`, `minimal`, `low`, `medium`, `high`, `xhigh` or `max` |

### Reasoning effort

| To | Set |
| --- | --- |
| speed up the fact-check for one run | `DEEP_RESEARCH_REASONING_EFFORT_FACT_CHECK=medium drsh -p "…"` |
| make every run's analysis think harder | `reasoning_effort:` / `  analyze: high` in `config.yaml` |
| go back to what the provider does | the phase's value `default` |
| see what a run sent | the `analyze  <model> · effort …` lines: on stderr, in `--jsonl`, and in the `.json` timeline of an interactive or `--jsonl` run |

Thinking is most of a run's time, and the phases need it unequally. The
defaults:

```yaml
reasoning_effort:
  plan: low          # writes the search queries
  search: default    # only runs with no search engine, and then it writes the findings
  analyze: default   # the claims and conclusions the answer rests on
  fact_check: default
  summarize: default # the report's prose below the Answer section
  chat: medium       # `ask` and the page's follow-up questions
```

`default` sends nothing, so the provider decides, and providers decide
differently: DeepSeek V4.1 Flash on OpenRouter reasons as much as at `high`,
Claude Opus 5.5 defaults to `medium`. That is why analyze and fact-check are
not set to one level for every provider. Each phase is its own key, so a
`config.yaml` that names one phase keeps the others' defaults, and
`DEEP_RESEARCH_REASONING_EFFORT_<PHASE>` (`PLAN`, `SEARCH`, `ANALYZE`,
`FACT_CHECK`, `SUMMARIZE`, `CHAT`) overrides one from the environment. An
empty variable changes nothing; write `default`. The repair pass uses the
analyze and fact-check levels.

What a provider does with a level is its own: an OpenAI-compatible endpoint
is sent `reasoning_effort`, Claude `output_config.effort` (and `claude -p
--effort`), and a model without reasoning ignores it. A level the endpoint
refuses with a 400 about reasoning, as Gemini refuses `none`, is asked again
without it, and left out for the rest of the run once that worked; the run
says so. Claude cannot turn thinking off, so it is sent `low` for `none` and
`minimal`. `drsh doctor` does not check effort.

Every `config.yaml` key can also be set from the environment as
`DEEP_RESEARCH_<KEY>` in uppercase — `DEEP_RESEARCH_SOURCES_PER_TOPIC=9`, `DEEP_RESEARCH_PARALLELISM=6` — and the
environment wins over the file. This is a per-key override channel, so a
stray `DEEP_RESEARCH_*` variable left in a shell changes how runs behave;
`env | grep DEEP_RESEARCH` is the place to look when a config file seems to
be ignored.

## Agent prompts

Agent prompts are built into the binary, one block per phase, and each
release brings its own. To change one without touching Go code, create
`agent.yaml` in a config directory with `prompts_version: 1` and only the
blocks you change, copied from this release's
[`config/agent.yaml`](../config/agent.yaml): a block left out falls back to
the built-in prompt, so the others keep following releases.
`chat_instructions` is not a phase: it steers `ask` and the page's follow-up
questions. `init` does not write `agent.yaml`; earlier releases did, and an
unedited copy from them keeps later releases' prompts from reaching you:
delete it.

`prompts_version: 1` marks the output schema the parsers expect: a file with
a missing or different version warns on stderr, naming the file, and is
still used, because an older analyzer or fact-check prompt can return JSON
this release cannot read. Move the file aside to use the built-in prompts,
or bring your blocks to this release's output schemas and only then set the
version.

## Research modes

Which mode a run uses is decided by what is configured:

- **Web search** — `searxng_url` set, which it is by default (`auto`), or
  `search_engine` set to `exa` or `tavily` (see above).
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
    When every public instance refuses with a challenge, a block or a limit,
    the run says once what to do instead (a local SearXNG, or Exa/Tavily).
  - A URL, or a comma-separated list of URLs, pins your own instances. JSON is
    asked for first; an instance that refuses it is read through HTML.
  - With more than one instance (`auto`, or a list), one that fails a query
    is not asked again for a while: ten minutes after a challenge, a `403`
    or no answer at its address, two minutes after a rate limit, timeout or
    other error. A query whose every instance is resting fails at once
    instead of waiting through each. A single instance never rests. When a
    whole pool fails, the error counts the failures by kind
    (`all 10 public SearXNG instances failed: 9 challenged, 1 timed out`).
    Once a query finds every instance resting, its sub-agent sends no more
    queries and the follow-up round is skipped, since none of them would ask
    anything. An instance whose answer was all off-topic rests two minutes
    too, and each search's line names the instance that answered
    (`searxng 10 results from baresearch.org`).
  - A search refused, or answered with nothing on the subject, gave the run
    nothing. When those outnumber the searches that gave something, a run
    with fewer than three sources stops before the analysis, and one with
    more is delivered marked incomplete.
  - A search that every instance refuses fails with its reason —
    `rate-limited`, `challenge`, `blocked`, `unreachable` (nothing answers at
    the address), `unauthorized` or `quota` (a hosted engine's key) or
    `unavailable` — on the event and in the error. It is never replaced by the model inventing findings,
    and a run whose every search failed stops before analysing nothing.
- **LLM search** — `searxng_url: off` or `search_engine: off`. The model itself proposes the findings
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
