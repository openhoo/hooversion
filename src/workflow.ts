import { existsSync, mkdirSync, readFileSync, writeFileSync } from "node:fs";
import { join } from "node:path";

export interface GitHubWorkflowOptions {
  actionOwnerRepo?: string;
  actionRef?: string;
  hooversionVersion?: string;
  bunVersion?: string;
}

const defaultActionOwnerRepo = "openhoo/hooversion";
const defaultBunVersion = "1.3.14";

export function writeGitHubWorkflow(cwd: string, options: GitHubWorkflowOptions = {}): string {
  return writeGitHubWorkflows(cwd, options)[0];
}

export function writeGitHubWorkflows(cwd: string, options: GitHubWorkflowOptions = {}): string[] {
  const workflowDir = join(cwd, ".github", "workflows");
  const ciPath = join(workflowDir, "ci.yml");
  const releasePath = join(workflowDir, "release.yml");
  const workflows = renderGitHubWorkflows(options);

  mkdirSync(workflowDir, { recursive: true });
  writeFileSync(ciPath, workflows.ci);
  writeFileSync(releasePath, workflows.release);
  return [ciPath, releasePath];
}

export function renderGitHubWorkflow(options: GitHubWorkflowOptions = {}): string {
  return renderGitHubWorkflows(options).ci;
}

