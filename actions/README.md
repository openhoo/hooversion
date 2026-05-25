# Hooversion GitHub Actions

This directory contains composite actions for using Hooversion from other
repositories.

## Setup CLI

```yaml
- uses: openhoo/hooversion/actions/setup@v0.1.1
  with:
    version: 0.1.1
- run: hooversion plan
```

## Lint Commits

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0
- uses: openhoo/hooversion/actions/lint@v0.1.1
  with:
    version: 0.1.1
```

## Release

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0
- id: release
  uses: openhoo/hooversion/actions/release@v0.1.1
  with:
    version: 0.1.1
    push: "false"
    github: "false"
    github-token: ${{ secrets.RELEASE_TOKEN }}
```

Use `steps.release.outputs.published`, `version`, `tag`, and `releases-json`
to open release PRs or gate downstream package, Docker, or archive publishing
jobs. For repositories where `main` requires pull requests, use a
`RELEASE_TOKEN` that can push the release branch and open the release PR.
