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
  const path = join(cwd, ".github", "workflows", "hooversion.yml");
  mkdirSync(join(cwd, ".github", "workflows"), { recursive: true });
  writeFileSync(path, renderGitHubWorkflow(options));
  return path;
}

export function renderGitHubWorkflow(options: GitHubWorkflowOptions = {}): string {
  const hooversionVersion = options.hooversionVersion ?? getPackageVersion();
  const actionOwnerRepo = options.actionOwnerRepo ?? defaultActionOwnerRepo;
  const actionRef = options.actionRef ?? `v${hooversionVersion}`;
  const bunVersion = options.bunVersion ?? defaultBunVersion;

  return `name: CI and Release

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

  release:
    name: Release
    runs-on: ubuntu-latest
    needs: build
    if: github.event_name == 'push' && github.ref == 'refs/heads/main'
    permissions:
      contents: write
      issues: write
      pull-requests: write
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - name: Release
        id: release
        uses: ${actionOwnerRepo}/actions/release@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}
          github-token: \${{ secrets.GITHUB_TOKEN }}
          install-command: bun install --frozen-lockfile
`;
}

function getPackageVersion(): string {
  const packagePath = new URL("../package.json", import.meta.url);
  if (!existsSync(packagePath)) return "0.1.0";
  const json = JSON.parse(readFileSync(packagePath, "utf8")) as { version?: string };
  return json.version ?? "0.1.0";
}