export function renderGitHubWorkflows(options: GitHubWorkflowOptions = {}): { ci: string; release: string } {
  const hooversionVersion = options.hooversionVersion ?? getPackageVersion();
  const actionOwnerRepo = options.actionOwnerRepo ?? defaultActionOwnerRepo;
  const actionRef = options.actionRef ?? `v${hooversionVersion}`;
  const bunVersion = options.bunVersion ?? defaultBunVersion;

  const ci = `name: CI

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

env:
  HOOVERSION_VERSION: ${hooversionVersion}

jobs:
  commitlint:
    name: Lint commits
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - name: Lint commits
        uses: ${actionOwnerRepo}/actions/lint@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}

  build:
    name: Test and build
    runs-on: ubuntu-latest
    needs: commitlint
    steps:
      - name: Checkout
        uses: actions/checkout@v6

      - name: Set up Bun
        uses: oven-sh/setup-bun@v2
        with:
          bun-version: ${bunVersion}

      - name: Install dependencies
        run: bun install --frozen-lockfile

      - name: Run checks
        run: bun run check
`;

  const release = `name: Release

on:
  workflow_run:
    workflows:
      - CI
    branches:
      - main
    types:
      - completed

permissions:
  contents: read

env:
  HOOVERSION_VERSION: ${hooversionVersion}

jobs:
  prepare:
    name: Prepare release PR
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && !contains(github.event.workflow_run.head_commit.message, 'chore(release):')
    runs-on: ubuntu-latest
    permissions:
      contents: write
      pull-requests: write
    env:
      RELEASE_TOKEN: \${{ secrets.RELEASE_TOKEN }}
    steps:
      - name: Check release token
        id: release-token
        run: |
          set -euo pipefail
          if [[ -z "\${RELEASE_TOKEN}" ]]; then
            echo "::notice::Skipping release preparation because RELEASE_TOKEN is not configured."
            echo "configured=false" >> "$GITHUB_OUTPUT"
            exit 0
          fi
          echo "configured=true" >> "$GITHUB_OUTPUT"

      - name: Checkout
        if: steps.release-token.outputs.configured == 'true'
        uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: main

      - name: Prepare release
        if: steps.release-token.outputs.configured == 'true'
        id: release
        uses: ${actionOwnerRepo}/actions/release@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}
          push: "false"
          github: "false"
          github-token: \${{ secrets.RELEASE_TOKEN }}
          install-command: bun install --frozen-lockfile

      - name: Ensure release PR runs CI
        if: steps.release-token.outputs.configured == 'true' && steps.release.outputs.published == 'true'
        run: |
          set -euo pipefail
          subject="$(git log -1 --pretty=%s)"
          if [[ "$subject" == *" [skip ci]" ]]; then
            body="$(git log -1 --pretty=%b)"
            subject="\${subject% [skip ci]}"
            git commit --amend -m "$subject" -m "$body"
          fi

      - name: Open release PR
        if: steps.release-token.outputs.configured == 'true' && steps.release.outputs.published == 'true'
        env:
          GH_TOKEN: \${{ secrets.RELEASE_TOKEN }}
          RELEASE_VERSION: \${{ steps.release.outputs.version }}
          RELEASE_TAG: \${{ steps.release.outputs.tag }}
        run: |
          set -euo pipefail
          branch="hooversion/release-main"
          title="chore(release): \${RELEASE_VERSION}"
          body_file="$(mktemp)"

          git switch -c "$branch"
          git push --force origin "HEAD:\${branch}"

          cat > "$body_file" <<BODY
          Automated Hooversion release PR.

          - Version: \${RELEASE_VERSION}
          - Tag: \${RELEASE_TAG}

          Merging this PR runs the required CI checks before the release workflow publishes the tag and GitHub release.
          BODY

          pr="$(gh pr list --base main --head "$branch" --state open --json number --jq '.[0].number')"
          if [[ -n "$pr" ]]; then
            gh pr edit "$pr" --title "$title" --body-file "$body_file"
          else
            gh pr create --base main --head "$branch" --title "$title" --body-file "$body_file"
          fi

  publish:
    name: Publish release
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && contains(github.event.workflow_run.head_commit.message, 'chore(release):')
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: main

      - name: Set up Bun
        uses: oven-sh/setup-bun@v2
        with:
          bun-version: ${bunVersion}

      - name: Read package metadata
        id: package
        run: |
          set -euo pipefail
          name="$(bun -e 'const p = await Bun.file("package.json").json(); console.log(p.name)')"
          version="$(bun -e 'const p = await Bun.file("package.json").json(); console.log(p.version)')"
          {
            echo "name=\${name}"
            echo "version=\${version}"
            echo "tag=v\${version}"
          } >> "$GITHUB_OUTPUT"

      - name: Create tag and GitHub release
        env:
          GH_TOKEN: \${{ secrets.GITHUB_TOKEN }}
          PACKAGE_NAME: \${{ steps.package.outputs.name }}
          PACKAGE_VERSION: \${{ steps.package.outputs.version }}
          RELEASE_TAG: \${{ steps.package.outputs.tag }}
        run: |
          set -euo pipefail
          if git ls-remote --exit-code --tags origin "refs/tags/\${RELEASE_TAG}" >/dev/null 2>&1; then
            echo "Tag \${RELEASE_TAG} already exists."
            exit 0
          fi

          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git tag -a "$RELEASE_TAG" -m "\${PACKAGE_NAME} \${PACKAGE_VERSION}"
          git push origin "$RELEASE_TAG"

          awk -v version="$PACKAGE_VERSION" '
            $0 ~ "^## " version " \\\\(" { found=1; print; next }
            found && /^## / { exit }
            found { print }
          ' CHANGELOG.md > release-notes.md
          if [[ ! -s release-notes.md ]]; then
            printf '%s %s\\n' "$PACKAGE_NAME" "$PACKAGE_VERSION" > release-notes.md
          fi
          gh release create "$RELEASE_TAG" --title "\${PACKAGE_NAME} \${PACKAGE_VERSION}" --notes-file release-notes.md --verify-tag
`;

  return { ci, release };
}

function getPackageVersion(): string {
  const packagePath = new URL("../package.json", import.meta.url);
  if (!existsSync(packagePath)) return "0.1.0";
  const json = JSON.parse(readFileSync(packagePath, "utf8")) as { version?: string };
  return json.version ?? "0.1.0";
}
