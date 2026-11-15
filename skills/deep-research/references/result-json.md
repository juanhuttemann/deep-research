# The `.json` artifact

The structured result of one run, written beside the `.md` and `.pdf`. The
full struct is `Meta` in `internal/ui/export.go`. It can be several MB,
mostly `pages` and `timeline`; read the fields you need (for example with
`jq`) instead of loading it whole.

## Top level

| Field | Meaning |
| --- | --- |
| `question`, `timestamp`, `depth` | what was asked, when, at which `--mode` tier |
| `model`, `provider`, `served_by` | what wrote the report; `served_by` is what a router alias picked |
| `domains` | sites the run was confined to (`--domains`), if any |
| `confidence` | the report's overall confidence |
| `executive_summary`, `report` | the report text, with inline citations |
| `error` | set when a late phase failed and the report is incomplete |
| `analysis` | the analysis, below |
| `fact_check` | the verification pass, below |
| `citations` | every source: `title`, `url`, `domain`, `query`, `status` (`ok` fetched, `degraded` snippet only, `unverified` never fetched), `cited` (whether the report body references it) |
| `pages` | URL → the stored text of each fetched page |
| `passages` | every quote the fact-check gave, below |
| `topics` | per-topic findings with their own confidence |
| `sub_agent_notes`, `timeline` | how the run went; rarely needed for an answer |

## `analysis`

- `answer`, `interpretation`: the answer and how the question was read.
- `claims[]`: `id` (`c1`, `c2`, …), `claim`, `scope`, `sources`, `status`,
  `note`. `status` is what the run concluded after the fact-check; only
  `supported` lets a claim carry a recommendation. A numeric claim also
  has `value` and `unit`; one with `computed` (`m1`) states that
  computation's result and was decided by the program, not the fact-check:
  `supported` when its figure is the result, `contradicted` when it is not,
  with the arithmetic in `note`. It has no verdict in `fact_check`.
- `computations[]`: `id`, `label`, `op`, `inputs` (claim or computation
  IDs), and the program's `result` (plain digits), `unit`, `formula`.
  Only one with `valid` true may be stated as a figure; otherwise `reason`
  says why it is not established.
- `charts[]`: `id`, `title`, `kind` (`bar` or `line`), `points` (`label`,
  `ref`, `value`) and `unit`. `file` is the SVG beside the report when it
  was drawn; `reason` says why it was not.
- `recommendations[]` / `conclusions[]`: `choose` + `when`, or `statement`,
  resting on `claims`. One with `blocked` set was **not** approved
  (`failed` names the claims that did not pass): report it as not
  established.
- `conflicts[]`: `claims`, `resolution` (or `unresolved`), `why`.
- `gaps`, `follow_up`: what the run could not establish, and queries that
  would.

## `fact_check`

- `verdicts[]`: `id` (the claim), `claim`, `status`, `reason`,
  `evidence[]` (`source`, `quote`). Statuses: `supported`, `partial`,
  `disputed`, `contradicted`, `insufficient`, `unsourced`.
- `inferences[]`: whether each recommendation follows from its claims.
- `verified`, `unverified`, `contradictions`: lists derived from the
  verdicts, for the reports.

## `passages`

One per quote: `verdict` (index into `fact_check.verdicts`), `page` (key in
`pages`, empty when no fetched page matches), `quote`, `located`. `located`
proves only that the words are on that page; whether they support the claim
is the verdict's `status`.
