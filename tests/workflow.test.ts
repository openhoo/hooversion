import { describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { renderGitHubWorkflows, writeGitHubWorkflows } from "../src/workflow";

const packageJson = JSON.parse(readFileSync("package.json", "utf8")) as { version: string };

describe("GitHub Actions integration", () => {
  it("keeps action version defaults aligned with the package version", () => {
    for (const file of ["actions/setup/action.yml", "actions/lint/action.yml", "actions/release/action.yml"]) {
      const text = readFileSync(file, "utf8");
      expect(text).toContain(`  version:\n    description:`);
      expect(text).toContain(`    default: "${packageJson.version}"`);
      expect(text).toContain("runs:\n  using: composite");
    }
  });

  it("defines release outputs from the Hooversion release step", () => {
    const text = readFileSync("actions/release/action.yml", "utf8");
    expect(text).toContain("published:");
    expect(text).toContain("value: ${{ steps.release.outputs.published }}");
    expect(text).toContain("releases-json:");
    expect(text).toContain("value: ${{ steps.release.outputs.releases_json }}");
    expect(text).toContain("install-command:");
    expect(text).toContain("git remote set-url origin");
  });

  it("renders action-based CI and release workflows with full history checkout and release permissions", () => {
    const workflows = renderGitHubWorkflows({
      actionOwnerRepo: "acme/hooversion",
      actionRef: "main",
      hooversionVersion: "9.9.9",
      bunVersion: "1.2.3",
      releaseBranch: "stable",
    });

    expect(workflows.ci).toContain("name: CI");
    expect(workflows.ci).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(workflows.ci).toContain("uses: acme/hooversion/actions/lint@main");
    expect(workflows.ci).toContain("uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6");
    expect(workflows.ci).toContain("uses: oven-sh/setup-bun@0c5077e51419868618aeaa5fe8019c62421857d6 # v2");
    expect(workflows.ci).toContain("branches:\n      - stable");
    expect(workflows.ci).toContain("name: Lint commits");
    expect(workflows.ci).toContain("name: Test and build");
    expect(workflows.release).toContain("workflows:\n      - CI");
    expect(workflows.release).toContain("workflow_dispatch:");
    expect(workflows.release).toContain("uses: acme/hooversion/actions/release@main");
    expect(workflows.release).toContain("github.event.workflow_run.head_sha");
    expect(workflows.release).toContain("github.event.workflow_run.head_branch == 'stable'");
    expect(workflows.release).toContain("github.ref_name == 'stable'");
    expect(workflows.release).toContain("ref: ${{ github.event_name == 'workflow_run' && github.event.workflow_run.head_sha || github.sha }}");
    expect(workflows.release).toContain("concurrency:\n  group: release-${{ github.repository }}\n  cancel-in-progress: false");
    expect(workflows.release).toContain("uses: actions/checkout@d23441a48e516b6c34aea4fa41551a30e30af803 # v6");
    expect(workflows.release).not.toContain("actions/checkout@v6");
    expect(workflows.release).toContain("github.event.workflow_run.conclusion == 'success'");

    expect(workflows.release).toContain("github.event.workflow_run.event == 'push'");
    expect(workflows.release).toContain("contains(github.event.workflow_run.head_commit.message, 'chore(release):')");
    expect(workflows.release).toContain("name: Release");
    expect(workflows.release).toContain("github-token: ${{ secrets.GITHUB_TOKEN }}");
    expect(workflows.release).not.toContain("RELEASE_TOKEN");
    expect(workflows.release).not.toContain("Prepare release PR");
    expect(workflows.release).not.toContain('push: "false"');
    expect(workflows.release).not.toContain('github: "false"');
    expect(workflows.release).not.toContain("gh pr create");
    expect(workflows.release).toContain("fetch-depth: 0");
    expect(workflows.release).toContain("contents: write");
    expect(workflows.release).toContain("install-command: bun install --frozen-lockfile");
  });
  it("refuses to overwrite an existing generated workflow", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-workflow-existing-"));
    const workflowDir = join(cwd, ".github", "workflows");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    mkdirSync(workflowDir, { recursive: true });
    writeFileSync(join(workflowDir, "ci.yml"), "user-owned\\n");

    expect(() => writeGitHubWorkflows(cwd)).toThrow("Refusing to overwrite existing workflow");
    expect(readFileSync(join(workflowDir, "ci.yml"), "utf8")).toBe("user-owned\\n");
  });

  it("allows force only for workflows carrying the Hooversion marker", () => {
    const generatedCwd = mkdtempSync(join(tmpdir(), "hooversion-workflow-force-"));
    writeGitHubWorkflows(generatedCwd);
    writeGitHubWorkflows(generatedCwd, { force: true, hooversionVersion: "9.9.9" });
    expect(readFileSync(join(generatedCwd, ".github", "workflows", "ci.yml"), "utf8")).toContain(
      "HOOVERSION_VERSION: 9.9.9",
    );

    const userCwd = mkdtempSync(join(tmpdir(), "hooversion-workflow-force-user-"));
    const workflowDir = join(userCwd, ".github", "workflows");
    mkdirSync(workflowDir, { recursive: true });
    writeFileSync(join(workflowDir, "ci.yml"), "name: User CI\\n");
    expect(() => writeGitHubWorkflows(userCwd, { force: true })).toThrow("Refusing to overwrite existing workflow");
  });

  it("wires init flags into the generated workflow", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-init-"));
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    const cli = resolve("src/cli.ts");

    execFileSync(
      "bun",
      [
        "run",
        cli,
        "init",
        "--action-owner-repo",
        "acme/hooversion",
        "--action-ref",
        "main",
        "--hooversion-version",
        "9.9.9",
      ],
      { cwd, encoding: "utf8" },
    );

    const ci = readFileSync(join(cwd, ".github", "workflows", "ci.yml"), "utf8");
    const release = readFileSync(join(cwd, ".github", "workflows", "release.yml"), "utf8");
    expect(ci).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(ci).toContain("uses: acme/hooversion/actions/lint@main");
    expect(release).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(release).toContain("uses: acme/hooversion/actions/release@main");
  });
});
