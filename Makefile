.PHONY: catalog test e2e eval-replay build run fmt fmt-check vet cyclo ineffassign golangci deadcode lint verify tools

# -race is part of the gate, not an extra step: the tools and driver both fan
# work out across goroutines, and a race that only `go test -race` sees is a
# race the shipped verify target used to miss entirely.
test:
	go test -race ./...

# The e2e suite runs the built binary against the real provider and search.
# It is not part of verify: a run spends about four model requests, which
# free models cap per day, and public SearXNG can refuse it on any given day.
# Needs OPENAI_API_KEY; without one only the rejected-key test runs.
e2e:
	go test -tags e2e -count=1 -timeout 15m -v ./cmd/drsh/

# The checker eval runs the live fact-check and the governance code on
# frozen analyses whose right outcome is known (internal/ui/testdata/checker)
# and reports false approvals, false blocks, false accepts and false rejects.
# It spends a model request per fact-check call (two for most cases, one call
# per claim share and per inference share), times EVAL_REPEAT (default 1), so
# it is not part of verify. Needs the model configured as for a run.
# EVAL_CASE=name runs only the cases whose name contains it.
eval-replay:
	go test -tags eval -count=1 -timeout 30m -run TestEvalChecker -v ./internal/ui/

# The provider/model list `drsh login` shows offline: Catwalk's
# catalog (MIT), kept to the provider types deep-research has a client for and
# the fields it reads. The binary refreshes it at login when online; this
# updates the copy built in. Not part of verify: it needs the network.
CATALOG_FILTER = [.[] | select((.type | IN("openai","openai-compat","openrouter","vercel","anthropic")) and .api_key != null) | {id, name, type, api_endpoint, api_key, default_large_model_id, models: [.models[] | {id, name, cost_per_1m_in, cost_per_1m_out, context_window}]}]

# One shell line with each step checked: piped, a failed curl still let jq
# exit 0 on empty input and replace the snapshot with an empty file.
catalog:
	curl -fsSL https://catwalk.charm.land/v2/providers -o internal/catalog/catwalk.raw.tmp && \
	jq -ce '$(CATALOG_FILTER) | select(length > 0)' internal/catalog/catwalk.raw.tmp > internal/catalog/catwalk.json.tmp && \
	test -s internal/catalog/catwalk.json.tmp && \
	mv internal/catalog/catwalk.json.tmp internal/catalog/catwalk.json; \
	status=$$?; rm -f internal/catalog/catwalk.raw.tmp internal/catalog/catwalk.json.tmp; exit $$status

# VERSION is stamped into the binary so --version reports the build rather
# than the hardcoded "dev" it used to print for every release.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o drsh ./cmd/drsh

run:
	go run ./cmd/drsh

fmt:
	gofmt -l -w .

fmt-check:
	@files=$$(gofmt -l .) || exit 1; \
	if [ -n "$$files" ]; then echo "$$files"; exit 1; fi

vet:
	go vet ./...

# The lint tools are not vendored, so a fresh checkout has none of them. A
# missing binary used to fail `make verify` with "command not found" and no
# hint, which made the advertised gate look broken rather than uninstalled.
# Each check now says how to install what it needs and keeps going, while a
# tool that is present still fails the build when it finds something: a
# warning is an honest "not checked", an abort would report nothing at all.
# $(1) binary, $(2) the check to run, $(3) how to install it.
define run_tool
@if command -v $(1) >/dev/null 2>&1; then $(2); \
	else echo "warning: $(1) not installed — skipping (make tools, or: go install $(3))"; fi
endef

TOOLS = \
	github.com/fzipp/gocyclo/cmd/gocyclo@latest \
	github.com/gordonklaus/ineffassign@latest \
	github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest \
	golang.org/x/tools/cmd/deadcode@latest

# Installs every binary lint needs into $(shell go env GOPATH)/bin.
tools:
	@for t in $(TOOLS); do echo "installing $$t"; go install $$t || exit 1; done

cyclo:
	$(call run_tool,gocyclo,gocyclo -over 15 .,github.com/fzipp/gocyclo/cmd/gocyclo@latest)

ineffassign:
	$(call run_tool,ineffassign,ineffassign ./...,github.com/gordonklaus/ineffassign@latest)

golangci:
	$(call run_tool,golangci-lint,golangci-lint run,github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest)

# deadcode exits 0 even when it finds unreachable functions, so the gate has
# to look at the output. Without this an entire dead package sat in the tree
# with `make verify` green.
deadcode:
	@if ! command -v deadcode >/dev/null 2>&1; then \
		echo "warning: deadcode not installed — skipping (make tools, or: go install golang.org/x/tools/cmd/deadcode@latest)"; \
		exit 0; fi; \
	out=$$(deadcode ./...); \
	if [ -n "$$out" ]; then echo "unreachable code:"; echo "$$out"; exit 1; fi

# Fails if any .go file is gitignored (unanchored patterns like `standup`
# once swallowed cmd/standup/ and CI checked out a tree with no entrypoint).
ignored-go:
	@ignored=$$(git ls-files --others --ignored --exclude-standard | grep '\.go$$' || true); \
	if [ -n "$$ignored" ]; then \
		echo "gitignored .go files:"; echo "$$ignored"; \
		echo "anchor the .gitignore pattern (e.g. /drsh, not drsh)"; exit 1; fi

lint: fmt-check vet cyclo ineffassign golangci deadcode

verify: lint test ignored-go
