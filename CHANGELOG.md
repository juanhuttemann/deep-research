# Changelog

All notable changes to this project are documented here. The format is based
on Keep a Changelog, and this project adheres to Semantic Versioning.

## [Unreleased]

## [0.1.0] - 2026-09-20

### Fixed

- The binary builds on macOS again. `internal/ui/input_unix.go` is tagged
  `//go:build unix`, which includes darwin, but reached for `unix.TCGETS` and
  `unix.TCSETSF` — Linux-only ioctl request numbers. Every Linux build stayed
  green while no macOS build compiled at all. The requests now come from
  per-family files (`ioctl_linux.go`, `ioctl_bsd.go`, which uses darwin's
  `TIOCGETA`/`TIOCSETAF`), and CI cross-compiles all six released targets so
  a compile-only break on a platform nobody develops on fails the run.

### Added

- `docs/services.md`: setup instructions for SearXNG and Firecrawl, the two
  services web-search mode needs. Previously the docs said web search
  required them but not how to run them. Covers enabling SearXNG's JSON API,
  which is off by default and which this CLI requires, and a verification
  command for each service.
- CI on GitHub Actions: fmt, vet, `go test -race`, a cross-compile of every
  release target, a smoke test that runs the real binary through an offline
  research run and all three exporters, golangci-lint, gocyclo and deadcode.
- Tagged releases draft automatically via GoReleaser, with archives and a
  checksums file for linux/darwin/windows on amd64 and arm64.
- One-line installers for Linux, macOS, WSL2 and Termux
  (`scripts/install.sh`) and Windows (`scripts/install.ps1`), both verifying
  the release checksum before installing.
- `CONTRIBUTING.md`, `RELEASING.md`, this changelog, an MIT `LICENSE`, and
  `.env.example`.

### Changed

- Documentation split for a public repo: the README is now what it is for —
  what the tool does, install, quick start, the research modes and the
  handful of settings most runs need. The reference material moved to
  `docs/usage.md`, `docs/configuration.md` and `docs/architecture.md`.

[Unreleased]: https://github.com/juanhuttemann/deep-research/compare/v0.1.0...HEAD
[0.1.0]: https://github.com/juanhuttemann/deep-research/releases/tag/v0.1.0
