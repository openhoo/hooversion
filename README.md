# Hooversion

Hooversion is a Bun/TypeScript CLI for Conventional Commit linting and semantic
release automation. It is designed to replace separate `commitlint`,
`semantic-release`, changelog, release-note, tag, and GitHub release glue.

## Commands

```sh
hooversion init
hooversion lint --from origin/main --to HEAD
hooversion plan
hooversion release --dry-run
hooversion release
hooversion doctor
hooversion app
versionhoo-app
```

## Release Model

- `feat` releases a minor version.
- `fix` and `perf` release a patch version.
- `!` or `BREAKING CHANGE:` releases a major version.
- Release commits and merge/revert noise are ignored.
- Single-package repos use `v${version}` tags.
- Independent multi-package repos use `${name}@v${version}` tags.
- Commits route to packages through changed paths and Conventional Commit scopes.
- Local dependents can release automatically through `dependencies` in config.

## Commit-policy API

The packaged `@openhoo/hooversion` API is the supported Hootway-style commit-policy
parity surface. Use it for commit parsing and linting rather than importing
internal modules:

```ts
import { lintCommit, lintCommits, parseCommit, type CommitPolicy } from "@openhoo/hooversion";

const policy: CommitPolicy = {
  allowedTypes: ["feat", "fix", "docs"],
  releaseTypes: { docs: "patch" },
};
const commit = {
  hash: "abc123",
  subject: "docs: explain the public policy",
  body: "",
  files: [],
};

const issues = lintCommit(commit, policy);
const batchIssues = lintCommits([commit], policy);
const parsed = parseCommit(commit, policy);
```

`allowedTypes` limits accepted Conventional Commit types; `releaseTypes` extends
or overrides the release-type mapping used by `parseCommit`. `lintCommit`,
`lintCommits`, and `parseCommit` all accept the same optional `CommitPolicy`.


## Config and initialization

Run `hooversion init` to generate `hooversion.config.ts`. See `examples/` for a
single-package Node/Bun setup and an independent Rust workspace setup.
Packages can use `node`, `rust`, `python`, or `version-file` manifests. The
`version-file` type reads and writes a plain text file containing only the
semantic version, which fits container or Go repos that already keep release
versions in files such as `transports/version`.

`init` refuses to overwrite an existing configuration. `--force` can replace
one existing configuration but refuses duplicate config files; it overwrites
workflows only when they are recognized as Hooversion-generated. Use
`--no-workflow` to generate only the config.

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

Release outputs are managed files. Each non-dry-run release clears prior
generated output, writes `.hooversion/outputs.json` and per-tag notes, and
writes `.release-version` only for a single release (removing it otherwise).
These paths are generated release state, not user-owned files. Corresponding CI
outputs are written to `GITHUB_OUTPUT` when available. In protected repositories
that allow GitHub Actions to bypass release-only branch protections, this keeps
releases automatic while human changes to `main` remain pull-request gated.


## GitHub Actions

Hooversion ships composite actions for downstream CI:

```yaml
jobs:
  plan:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: openhoo/hooversion/actions/setup@v0.2.0
        with:
          version: 0.2.0
      - run: hooversion plan

  commitlint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: openhoo/hooversion/actions/lint@v0.2.0
        with:
          version: 0.2.0

  release:
    runs-on: ubuntu-latest
    if: github.event.workflow_run.conclusion == 'success'
    permissions:
      contents: write
      pull-requests: write
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - id: release
        uses: openhoo/hooversion/actions/release@v0.2.0
        with:
          version: 0.2.0
          github-token: ${{ secrets.GITHUB_TOKEN }}
```

`actions/lint` automatically uses the PR base/head range on pull requests and
`--last` on pushes. `actions/release` exposes `published`, `version`, `tag`, and
`releases-json` outputs for downstream package, Docker, or archive publishing
jobs.

The generated release workflow checks out the successful workflow run's immutable
`head_sha` and uses repository-scoped concurrency with
`cancel-in-progress: false`, so a later release never cancels an already-running
release.

## Real GitHub App

Hooversion also ships `versionhoo-app`, a Bun server for running releases as an
installed GitHub App. It verifies GitHub webhook signatures, mints installation
tokens from the app private key, clones repositories, runs the same release
engine, pushes release commits and tags, creates GitHub Releases, and reports a
`Versionhoo Release` check back to GitHub.

Use this when `main` is pull-request gated for humans but a dedicated app should
be allowed to perform release-only writes. See `docs/github-app.md` for the app
permissions, webhook settings, ruleset bypass setup, and runtime environment.
