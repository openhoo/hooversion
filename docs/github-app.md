# Versionhoo GitHub App

Versionhoo can run as a real GitHub App instead of a repository workflow. The app
receives `workflow_run` webhooks, authenticates as the installed app, clones the
repository with an installation token, runs Hooversion, pushes release commits and
tags, and creates GitHub Releases.

## GitHub App Settings

Create a GitHub App owned by the organization and configure:

- Webhook URL: `https://<your-host>/webhooks/github`
- Webhook secret: a long random value
- Repository permissions:
  - Actions: read
  - Checks: read and write
  - Contents: read and write
  - Metadata: read
  - Pull requests: read and write
- Subscribe to events:
  - Workflow run
  - Ping

Install the app on every repository it should release.

`examples/github-app-manifest.json` contains the same permission and event
shape with placeholder URLs for GitHub's app manifest flow.

Protected `main` branches still need an explicit ruleset bypass for the GitHub
App. Add the app as a bypass actor only on the release ruleset path that permits
release commits and tags. Humans can remain pull-request gated.

## Runtime

Build and start the app:

```sh
go build -o bin/versionhoo-app ./cmd/versionhoo-app
VERSIONHOO_APP_ID=12345 \
VERSIONHOO_PRIVATE_KEY_PATH=/run/secrets/versionhoo-app.pem \
VERSIONHOO_WEBHOOK_SECRET=... \
./bin/versionhoo-app
```

`VERSIONHOO_ALLOWED_REPOS` is intentionally omitted above. If you set it, list
only repositories without Hooversion hooks; App mode rejects dependency
installation and every repository hook. In particular, do not allow-list
`openhoo/hooversion`, whose release configuration uses an `afterVersion` hook.

Alternatively install the app with Go:

```sh
go install github.com/openhoo/hooversion/cmd/versionhoo-app@v1.1.0
versionhoo-app
```

Release archives contain the app-capable `hooversion` CLI, not a standalone
`versionhoo-app`; use `hooversion app` after downloading that CLI.

Health check:

```sh
curl http://127.0.0.1:3000/health
```

Container build:

```sh
docker build -t versionhoo-app .
docker run --rm -p 3000:3000 --env-file examples/versionhoo-app.env.example \
  -v versionhoo-app-data:/var/lib/versionhoo \
  -v /secure/versionhoo-app.pem:/run/secrets/versionhoo-app.pem:ro \
  versionhoo-app
```

The named `versionhoo-app-data` volume is part of the deployment state; retain
and reuse it when replacing the container so accepted webhook records survive
restarts.

## Environment

- `VERSIONHOO_APP_ID`: GitHub App id.
- `VERSIONHOO_PRIVATE_KEY`: PEM private key content. Escaped `\n` sequences are
  accepted.
- `VERSIONHOO_PRIVATE_KEY_PATH`: path to a PEM private key. Used when
  `VERSIONHOO_PRIVATE_KEY` is not set.
- `VERSIONHOO_WEBHOOK_SECRET`: webhook HMAC secret.
- `VERSIONHOO_GITHUB_API_URL`: GitHub API URL. Defaults to
  `https://api.github.com`.
- `VERSIONHOO_TRUSTED_GITHUB_API_URLS`: comma-separated HTTPS API origins
  additionally trusted for GitHub API requests. The default
  `https://api.github.com` is always trusted; this setting is needed for a
  GitHub-compatible API origin.
- `VERSIONHOO_TRUSTED_GITHUB_CLONE_HOSTS`: comma-separated HTTPS clone
  hostnames additionally trusted alongside `github.com`. Clone URLs must still
  match the webhook repository exactly and contain no credentials, port, query,
  or fragment.
- `VERSIONHOO_HOST`: bind host. Defaults to `0.0.0.0`.
- `VERSIONHOO_PORT`: bind port. Defaults to `3000`.
- `VERSIONHOO_WEBHOOK_MAX_BODY_BYTES`: positive integer maximum webhook body
  size. Defaults to `1048576` (1 MiB); oversized declared or streamed bodies
  are rejected.
- `VERSIONHOO_WEBHOOK_SPOOL_DIR`: durable webhook backlog directory. Defaults
  to the explicit value, then `<workdir>/webhook-spool` when a workdir is
  configured, then `os.UserCacheDir()/versionhoo/webhook-spool`, then
  `$HOME/.cache/versionhoo/webhook-spool` when the user cache directory cannot
  be resolved, and finally `os.TempDir()/versionhoo-webhook-spool`. The
  directory and records are created with restrictive permissions.
