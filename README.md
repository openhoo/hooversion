# Hooversion

Hooversion is a single static binary for Conventional Commit linting and
semantic release automation. It is designed to replace separate `commitlint`,
`semantic-release`, changelog, release-note, tag, and GitHub release glue —
with no runtime dependencies: no Node, no Bun, no npm packages.

## Commands

```sh
hooversion init
hooversion lint --from origin/main --to HEAD
hooversion plan
hooversion release --dry-run
hooversion release
hooversion verify-release --repository openhoo/hooversion --tag v1.1.0
hooversion doctor
hooversion migrate
hooversion help
hooversion version
hooversion app
```

## Install

Install with Go:

```sh
go install github.com/openhoo/hooversion/cmd/hooversion@v1.1.0
```

Or download the `hooversion` prebuilt static binary from the
[GitHub Releases](https://github.com/openhoo/hooversion/releases) page and put
it on your `PATH`. Start the GitHub App server from that release binary with
`hooversion app`; release archives do not contain a standalone `versionhoo-app`
executable. The same releases power the GitHub Actions integration below.

## Quickstart

```sh
hooversion init             # writes hooversion.yaml (and optional workflows)
hooversion lint --last      # lint the most recent commit
hooversion plan             # preview the next release plan
hooversion release --dry-run
hooversion release          # bump versions, changelogs, tags, push, publish
hooversion doctor           # sanity-check config, git, tokens
```

### Command reference

- `init [--force] [--no-workflow] [--action-owner-repo <owner/repo>] [--action-ref <ref>] [--hooversion-version <version>]`
  generates `hooversion.yaml` plus optional GitHub workflows. `init` refuses to
  overwrite an existing configuration without `--force`; `--force` refuses
  duplicate config files. `--no-workflow` generates only the config.
- `lint --last | --edit <commit-msg-file> | --from <ref> [--to <ref>]`
  validates Conventional Commits; exactly one selector is required.
- `plan [--config <path>]` prints the next release plan.
- `release [--dry-run] [--no-push] [--no-github] [--config <path>]`.
- `verify-release [--repository <owner/repo>] [--tag <tag>]` independently
  downloads a published release, resolves its tag to a commit, and requires
  exact `SHA256SUMS` coverage. Strict flags verify SBOMs, embedded licenses,
  Sigstore bundles, GitHub attestations, or annotated-tag signatures. `--output`
  writes a SLSA VSA only after every selected check passes. See
  [Release verification](docs/release-verification.md).
- `doctor [--config <path>]` prints `ok:`/`warning:`/`error:` findings.
- `migrate` converts a legacy TypeScript config (see [Migration](#migration)).
- `help`, `version`.

## Configuration

Hooversion reads its configuration from the current directory: `hooversion.yaml`,
`.hooversion.yaml`, `hooversion.yml`, `.hooversion.yml`, `hooversion.config.json`,
or `hooversion.json`, in that order. An explicit path passed via `--config`
takes precedence.

| Key | Type | Default | Description |
| --- | --- | --- | --- |
| `branches` | `string[]` | `["main"]` | Release branches; every entry must be a valid Git branch name. |
| `tagFormat` | `string` | `v${version}` | Tag template for single-package repos. Placeholders: `${name}`, `${version}`. |
| `independentTagFormat` | `string` | `${name}@v${version}` | Tag template used when more than one package is configured. |
| `packages` | list | required, non-empty | One entry per releasable package. |
| `packages[].name` | `string` | manifest name | Falls back to the name read from the manifest. |
| `packages[].path` | `string` | `.` | Package root relative to the config directory. |
| `packages[].type` | `string` | required | One of `node`, `rust`, `python`, `version-file`. |
| `packages[].manifest` | `string` | per type | Defaults to `<path>/package.json` (`node`), `<path>/Cargo.toml` (`rust`), `<path>/pyproject.toml` (`python`), `<path>/version` (`version-file`). |
| `packages[].changelog` | `string` | `<path>/CHANGELOG.md` | Changelog file updated on release. |
| `packages[].scopes` | `string[]` | `[name]` | Commit scopes that route to this package (comma-separated scopes are split). |
| `packages[].dependencies` | `string[]` | `[]` | Local package names this package depends on; dependents get patch bumps when a dependency releases. |
| `packages[].assets` | `string[]` | `[]` | Files uploaded to the GitHub Release. |
| `hooks.beforeRelease` | `string[]` | `[]` | Shell commands run before any files change. |
| `hooks.afterVersion` | `string[]` | `[]` | Shell commands run after manifests/changelogs are updated. |
| `hooks.afterRelease` | `string[]` | `[]` | Shell commands run after publishing. |
| `github.enabled` | `bool` | `true` | `enabled: false` fully disables GitHub integration. |
| `github.releases` | `bool` | `true` | Create GitHub Releases during `release`. |
| `github.repository` | `string` | origin remote | `owner/repo` override when it cannot be derived from Git. |
| `github.apiUrl` | `string` | `https://api.github.com` | GitHub API base URL (GitHub Enterprise supported). |
| `outputDir` | `string` | `.hooversion` | Directory for managed release outputs. |
| `push` | `bool` | `true` | Push the release commit and tags. |

Validation is fail-closed: duplicate package names, unknown or self
`dependencies`, dependency cycles, invalid tag formats, and invalid branch or
package names are rejected before anything runs. See `examples/` for a
single-package Node setup and an independent Rust workspace setup. Packages can
use `node`, `rust`, `python`, or `version-file` manifests. The `version-file`
type reads and writes a plain text file containing only the semantic version,
which fits container or Go repos that already keep release versions in files
such as `transports/version`.

## Release Model

- `feat` releases a minor version.
- `fix` and `perf` release a patch version.
- `!` or `BREAKING CHANGE:` releases a major version.
- Release commits and merge/revert noise are ignored.
- Single-package repos use `v${version}` tags.
- Independent multi-package repos use `${name}@v${version}` tags.
- Commits route to packages through changed paths and Conventional Commit scopes.
- Local dependents can release automatically through `dependencies` in config.

Each release plan is bound to its checked-out source SHA. On the initial,
non-resumable attempt, Hooversion requires local `HEAD` and the remote release
branch to still match that SHA. It updates manifests and changelogs, runs
configured hooks, creates a release commit and tags, and pushes the branch and
all tags in one atomic Git push.

If GitHub publishing fails after that push, rerun the release from the exact
release commit and tags: Hooversion rejects remote drift, reuses only a matching
existing GitHub Release, and uploads only missing asset names. This makes the
post-push GitHub Release/asset step retryable without another branch or tag
push.

This repository's release workflow publishes cross-platform archives together
with an SPDX SBOM and sorted SHA-256 checksums. Every archive, SBOM, and checksum
file receives a keyless Sigstore bundle plus a GitHub artifact attestation.
Manual asset recovery rebuilds and reattests the same immutable tag.

Publication and verification remain separate states. `verify-release` reads the
live tag, release metadata, assets, signatures, and attestations back from
GitHub; a local build, green workflow, or existing tag alone is not accepted as
proof of the published artifact set.

Release outputs are managed files. Each non-dry-run release clears prior
generated output, writes `.hooversion/outputs.json` and per-tag notes, and
writes `.release-version` only for a single release (removing it otherwise).
When clearing older payloads, legacy `notesPath` values are removed only when
they remain contained by the configured output directory. These paths are
generated release state, not user-owned files. Corresponding CI
outputs are written to `GITHUB_OUTPUT` when available. In protected repositories
that allow GitHub Actions to bypass release-only branch protections, this keeps
releases automatic while human changes to `main` remain pull-request gated.

Webhook bodies are bounded by `VERSIONHOO_WEBHOOK_MAX_BODY_BYTES` before
signature verification and durable admission. Every validated handled
`workflow_run` is fsynced to the bounded file-backed webhook spool before the
app returns HTTP 202. The execution queue remains bounded at 64 running or
waiting jobs and serializes each repository/ref; queue saturation leaves the
durable record pending instead of returning a lossy `503`.

`VERSIONHOO_WEBHOOK_SPOOL_DIR` selects the durable backlog directory and
`VERSIONHOO_WEBHOOK_SPOOL_MAX_BYTES` bounds its total size (64 MiB by default).
Sequence-numbered records preserve per-repository/ref FIFO and are recovered
and retried after restart. Dedupe reservations remain in memory for 24 hours
and are released after final failure or an admission error, never just because
the in-memory queue is full. Corrupt, oversized, symlinked, and unsafe-path
records are quarantined or skipped without blocking later deliveries.

## Migration

```sh
hooversion migrate
```

which imports the legacy module (using `bun` when it is available), normalizes
the result, and writes `hooversion.yaml`. Without bun installed, `migrate`
prints guidance for converting manually. Manual mapping is direct: every key in
the old config has the same name in the new YAML schema (see the table above),
with one addition — `github: false` becomes:

```yaml
github:
  enabled: false
```

**JS API removed:** the npm package and its programmatic API are discontinued.
The commit-policy parity surface is now the `hooversion lint` command together
with policy behavior driven by configuration: the same allowed-type checking,
description requirements, 100-character header cap, and release-type mapping
rules apply through the CLI instead of an imported library.

## GitHub Actions

Hooversion ships composite actions for downstream CI. The actions download a
static binary from GitHub Releases using the `version` input:

```yaml
name: Release

on:
  workflow_run:
    workflows:
      - CI
    branches:
      - main
    types:
      - completed
  workflow_dispatch:

permissions:
  contents: read

concurrency:
  group: release-${{ github.repository }}
  cancel-in-progress: false

jobs:
  release:
    runs-on: ubuntu-latest
    if: >-
      (github.event_name == 'workflow_run' && github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && github.event.workflow_run.head_branch == 'main' && github.event.workflow_run.repository.full_name == github.repository) ||
      (github.event_name == 'workflow_dispatch' && github.ref == 'refs/heads/main')
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7
        with:
          fetch-depth: 0
          ref: ${{ github.event_name == 'workflow_run' && github.event.workflow_run.head_sha || github.sha }}
      - name: Prepare protected-branch release
        if: github.event_name == 'workflow_dispatch' || (github.event_name == 'workflow_run' && !startsWith(github.event.workflow_run.head_commit.message, 'chore(release):'))
        uses: openhoo/hooversion/actions/prepare-release@f2186561c587b58c5ea08c74c15800cdd39eab42 # v1.1.0
        with:
          version: 1.1.0
          install-command: bun install --frozen-lockfile
          github-token: ${{ secrets.GITHUB_TOKEN }}
      - name: Finalize protected-branch release
        id: finalize
        if: github.event_name == 'workflow_run' && startsWith(github.event.workflow_run.head_commit.message, 'chore(release):')
        uses: openhoo/hooversion/actions/release@f2186561c587b58c5ea08c74c15800cdd39eab42 # v1.1.0
        with:
          version: 1.1.0
          install-command: bun install --frozen-lockfile
          github-token: ${{ secrets.GITHUB_TOKEN }}
```

`actions/lint` automatically uses the PR base/head range on pull requests and
`--last` on pushes. `actions/release` exposes `published`, `version`, `tag`, and
`releases-json` outputs for downstream package, Docker, or archive publishing
jobs.


Generated workflows keep the immutable action commit pinned to the latest
published Hooversion action, independently of the CLI version selected by
`HOOVERSION_VERSION`. For a Node package in a subdirectory, the generator
preserves that directory for dependency installation and checks while running
both protected-release action phases from the repository root.
The generated release workflow checks out the successful workflow run's immutable
`head_sha` and uses repository-scoped concurrency with
`cancel-in-progress: false`, so a later release never cancels an already-running
release.

## Real GitHub App

Hooversion also ships `versionhoo-app` (built from `cmd/versionhoo-app`), a
native server for running releases as an installed GitHub App. The same server
is available as a Docker image. It verifies GitHub webhook signatures, mints
installation tokens from the app private key, clones repositories, runs the
same release engine, pushes release commits and tags, creates GitHub Releases,
and reports a `Versionhoo Release` check back to GitHub.

The `versionhoo-app` executable name is reserved for the app's Go and
container routes:

```sh
go install github.com/openhoo/hooversion/cmd/versionhoo-app@v1.1.0
go build -o bin/versionhoo-app ./cmd/versionhoo-app
```

The container route builds the same app from source:

```sh
docker build -t versionhoo-app .
```

Use this when `main` is pull-request gated for humans but a dedicated app should
be allowed to perform release-only writes. See `docs/github-app.md` for the app
permissions, webhook settings, ruleset bypass setup, and runtime environment.

The App runner deliberately rejects repository dependency installation and all
repository hooks. Hooversion itself has a release hook, so it must not be put
in a `versionhoo-app` allow-list; use the workflow action for this repository.
