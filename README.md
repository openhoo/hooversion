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

## Config

Run `hooversion init` to generate `hooversion.config.ts`. See `examples/` for a
single-package Node/Bun setup and an independent Rust workspace setup.
Packages can use `node`, `rust`, `python`, or `version-file` manifests. The
`version-file` type reads and writes a plain text file containing only the
semantic version, which fits container or Go repos that already keep release
versions in files such as `transports/version`.

The release command updates manifests and changelogs, runs configured hooks,
creates a release commit and tags, pushes them by default, creates GitHub
Releases with `GITHUB_TOKEN`, and writes CI outputs to `.hooversion/outputs.json`.
For a single release it also writes `.release-version` for compatibility with
existing workflows. In protected repositories that allow GitHub Actions to
bypass release-only branch protections, this keeps releases automatic while
human changes to `main` remain pull-request gated.

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
      - uses: openhoo/hooversion/actions/setup@v0.1.1
        with:
          version: 0.1.1
      - run: hooversion plan

  commitlint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v6
        with:
          fetch-depth: 0
      - uses: openhoo/hooversion/actions/lint@v0.1.1
        with:
          version: 0.1.1

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
        uses: openhoo/hooversion/actions/release@v0.1.1
        with:
          version: 0.1.1
          github-token: ${{ secrets.GITHUB_TOKEN }}
```

`actions/lint` automatically uses the PR base/head range on pull requests and
`--last` on pushes. `actions/release` exposes `published`, `version`, `tag`, and
`releases-json` outputs for downstream package, Docker, or archive publishing
jobs.
