# Release verification

`hooversion verify-release` checks a release after publication. It performs no
release mutation and emits no VSA until every selected policy check passes.

## Baseline

```sh
hooversion verify-release \
  --repository openhoo/hooversion \
  --tag v1.0.7
```

Without `--tag`, GitHub's latest release is selected. Inside a recognized
GitHub checkout, `--repository` defaults to `remote.origin.url`. Set `GH_TOKEN`
or `GITHUB_TOKEN` for authenticated API limits; `gh auth` credentials are used
by `gh attestation verify` but are not silently extracted for direct API calls.

Baseline verification:

- rejects drafts, duplicate/unsafe/incomplete assets, invalid asset state or
  size, and tag-resolution ambiguity;
- resolves lightweight or nested annotated tags to the exact commit;
- downloads every payload plus `SHA256SUMS` through the GitHub API;
- requires exact set equality between payload assets and checksum entries;
- checks downloaded bytes against both `SHA256SUMS` and GitHub's asset digest
  when GitHub supplies one;
- limits each asset to 256 MiB, all selected downloads to 1 GiB, API JSON to
  32 MiB, and checksum input to 1 MiB.

## Strict OpenHoo policy

```sh
repo=openhoo/hooversion
tag=v1.0.7
hooversion verify-release \
  --repository "$repo" \
  --tag "$tag" \
  --require-sbom \
  --require-license \
  --require-signatures \
  --signature-identity "https://github.com/${repo}/.github/workflows/release.yml@refs/heads/main" \
  --signature-issuer https://token.actions.githubusercontent.com \
  --require-attestations \
  --signer-workflow "${repo}/.github/workflows/release.yml" \
  --source-ref refs/heads/main \
  --output "${tag}.vsa.intoto.json"
```

`--require-sbom` validates the core SPDX 2.2/2.3 or CycloneDX 1.x document
structure instead of trusting a filename. SBOM JSON is limited to 64 MiB and
must contain exactly one value.

`--require-signatures` invokes fixed `cosign verify-blob` arguments for every
payload and `SHA256SUMS`; `cosign` must be installed. `--require-attestations`
invokes fixed `gh attestation verify` arguments bound to repository, release-tag
commit, SLSA provenance predicate, signer workflow, and optional source ref;
`gh` must be installed and authenticated. Both commands have a two-minute
per-invocation timeout and 1 MiB output limit.

Certificate identity and source ref must match the actual release workflow
invocation. A branch-triggered run commonly uses `refs/heads/main`; a workflow
run from the immutable release tag uses `refs/tags/<tag>`. The verifier never
widens one into a pattern.

`--require-license` checks every `.tar.gz`, `.tgz`, `.zip`, and `.nupkg` for a
regular non-empty `LICENSE` file. `--require-signed-tag` is separate because a
valid artifact signature does not imply a Git tag signature, and existing
OpenHoo release tags may be unsigned annotated tags.

## VSA and Hoolicy

The output is an in-toto Statement with SLSA Verification Summary Attestation
predicate type `https://slsa.dev/verification_summary/v1`. Subjects contain the
verified payloads and checksum file. The predicate records verifier identity,
verification time, release URL, effective-policy digest, `PASSED`, and an
OpenHoo URI-namespaced extension with repository, tag commit, release ID, and
check counts. `verifiedLevels` is empty: checksum/signature/provenance checks do
not justify claiming an unverified SLSA build level.

Hoolicy consumes the file as external `provenance` evidence and recognizes VSA
`verifier.id`, `timeVerified`, subjects, and `verificationResult`:

```yaml
version: 1
external:
  - id: published-release
    type: provenance
    path: evidence/v1.0.7.vsa.intoto.json
    sha256: sha256:<digest-of-vsa-file>
    subjectDigest: sha256:<one-verified-release-subject>
    requiredProducer: https://openhoo.dev/hooversion/verify-release/v1
    maximumAge: 168h
    minimumItems: 1
    maximumFailures: 0
```

Hoolicy's `sha256` field pins the VSA bytes. A VSA with
`verificationResult: FAILED`, a wrong producer, stale timestamp, missing subject,
or changed file digest fails closed.
