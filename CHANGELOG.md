# @openhoo/hooversion Changelog

## 1.0.4 (2026-08-30)

### Bug Fixes

- **actions:** harden shared Hoostack integration (6596a66)
- **actions:** resolve bundled setup from action path (165b4f9)

### Other Changes

- **app:** assert only per-key queue order (bd501d2)
- standardize Hoostack dogfood (#21) (75cbf6e)

## 1.0.3 (2026-08-30)

### Bug Fixes

- **actions:** harden shared Hoostack integration (6596a66)
- **actions:** resolve bundled setup from action path (165b4f9)

### Other Changes

- **app:** assert only per-key queue order (bd501d2)

## 1.0.2 (2026-08-30)

### Bug Fixes

- **actions:** harden shared Hoostack integration (6596a66)

## 1.0.1 (2026-08-26)

### Bug Fixes

- **release:** rebuild assets for published tags on demand (#13) (460a0bf)
- **release:** route release commits through pull requests (#14) (a81d8d1)

### Other Changes

- portable asset reads and release workflow version order (#12) (eb06918)
- sync action refs and readme pins to v1.0.0 (d45d72e)

## 1.0.0 (2026-08-26)

### Breaking Changes

- rewrite Hooversion in Go (#11) (eab25f7)
  - BREAKING: hooversion.config.{ts,mjs,js,cjs} must migrate

### Features

- harden release automation (#7) (6ede301)

### Bug Fixes

- **app:** isolate runner token (#8) (a53f902)

### Other Changes

- run commit lint on hosted runner (#9) (d3959a4)
- use hosted runners exclusively (#10) (6513f0d)

## Unreleased

### Go rewrite

- Rewrite Hooversion as a single static binary with zero runtime dependencies; commit linting, planning, release execution, doctor diagnostics, and the Versionhoo app ship as native executables.
- Replace TypeScript configuration modules with declarative YAML configuration and add `hooversion migrate` to convert legacy `hooversion.config.{ts,mjs,js,cjs}` files (assisted when bun is available, manual mapping otherwise).
- Keep release, resume, and drift guarantees identical: SHA-bound plans, local and remote drift rejection, resumable publication from verified release state, atomic tag pushes, idempotent GitHub Releases, and managed outputs.
- Distribute the CLI via `go install` and GitHub release tarballs; discontinue the npm distribution and the `@openhoo/hooversion` JS API — commit-policy parity now lives behind `hooversion lint`.
- Replace the Bun app server with the native `versionhoo-app` binary behind the same webhook contract and runner isolation.

### Source and release safety

- Bind release plans to the reviewed source SHA, reject local or remote drift, and atomically push the release commit with its tags.
- Serialize generated releases per repository; successful CI `workflow_run` releases check out the exact validated SHA, and publication resumes only from a verified release commit and tag state.

### CLI, configuration, and manifests

- Fail closed on invalid CLI, initialization, configuration, and manifest inputs; keep generated outputs managed and reject unexpected output-tree changes before a release.
- Support Node, Rust, Python, and dedicated version-file package manifests with validated version updates and local dependency synchronization.

### App hardening

- Harden GitHub App intake and execution with repository and installation isolation, bounded webhook bodies, delivery deduplication, and token- and secret-safe handling.

### Public API and packaging

- Publish typed package declarations and the supported `lintCommits`, `lintCommit`, and `parseCommit` consumer API.

### Tests

- Expand coverage for source verification, resumable publication, CLI/configuration and manifest validation, managed outputs, and App authorization boundaries.

## 0.2.0 (2026-07-06)

### Other Changes

- split ci and release workflows (#2) (892d8b7)
- skip release when token is missing (#3) (fd34eb3)
- restore automatic releases (#4) (eb6e99e)

### Features

- **hooversion:** support version-file manifests (#5) (3fe3028)

## 0.1.1 (2026-05-24)

### Bug Fixes

- **hooversion:** publish package on npm (e45bb8c)

## 0.1.0 (2026-05-24)

### Other Changes

- bootstrap hooversion (5ddbe76)

### Features

- release initial version (9528c01)

### Bug Fixes

- configure release git author (ff6aebd)
