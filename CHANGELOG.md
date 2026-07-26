# @openhoo/hooversion Changelog

## Unreleased

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
