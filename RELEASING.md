# Releasing

Cutting a release is a changelog commit followed by an annotated tag. The tag
push triggers CI to draft a GitHub release with binaries attached
(GoReleaser); a human then publishes it.

## 1. Close the changelog entry

In `CHANGELOG.md`, rename the `## [Unreleased]` section to
`## [X.Y.Z] - YYYY-MM-DD` and add a fresh empty `## [Unreleased]` section
above it. Pick the next semver number (`0.MINOR.0` for a release with new
features, `0.x.PATCH` for fixes only — the project is pre-1.0).

## 2. Commit and tag

```sh
git add -A
git commit -m "Release vX.Y.Z"
git tag -a vX.Y.Z -m "Release vX.Y.Z"
```

## 3. Push

```sh
git push && git push --tags
```

Pushing the tag runs the `release` job in `.github/workflows/ci.yml`, which
builds the binaries with GoReleaser (`.goreleaser.yml`) and drafts a release
with archives and `deep-research_checksums.txt` attached:

- `deep-research_linux_amd64.tar.gz`
- `deep-research_linux_arm64.tar.gz`
- `deep-research_darwin_amd64.tar.gz`
- `deep-research_darwin_arm64.tar.gz`
- `deep-research_windows_amd64.zip`
- `deep-research_windows_arm64.zip`

The release job runs only after `test` and `lint` pass, and the `test` job
cross-compiles every one of those targets — a platform that does not build is
a red CI run, not a half-empty release.

Ordinary `main` pushes do not trigger a release — the job only fires on
`refs/tags/v*` (see the `if: startsWith(github.ref, 'refs/tags/')` guard).

## 4. Publish on GitHub

Once CI is green, open the draft release at
<https://github.com/juanhuttemann/deep-research/releases>. Edit the body
(paste the changelog entry for `X.Y.Z`), then mark it as a non-draft release
to publish.

The one-liner installers (`scripts/install.sh`, `scripts/install.ps1`) and
the `releases/latest/download/...` URLs only see the release after it is
published.
