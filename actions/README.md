# Hooversion GitHub Actions

This directory contains composite actions for using Hooversion from other
repositories. The actions download a self-contained Hooversion binary from the
GitHub release for the requested version; no JavaScript runtime is required.

## Setup CLI

```yaml
- uses: openhoo/hooversion/actions/setup@f2186561c587b58c5ea08c74c15800cdd39eab42 # v1.1.0
  with:
    version: 1.1.0
- run: hooversion plan
```

## Lint Commits

```yaml
- uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
  with:
    fetch-depth: 0
- uses: openhoo/hooversion/actions/lint@f2186561c587b58c5ea08c74c15800cdd39eab42 # v1.1.0
  with:
    version: 1.1.0
```

## Release

```yaml
permissions:
  contents: write

concurrency:
  group: release-${{ github.repository }}
  cancel-in-progress: false

steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
    with:
      fetch-depth: 0
      ref: ${{ github.event_name == 'workflow_run' && github.event.workflow_run.head_sha || github.sha }}
  - id: release
    uses: openhoo/hooversion/actions/release@f2186561c587b58c5ea08c74c15800cdd39eab42 # v1.1.0
    with:
      version: 1.1.0
      install-command: npm ci
      github-token: ${{ secrets.GITHUB_TOKEN }}
```

Generated release workflows run after a successful CI `workflow_run` and also
support `workflow_dispatch` on the release branch. They serialize releases per
repository; successful `workflow_run` releases check out the exact SHA
validated by CI. Custom workflows should use the same checkout SHA and
`release-${{ github.repository }}` concurrency group.

`published` is `true` when one or more packages were released. `version` and
`tag` are set only when exactly one package was released; `releases-json` is the
JSON array containing every package release. Gate downstream publishing on
`published`, and use `releases-json` for multi-package releases.

Hooversion verifies the source before mutation. If a prior run created the
expected release commit and tags but did not finish publication, rerunning it
resumes only that verified state and rejects remote drift.

In repositories where `main` requires pull requests, use
`actions/prepare-release`. It pushes the generated release commit to a release
branch and writes the pull-request compare URL to the job summary. A maintainer
opens and squash-merges that PR, so required checks and code scanning still gate
the release. This works when enterprise policy forbids Actions-created PRs.

```yaml
permissions:
  contents: write

steps:
  - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1 # v7.0.1
    with:
      fetch-depth: 0
  - uses: openhoo/hooversion/actions/prepare-release@<immutable-commit>
    with:
      github-token: ${{ secrets.GITHUB_TOKEN }}
```

After the squashed release commit lands on protected `main`, the repository
release workflow must create the tag on that exact commit and publish artifacts.
