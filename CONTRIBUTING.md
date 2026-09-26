# Contributing

## The gate

```bash
make verify
```

`verify` is fmt, vet, gocyclo (<15), ineffassign, golangci-lint, deadcode,
`go test -race ./...`, and a check that no `.go` file is gitignored. Run it
before considering a change done.

A lint binary that is not installed is reported and skipped rather than
failing the build with `command not found`; `make tools` installs all four
into `$(go env GOPATH)/bin`.

Other targets:

```bash
make build                  # stamps the version into the deep-research binary
make test                   # go test -race ./...
go test -race ./internal/ui # a single package
make lint                   # fmt vet cyclo ineffassign golangci deadcode
```

`.github/workflows/ci.yml` runs the same checks on every push and pull
request, plus two things `make verify` cannot do locally: it cross-compiles
all six released targets, and it smoke-tests the built binary's startup
and history path.

If you touch the platform-split UI files, cross-compile before pushing —
`//go:build unix` includes darwin, and a Linux-only constant there breaks
every macOS build while `go build` stays green:

```bash
GOOS=darwin GOARCH=arm64 go build -o /dev/null ./...
GOOS=windows GOARCH=amd64 go build -o /dev/null ./...
```

## Code style

- Standard library `testing` only. No testify, no external test frameworks.
  Use `t.Fatalf` / `t.Errorf`, `t.Helper()`, and `httptest` servers for HTTP
  clients, matching the existing test doubles:

  ```go
  func newSearXNGServer(t *testing.T, results []map[string]any) *httptest.Server {
      t.Helper()
      server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
          w.Header().Set("Content-Type", "application/json")
          json.NewEncoder(w).Encode(map[string]any{"results": results})
      }))
      t.Cleanup(server.Close)
      return server
  }
  ```

- Test files: `<pkg>_test.go` holds the unit tests; `regression_test.go` holds
  the suite that pins behaviour a shipped bug got wrong, each test naming the
  failure it prevents.
- Keep functions short — the gocyclo ceiling of 15 is enforced by
  `make verify`.
- Comments explain *why*, not *what*. Non-obvious decisions (timeout
  doubling, hash-suffixed filenames, deprecations) are documented inline.

## Architecture rules

See [docs/architecture.md](docs/architecture.md) for the full picture. The
rules a change has to respect:

- `internal/ui`'s `Driver` is the only thing that sequences the pipeline.
  `internal/agent` talks to one model and parses its answers; don't move
  sequencing into it.
- Each assistant phase is a single model call — no multi-turn loops inside an
  agent method.
- Layering is one-way: `cli` → {agent, config, store, ui}, `agent` → `tools`,
  `tools` standalone. Never import upward.
- New config keys must work through all three channels: environment,
  `config.yaml`, embedded defaults.
- Both platform variants of a split file must compile.
- Changing `store.ResearchResult` fields must keep legacy JSONL records
  loading; test both schemas.

## Commits

Imperative, sentence-case messages describing the change and its reason. No
conventional-commit prefixes. One commit per review/fix pass, with a body
explaining each finding and why it mattered, is the established shape here.

User-visible changes get a `CHANGELOG.md` entry under `## [Unreleased]` in
the same commit; [RELEASING.md](RELEASING.md) covers cutting a release.

Never commit `.env` or anything matching `.gitignore`. If you add a gitignore
pattern for a directory of Go code, anchor it with a leading `/` — otherwise
`make verify` fails on hidden `.go` files.
