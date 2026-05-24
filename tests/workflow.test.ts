import { describe, expect, it } from "bun:test";
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";
import { renderGitHubWorkflow } from "../src/workflow";

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

  it("renders action-based workflows with full history checkout and release permissions", () => {
    const workflow = renderGitHubWorkflow({
      actionOwnerRepo: "acme/hooversion",
      actionRef: "main",
      hooversionVersion: "9.9.9",
      bunVersion: "1.2.3",
    });

    expect(workflow).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(workflow).toContain("uses: acme/hooversion/actions/lint@main");
    expect(workflow).toContain("uses: acme/hooversion/actions/release@main");
    expect(workflow).toContain("fetch-depth: 0");
    expect(workflow).toContain("contents: write");
    expect(workflow).toContain("github-token: ${{ secrets.GITHUB_TOKEN }}");
    expect(workflow).toContain("install-command: bun install --frozen-lockfile");
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

    const workflow = readFileSync(join(cwd, ".github", "workflows", "hooversion.yml"), "utf8");
    expect(workflow).toContain("HOOVERSION_VERSION: 9.9.9");
    expect(workflow).toContain("uses: acme/hooversion/actions/lint@main");
    expect(workflow).toContain("uses: acme/hooversion/actions/release@main");
  });
});
