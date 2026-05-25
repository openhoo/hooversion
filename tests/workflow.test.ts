import { describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { renderGitHubWorkflows } from "../src/workflow";

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
    });

    expect(workflows.ci).toContain("name: CI");
    expect(workflows.ci).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(workflows.ci).toContain("uses: acme/hooversion/actions/lint@main");
    expect(workflows.ci).toContain("name: Lint commits");
    expect(workflows.ci).toContain("name: Test and build");
    expect(workflows.release).toContain("name: Release");
    expect(workflows.release).toContain("workflows:\n      - CI");
    expect(workflows.release).toContain("uses: acme/hooversion/actions/release@main");
    expect(workflows.release).toContain("github.event.workflow_run.conclusion == 'success'");
    expect(workflows.release).toContain("github.event.workflow_run.event == 'push'");
    expect(workflows.release).toContain("contains(github.event.workflow_run.head_commit.message, 'chore(release):')");
    expect(workflows.release).toContain("Prepare release PR");
    expect(workflows.release).toContain("RELEASE_TOKEN: ${{ secrets.RELEASE_TOKEN }}");
    expect(workflows.release).toContain('push: "false"');
    expect(workflows.release).toContain('github: "false"');
    expect(workflows.release).toContain("gh pr create --base main");
    expect(workflows.release).toContain("Publish release");
    expect(workflows.release).toContain("gh release create");
    expect(workflows.release).toContain("fetch-depth: 0");
    expect(workflows.release).toContain("contents: write");
    expect(workflows.release).toContain("github-token: ${{ secrets.RELEASE_TOKEN }}");
    expect(workflows.release).toContain("install-command: bun install --frozen-lockfile");
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