- `VERSIONHOO_WEBHOOK_SPOOL_MAX_BYTES`: positive integer maximum total spool
  bytes. Defaults to `67108864` (64 MiB); a full spool is the only admission
  condition that can return a retryable `503`.
- `VERSIONHOO_WORKDIR`: parent directory for release clones. Defaults to the
  system temp directory.
- `VERSIONHOO_ALLOWED_REPOS`: comma-separated allow-list. Empty means every
  installed repository is allowed.
- `VERSIONHOO_RELEASE_BRANCHES`: comma-separated release branches. Defaults to
  `main`.
- `VERSIONHOO_CI_WORKFLOWS`: comma-separated workflow names that trigger
  releases after success. Defaults to `CI`.
- `VERSIONHOO_CONFIG`: optional path to a Hooversion config inside each cloned
  repository.
- `VERSIONHOO_INSTALL_COMMAND`: rejected by App mode. Production App mode
  always creates a fresh clone, so dependencies cannot be installed in advance;
  a repository containing `bun.lock` is also rejected to prevent implicit
  installation.
- `VERSIONHOO_GIT_AUTHOR_NAME`: release commit author name. Defaults to
  `versionhoo[bot]`.
- `VERSIONHOO_GIT_AUTHOR_EMAIL`: release commit author email. Defaults to
  `versionhoo[bot]@users.noreply.github.com`.
- `VERSIONHOO_KEEP_WORKDIR`: set to `true` to keep clones after a run for
  debugging. Otherwise the temporary clone and token artifacts are removed.

`HOOVERSION_*` aliases are accepted for every `VERSIONHOO_*` variable. The app
uses the repository numeric id when requesting an installation token, so the
token is scoped to that repository rather than the whole installation.

Each release runs in a secret-isolated child environment containing only the
minimal process and repository metadata needed by the runner. Git credentials
are written to temporary mode-restricted files for clone/push and removed in
cleanup, including failure cleanup; the token is not placed in the child
environment. App mode always creates a fresh clone and supports only
repositories requiring neither dependency installation nor repository hooks.
Use the GitHub Actions release workflow or ordinary CLI for repositories that
need installation or hooks. The ordinary CLI retains its configured install
and hook behavior.

The listener enforces a 10-second header-read timeout, a 30-second whole-request
read timeout, 30-second write timeout, 60-second idle timeout, and a 64 KiB
maximum header size. If a reverse proxy is used, configure its request and
upstream timeouts no longer than these bounds.

## Release Flow

1. Repository CI completes successfully on a configured release branch.
2. GitHub sends a signed `workflow_run` webhook to the app.
3. Versionhoo verifies the webhook signature and ignores failed runs, PR runs,
   release commits, forked runs, unapproved repos, and unapproved branches.
4. Versionhoo creates a GitHub App installation token scoped to the webhook
   repository id.
5. The app creates a `Versionhoo Release` check run on the CI-passed commit.
6. The app clones the repository and confirms the release branch still points at
   the workflow head SHA. Stale runs are marked neutral and skipped.
7. The app runs the normal Hooversion release engine.
8. The app pushes release commits and tags with the installation token and
   creates GitHub Releases.
9. The app completes the `Versionhoo Release` check as success, neutral, or
   failure with a summary.

Release commits beginning with `chore(release):` or containing `[skip ci]` are
ignored to prevent release loops.

Webhook bodies are bounded by `VERSIONHOO_WEBHOOK_MAX_BODY_BYTES` before
signature verification and durable admission. Every validated, handled
`workflow_run` is written and fsynced as a bounded record in the spool before
the app returns HTTP 202. The in-memory queue still accepts at most 64 running
or waiting jobs and serializes jobs per repository/ref; a full queue therefore
leaves the durable record pending rather than returning a lossy `503`.

Spool records are sequence-numbered and drained FIFO for each repository/ref.
Pending records are recovered and retried after process restart. Completed
records and final failures are removed only after execution reaches its
terminal callback. Delivery and workflow-run dedupe reservations stay in
memory for 24 hours and are released after final failure (or an admission
error), never merely because the bounded execution queue is saturated.
Malformed, oversized, symlinked, or path-invalid spool entries are ignored or
quarantined without blocking later records.
