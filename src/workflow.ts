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
  release:
    name: Release
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && !contains(github.event.workflow_run.head_commit.message, 'chore(release):')
    runs-on: ubuntu-latest
    permissions:
      contents: write
      issues: write
      pull-requests: write
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: main

      - name: Release
        id: release
        uses: ${actionOwnerRepo}/actions/release@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}
          github-token: \${{ secrets.GITHUB_TOKEN }}
          install-command: bun install --frozen-lockfile
`;

  return { ci, release };
}

function getPackageVersion(): string {
  const packagePath = new URL("../package.json", import.meta.url);
  if (!existsSync(packagePath)) return "0.1.0";
  const json = JSON.parse(readFileSync(packagePath, "utf8")) as { version?: string };
  return json.version ?? "0.1.0";
}
