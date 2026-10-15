# `--jsonl` events

One JSON object per line on stdout, nothing else on stdout; warnings go to
stderr. Every field except `type` and `time` is optional. The full struct is
`Event` in `internal/ui/events.go`.

## Types

| `type` | When | Fields worth reading |
| --- | --- | --- |
| `source` | `--domains` only, one per site, before planning | `check`: `domain`, `status` (`ok`, `snippet-only`, `unindexed`, `unreadable`, `search-failed`), `pages`, `sample`, `chars`, `detail` |
| `plan` | once, before the first search | `plan`: the plan about to run, the same shape `--plan` reads |
| `phase` | a pipeline phase starts | `phase`, `detail` |
| `subagent` | a sub-agent changes state | `sub_id`, `sub_name`, `sub_state` (`running`, `queued`, `done`, `error`), `progress`, `line` |
| `search` | a sub-agent issues a query | `query`, `terms`, `sub_id` |
| `read` | a page was fetched | `url`, `domain` |
| `verify` | a source's availability | `url`, `status` (`ok`, `degraded`, `dropped`), `code` |
| `citation` | a distinct source joins the run | `source_title`, `source_url`, `sources` (running count) |
| `token` | running token total | `tokens` |
| `info` | progress, retries, which model is asked | `detail`; `transient: true` replaces the previous status line |
| `error` | a failure, fatal or not | `detail` |
| `report` | the report is written | |
| `detach` | the display was released | |
| `done` | last line of every run | `status`, `detail`, `artifacts` |

## Reading progress

- `info` lines with `transient: true` arrive at least every five seconds
  while a streamed model call (planning, analysis, fact-check, report) is
  running. For a reasoning model, `detail` is the status line, a newline,
  then the latest of its reasoning.
- Search, scraping and LLM-search calls have no heartbeat and can be silent
  until their per-call timeout (doubled on retry).
- `done` is written after the report files and history record. Validation
  errors (empty question, bad `--mode`, no key) also end on `done` with
  `status: failed`.
