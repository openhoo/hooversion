import { describe, expect, it } from "bun:test";
import { mkdirSync, mkdtempSync, readdirSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { updateChangelog } from "../src/changelog";
import type { PackageRelease } from "../src/types";

describe("changelog updates", () => {
  it("creates a missing changelog", () => {
    const cwd = tempDirectory();

    updateChangelog(cwd, release());

    expect(readFileSync(join(cwd, "CHANGELOG.md"), "utf8")).toBe("# app Changelog\n\nRelease notes\n\n\n");
    expect(tempFiles(cwd)).toEqual([]);
  });

  it("updates an existing changelog while preserving its header and body", () => {
    const cwd = tempDirectory();
    writeFileSync(join(cwd, "CHANGELOG.md"), "# Existing Changelog\n\n## 1.0.0\n\nOlder notes\n");

    updateChangelog(cwd, release());

    expect(readFileSync(join(cwd, "CHANGELOG.md"), "utf8")).toBe(
      "# Existing Changelog\n\nRelease notes\n\n## 1.0.0\n\nOlder notes\n",
    );
    expect(tempFiles(cwd)).toEqual([]);
  });

  it("rejects a symlinked changelog without changing its target", () => {
    const cwd = tempDirectory();
    writeFileSync(join(cwd, "real.md"), "# Existing Changelog\n");
    symlinkSync("real.md", join(cwd, "CHANGELOG.md"));

    expect(() => updateChangelog(cwd, release())).toThrow();
    expect(readFileSync(join(cwd, "real.md"), "utf8")).toBe("# Existing Changelog\n");
    expect(tempFiles(cwd)).toEqual([]);
  });

  it("leaves no temporary file when the changelog target is not a regular file", () => {
    const cwd = tempDirectory();
    mkdirSync(join(cwd, "CHANGELOG.md"));

    expect(() => updateChangelog(cwd, release())).toThrow();
    expect(tempFiles(cwd)).toEqual([]);
  });
});

function release(): PackageRelease {
  return {
    tag: "v1.0.1",
    currentVersion: "1.0.0",
    nextVersion: "1.0.1",
    releaseType: "patch",
    commits: [],
    notes: "Release notes",
    changelogPath: "CHANGELOG.md",
    dependencyTriggered: false,
    package: {
      name: "app",
      path: ".",
      type: "node",
      manifest: "package.json",
      changelog: "CHANGELOG.md",
      scopes: [],
      dependencies: [],
      assets: [],
    },
  };
}

function tempDirectory(): string {
  return mkdtempSync(join(tmpdir(), "hooversion-changelog-"));
}

function tempFiles(cwd: string): string[] {
  return readdirSync(cwd).filter((name) => name.includes(".hooversion-") && name.endsWith(".tmp"));
}
