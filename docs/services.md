# Running SearXNG and Firecrawl

Out of the box, web search uses public SearXNG instances. Running your own
keeps queries on your machine, and adding Firecrawl gives each source its
full page text instead of a search snippet:

| Service | Role | URL used below | API key |
| ------- | ---- | -------------- | ------- |
| SearXNG | searches the web, returns result URLs and snippets | `http://localhost:8888` | none |
| Firecrawl | fetches each result page and returns Markdown | `http://localhost:3002` | none when self-hosted |

Set `SEARXNG_URL` to use your SearXNG; set `FIRECRAWL_URL` too to scrape the
pages. SearXNG alone is enough for web search — each source is then its
search snippet, marked `snippet only`
([configuration.md](configuration.md#research-modes)).

For SearXNG alone, `deep-research init --docker` writes a ready
`docker-compose.yml` and settings file (JSON enabled, bound to localhost):
`docker compose up -d`, then set `SEARXNG_URL=http://localhost:8888`. The
sections below do the same by hand and add Firecrawl.

Follow the three sections in order. Each ends with a command that tells you
whether that piece works, so you find a problem at the step that caused it.

## Before you start

- Docker with Compose v2 (`docker compose version`).
- Ports 8888 and 3002 free. To use different ports, change them consistently
  in the commands below and in `SEARXNG_URL` / `FIRECRAWL_URL`.
- About 6 GB of disk for images. Firecrawl is the bulk of it (its API image
  is ~2.7 GB and its browser image ~2 GB); SearXNG is small.

## 1. Run SearXNG

Official docs: [Installation with Docker](https://docs.searxng.org/admin/installation-docker.html).

### Create the settings file

SearXNG's JSON API is off by default — its default `formats` list contains
only `html`. This CLI asks for `format=json` first and falls back to reading
the HTML result page, so JSON is not required, but it is the cheaper and
sturdier of the two. Create the settings file before first start:

```sh
mkdir -p searxng/config

cat > searxng/config/settings.yml <<YAML
use_default_settings: true

server:
  secret_key: "$(openssl rand -hex 32)"

search:
  formats:
    - html
    - json
YAML
```

`use_default_settings: true` keeps every SearXNG default and applies only the
two blocks above, so this file stays valid across upgrades.

### Start the container

```sh
docker run --name searxng -d --restart unless-stopped \
    -p 8888:8080 \
    -e FORCE_OWNERSHIP=false \
    -v "$PWD/searxng/config/:/etc/searxng/" \
    docker.io/searxng/searxng:latest
```

Set `FORCE_OWNERSHIP=false` as shown. Without it the container takes
ownership of everything in the mounted directory, and later edits to
`settings.yml` need `sudo`.

### Check it works

```sh
curl -s 'http://localhost:8888/search?q=test&format=json&categories=general' \
  | head -c 200
```

Expect JSON beginning `{"query": "test", "results": [...`. If you get an HTML
`403 Forbidden` page, the `search.formats` block did not take effect — see
[Troubleshooting](#troubleshooting).

Restart after any change to `settings.yml`: `docker restart searxng`.

## 2. Run Firecrawl

Official docs: [Self-hosting](https://docs.firecrawl.dev/contributing/self-host).

Firecrawl runs as several containers — API, a Playwright browser service,
Redis, RabbitMQ and Postgres — so you drive it from its own repository.

### Get the source and configure it

```sh
git clone https://github.com/firecrawl/firecrawl.git
cd firecrawl
git checkout v2.11.162

cat > .env <<YAML
PORT=3002
HOST=0.0.0.0
USE_DB_AUTHENTICATION=false
POSTGRES_USER=postgres
POSTGRES_PASSWORD=$(openssl rand -hex 24)
POSTGRES_DB=postgres
YAML
```

Check out a release tag rather than tracking `main`; the compose file and its
services change between releases.

`USE_DB_AUTHENTICATION=false` is what lets a self-hosted instance accept
requests with no API key, which is how this CLI calls it by default.

### Start the stack

```sh
docker compose up -d
docker compose ps --all
```

Start the whole stack with `docker compose up -d`, as shown. Do not start
individual services — in this release the `api` service does not declare a
dependency on the database, so bringing it up on its own leaves it unable to
reach Postgres and it exits.

By default this builds the API and browser images from source, which takes
a while. To pull prebuilt images instead, add this file next to
`docker-compose.yaml` before starting:

```sh
cat > docker-compose.override.yaml <<'YAML'
services:
  api:
    image: ghcr.io/firecrawl/firecrawl:latest
    build: !reset null
  playwright-service:
    image: ghcr.io/firecrawl/playwright-service:latest
    build: !reset null
  nuq-postgres:
    image: ghcr.io/firecrawl/nuq-postgres:latest
    build: !reset null
YAML
```

### Check it works

Give the API about a minute to finish starting, then:

```sh
curl --fail-with-body --silent --show-error --max-time 75 -X POST \
  http://localhost:3002/v2/scrape \
  -H 'Content-Type: application/json' \
  -d '{"url": "https://example.com", "formats": ["markdown"], "timeout": 60000}'
```

Expect `{"success":true,"data":{"markdown":"Example Domain...` with
`"statusCode":200` in the metadata. A connection refused shortly after
starting usually means the API is still booting; retry before investigating.

## 3. Point deep-research at them

Add the URLs to the `.env` that `deep-research init` writes:

```sh
SEARXNG_URL=http://localhost:8888
FIRECRAWL_URL=http://localhost:3002
```

You can instead set `searxng_url` and `firecrawl_url` in `config.yaml`.
Environment variables win over that file.

Then run anything:

```sh
deep-research run "What is the Firecrawl v2 scrape API?" --mode quick
```

### Confirm it used web search

The summary line at the end of a run counts how sources were obtained:

```
sources  9 (9 fetched)      tokens  18.1k
```

`fetched` means Firecrawl returned the page. `snippet only` means the search
result was used without the page. `unverified` means the run used LLM search
and fetched nothing — if you see that, `SEARXNG_URL` is `off`.
`deep-research doctor` checks both services directly.

While a run is in progress each result is reported as it is handled, marked
`ok`, `snippet`, `off-topic` (the page did not match the question, so it was
not fetched) or `dropped`.

## Using hosted Firecrawl instead

[firecrawl.dev](https://firecrawl.dev) serves the same v2 API, so you can
skip section 2 and set:

```sh
FIRECRAWL_URL=https://api.firecrawl.dev
FIRECRAWL_API_KEY=fc-...
```

The CLI sends an `Authorization` header only when `FIRECRAWL_API_KEY` is set.
The hosted service rejects unauthenticated scrapes with 401, and the run
continues on search snippets rather than stopping, so a missing or wrong key
shows up as every source being `snippet only`.

## Troubleshooting

| Symptom | Fix |
| ------- | --- |
| SearXNG returns an HTML `403 Forbidden` for `format=json` | The `search.formats` block is not in effect. Confirm the file you edited is the one mounted at `/etc/searxng/settings.yml`, then `docker restart searxng` |
| `permission denied` when editing `settings.yml` | The container took ownership. Edit with `sudo`, or recreate the container with `-e FORCE_OWNERSHIP=false` |
| Firecrawl API container exits; logs show `EAI_AGAIN nuq-postgres` | It was started without its database. Run `docker compose up -d` for the whole stack |
| Every source is `unverified` | The run used LLM search: `SEARXNG_URL` is `off` |
| `every search failed (rate-limited)` | Every instance refused. With `auto`, public instances limit bursts; retry later, or run your own SearXNG |
| Every source is `snippet only` | Search works, scraping does not. Check Firecrawl with the curl in section 2; if hosted, check `FIRECRAWL_API_KEY` |
| Sources reported as `blocked` | The target resolved to a private or loopback address and was refused before the request. See [the trust boundary](configuration.md#scraping-trust-boundary) |
| Settings in `.env` appear to be ignored | Variables already exported in your shell take precedence — `.env` does not overwrite them. Run `env \| grep -E 'OPENAI\|SEARXNG\|FIRECRAWL'` and unset what you do not want |
