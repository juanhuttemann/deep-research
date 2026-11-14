# Investigating research quality

Use the existing Go checker evaluation to test model judgments on fixed,
reviewed evidence. It is a maintainer command, separate from normal research
and `make verify`. Live evaluations spend provider requests; they do not
search the web.

## Reproduce the excerpt regression without a model

```sh
go test ./internal/tools -run '^TestExcerpt(SelectsPublicRegistryRowsForClaims|SharesBudgetBetweenLongTableAndProse|RegistryRowsWithReferences)$' -v
```

This uses the frozen public IANA HTTP status-code registry. It checks that
requested rows and the column header survive beside other relevant passages
within the excerpt limit; it
requires no provider key, model requests or network. Scope and budget
counterexamples run with `go test ./internal/tools`. Offline evaluation helper
tests run with `make eval-test` and are included in `make verify`.

## Run and inspect

```sh
# Focus on one committed case first; repeat model judgments to expose variance.
EVAL_CASE=units EVAL_REPEAT=3 EVAL_OUTPUT=/absolute/path/before.json make eval-replay
# After a change, use the same cases, labels and model settings.
EVAL_CASE=units EVAL_REPEAT=3 EVAL_OUTPUT=/absolute/path/after.json EVAL_BASELINE=/absolute/path/before.json make eval-replay
# Check all current expectations; fail on mistakes or provider errors.
EVAL_STRICT=1 make eval-replay
```

Choose separate output paths. JSON contains frozen cases and expected labels;
exact user prompts (`runs[].trace.prompts`), with system instructions hashed;
per-call parsed checker responses captured before merge/validation
(`runs[].trace.calls[].response`); governed outcomes (`governed_analysis`);
per-ID results; model, reasoning effort and prompt hash; HEAD and whether the
working tree was dirty; a source SHA-256 including untracked source inputs;
provider-reported tokens and elapsed time. New output files use private
permissions. Do not commit generated reports or secrets.
A fingerprint identifies source inputs but is not an archive: record the
reviewed diff with results.

`not_supported` means the final claim must not be accepted as supported; it
does not require a particular rejection category. Provider errors remain
errors. `EVAL_STRICT=1` also fails on incorrect raw inference judgments even
when a failed premise prevented the conclusion from being approved.

Comparisons aggregate correct judgments as k/n for each case/ID; individual
repetition numbers do not pair independent model samples. Fewer than three
samples are insufficient. A 0/n to n/n change (n ≥ 3 in both variants) is
reported as consistent **on that case**, not statistical significance. Mixed
outcomes are unstable. Unchanged passes and failures are explicit. Changed
models, prompts or effort are noted; do not attribute their effects to code.
Different evidence or labels require both variants to be rerun. Tokens are
not dollar estimates, and timings vary with provider load and reasoning.

## Find the responsible layer

1. Read the expected label and source context. If relevant evidence was never
   retrieved, investigate search and extraction; a checker fixture cannot
   measure coverage of the open web.
2. Compare the full source with the **recorded prompt**. If the needed passage
   is missing from the input, reproduce that omission with a deterministic
   excerpt test. No paid model calls are needed to measure passage retention.
3. If the passage reached the model, inspect its verdict and supporting quote.
   An incorrect semantic judgment points to instructions/model behavior;
   a malformed quote or a difference between raw and final outcomes points
   to validation or governance. Inspect dependencies before changing code.
4. If checking is sound but final prose misstates the evidence, investigate
   summarization. This harness does not evaluate final-report quality.

## Make and verify a correction

Use a public, reproducible source for new failures and record its URL, frozen
text and label rationale. Put checker cases in `internal/ui/testdata/checker`;
use supported and unsupported controls. A model-generated label is a lead,
not an expected answer. Never change a label to make a test pass; justify a
correction from the source and rerun both variants.

First reproduce the defect against the current code. For deterministic code
errors, add a failing regression and counterexamples before editing production
code. Keep the correction in the responsible layer. Verify the failing case
and the existing regressions, then run `make verify`. For model-judgment
changes, compare repeated runs and inspect every regression and unresolved
failure. Report actual counts, model/settings, errors, token/time observations
and limitations. A small development corpus is not general accuracy.

Known-case regression tests do not discover unknown failures. Review new
completed runs and final reports separately, confirm suspected problems
against their sources, and turn confirmed public examples into committed
cases. Keep private material and personal run details out of project docs,
fixtures and agent instructions.

## Verify the finished report

Checker accuracy is an intermediate measure. Before claiming a research-quality
improvement, compare final reports produced by the full analysis, fact-check
and summarization flow. Use the existing trace/replay commands, not a second
pipeline implementation.

Choose a public question and write its answer criteria from authoritative
sources **before** reading generated answers. Include required facts, source
support, material errors and unsupported advice; distinguish an unanswered
requirement from a false statement. Add a held-out question and an ordinary
prose control. Fix the rule for what would count as an improvement in advance.

Set `QUESTION`, `CAPTURE_DIR`, `TRACE`, `BASELINE_BIN`, `CANDIDATE_BIN`,
`BEFORE_DIR` and `AFTER_DIR` to your own public task, binaries and output paths:

```sh
drsh -p "$QUESTION" --trace --no-follow-ups --silent --reports "$CAPTURE_DIR"
"$BASELINE_BIN" --replay "$TRACE" --trace --silent --reports "$BEFORE_DIR"
"$CANDIDATE_BIN" --replay "$TRACE" --trace --silent --reports "$AFTER_DIR"
```

`TRACE` is the recorded `.trace.json` from the capture. Verify that it includes
the intended source material; record exclusions by an advance evidence rule,
not by which report scores better. Build parity matters: hold all other code,
model settings and report settings fixed. Replays must use the same trace and
must not fetch new evidence. Both variants incur live model usage.

Repeat into separate directories, alternating variants. Inspect the earliest
phase the change affects, using deterministic tests on identical inputs to
confirm the mechanism. If model inputs are identical throughout, differences
in reports are not evidence of a code improvement. Review final Markdown blind
to the variant where practical, checking citations against the actual fetched
pages, not just model memory. Report required-fact coverage, hedging, errors,
citation support, per-run results, tokens and time. Three repetitions on one
question are still a targeted regression experiment, not general accuracy.
