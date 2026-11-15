# Releasing

Cutting a release is a changelog commit followed by an annotated tag. The tag
push triggers CI to draft a GitHub release with binaries attached
(GoReleaser); a human then publishes it.

## 1. Close the changelog entry

In `CHANGELOG.md`, rename the `## [Unreleased]` section to
`## [X.Y.Z] - YYYY-MM-DD` and add a fresh empty `## [Unreleased]` section
above it. Pick the next semver number (`0.MINOR.0` for a release with new
features, `0.x.PATCH` for fixes only — the project is pre-1.0).

Then, in `integrations/pi/package.json`, bump `version` when the extension
changed since its last publish.

## 2. Commit and push to main

```sh
git add -A
git commit -m "Release vX.Y.Z"
git push
```

Wait for that commit's CI run on `main` to pass (`gh run watch`). The tag
does not test again: its release job requires a successful run of the CI
workflow on `main` for the tagged commit, and fails without one.

## 3. Tag and push the tag

```sh
git tag -a vX.Y.Z -m "Release vX.Y.Z"
git push --tags
```

Pushing the tag runs the `release` job in `.github/workflows/ci.yml`, which
checks that `main`'s CI passed for the commit, builds the binaries with
GoReleaser (`.goreleaser.yml`) and drafts a release with archives and
`deep-research_checksums.txt` attached:

- `deep-research_linux_amd64.tar.gz`
- `deep-research_linux_arm64.tar.gz`
- `deep-research_darwin_amd64.tar.gz`
- `deep-research_darwin_arm64.tar.gz`
- `deep-research_windows_amd64.zip`
- `deep-research_windows_arm64.zip`

The tag's run skips `test`, `lint` and `pi`: they ran on `main` for the same
commit, and the release job builds only once that run succeeded. The `test`
job cross-compiles every one of those targets, so a platform that does not
build is a red run on `main`, not a half-empty release. If the tag was pushed
before `main`'s run finished, the release job fails saying so; re-run it once
`main` is green (`gh run rerun <id> --failed`).

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

## 5. Publish the Pi extension

When its version was bumped, publish it to npm from a clean checkout of the
tag. It is `@juanhuttemann/pi-deep-research`, so it needs the npm account
`juanhuttemann` (`npm login`; `corepack npm@10` stands in for a missing
npm). Being scoped, it would publish as private; `publishConfig.access` in
its package.json makes it public:

```sh
cd integrations/pi
npm ci && npm run typecheck && npm test
npm publish
```

Publish only after the GitHub release. The extension tells users to run
`drsh update` when their drsh lacks `drsh settings`, and that update has to
find the release.
