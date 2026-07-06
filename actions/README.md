# Hooversion GitHub Actions

This directory contains composite actions for using Hooversion from other
repositories.

## Setup CLI

```yaml
- uses: openhoo/hooversion/actions/setup@v0.2.0
  with:
    version: 0.2.0
- run: hooversion plan
```

## Lint Commits

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0
- uses: openhoo/hooversion/actions/lint@v0.2.0
  with:
    version: 0.2.0
```

## Release

```yaml
- uses: actions/checkout@v6
  with:
    fetch-depth: 0
- id: release
  uses: openhoo/hooversion/actions/release@v0.2.0
  with:
    version: 0.2.0
    github-token: ${{ secrets.GITHUB_TOKEN }}
```

Use `steps.release.outputs.published`, `version`, `tag`, and `releases-json`
to gate downstream package, Docker, or archive publishing jobs. In repositories
where `main` requires pull requests, allow GitHub Actions to bypass release-only
branch protections so Hooversion can keep releases automatic while human changes
remain pull-request gated.
