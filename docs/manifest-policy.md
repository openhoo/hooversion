# Manifest compatibility and local dependency updates

Hooversion parses Cargo and Python manifests as TOML before changing them.
Only selected string values are replaced: comments, unrelated metadata, table
layout and line endings remain intact. Invalid TOML and dependency forms whose
meaning cannot be determined are rejected before that manifest is written.

## Cargo

Workspace discovery expands member patterns, including recursive `**`, applies
exclusions, and removes duplicates. Paths outside the workspace and symlinked
members are rejected. Recursive discovery is capped at 100,000 filesystem
entries and patterns at 256 path components.

Package versions inherited from `workspace.package.version` retain their
`workspace = true` declarations. Every configured inheriting package must be
released to the same next version; changing an unconfigured inheriting workspace
member is rejected. Shared workspace manifests and Cargo.lock files are included
in release staging and recovery. Historical release recovery resolves inherited
versions entirely from the selected Git revision.

Dependency aliases use their `package` field to identify the released package.
Workspace dependency inheritance, target tables, quoted keys and inline tables
are supported. Cargo.lock local entries are updated while registry entries and
source-qualified dependency identities are preserved.

## Python

Discovery includes root and nested PEP 621 projects and legacy Poetry metadata,
while skipping virtual environments. Package matching normalizes case and runs
of hyphens, underscores and dots. Requirement extras and the complete environment
marker survive an update. Dependencies, optional dependencies, dependency groups
and Poetry dependency version strings/tables are supported.

For an explicitly configured local dependency, the new released version must be
allowed by the rewritten requirement. Single `>=`, `==`, `===` and `~=` specifiers
retain their operator. Compound bounds, exclusions, strict or upper bounds and
parenthesized compound requirements become an exact `==next` pin. Positive
wildcards become an exact version. This deliberately changes range semantics
rather than creating a range that excludes the newly released package. Poetry
compound constraints become its exact-version form; simple caret and tilde
constraints retain their prefix.

Direct URL references are rejected: a package release version does not identify
a replacement URL, immutable artifact digest or VCS revision. Malformed and
unsupported constraints are also rejected rather than guessed.
