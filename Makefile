.PHONY: test build run fmt vet cyclo ineffassign golangci deadcode lint verify tools

# -race is part of the gate, not an extra step: the tools and driver both fan
# work out across goroutines, and a race that only `go test -race` sees is a
# race the shipped verify target used to miss entirely.
test:
	go test -race ./...

# VERSION is stamped into the binary so --version reports the build rather
# than the hardcoded "dev" it used to print for every release.
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)

build:
	go build -ldflags "-X main.version=$(VERSION)" -o deep-research ./cmd/deep-research

run:
	go run ./cmd/deep-research

fmt:
	gofmt -l -w .

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
		echo "anchor the .gitignore pattern (e.g. /deep-research, not deep-research)"; exit 1; fi

lint: fmt vet cyclo ineffassign golangci deadcode

verify: lint test ignored-go
