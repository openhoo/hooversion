import { describe, expect, it } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { clearReleaseOutputs, writeReleaseOutputs } from "../src/output";
import type { NormalizedConfig, ReleasePlan } from "../src/types";

function config(outputDir = ".hooversion"): NormalizedConfig {
  return { outputDir } as NormalizedConfig;
}

function emptyPlan(): ReleasePlan {
  return { releases: [] } as unknown as ReleasePlan;
}

function multiPlan(): ReleasePlan {
  return {
    releases: [
      { nextVersion: "1.0.1", tag: "a@v1.0.1", notes: "a", package: { name: "a" }, releaseType: "patch" },
      { nextVersion: "2.0.1", tag: "b@v2.0.1", notes: "b", package: { name: "b" }, releaseType: "patch" },
    ],
  } as unknown as ReleasePlan;
}

describe("release output lifecycle", () => {
  it("clears only managed files and is idempotent", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-cleanup-"));
    const outputDir = join(cwd, ".hooversion");
    mkdirSync(outputDir);
    writeFileSync(join(cwd, ".release-version"), "9.9.9\n");
    writeFileSync(
      join(outputDir, "outputs.json"),
      JSON.stringify({ published: true, releases: [{ tag: "v1.2.3" }] }),
    );
    writeFileSync(join(outputDir, "v1.2.3-notes.md"), "generated\n");
    writeFileSync(join(outputDir, "user-notes.md"), "keep\n");

    clearReleaseOutputs(cwd);
    clearReleaseOutputs(cwd);

    expect(existsSync(join(cwd, ".release-version"))).toBe(false);
    expect(existsSync(join(outputDir, "outputs.json"))).toBe(false);
    expect(existsSync(join(outputDir, "v1.2.3-notes.md"))).toBe(false);
    expect(readFileSync(join(outputDir, "user-notes.md"), "utf8")).toBe("keep\n");
  });

  it("removes stale single-release fields before a no-release publication", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-no-release-"));
    writeFileSync(join(cwd, ".release-version"), "1.0.1\n");
    mkdirSync(join(cwd, ".hooversion"));
    writeFileSync(join(cwd, ".hooversion", "outputs.json"), '{"published":true,"releases":[]}\n');

    clearReleaseOutputs(cwd);
    writeReleaseOutputs(cwd, config(), emptyPlan());

    expect(existsSync(join(cwd, ".release-version"))).toBe(false);
    expect(JSON.parse(readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8"))).toEqual({
      published: false,
      releases: [],
    });
  });

  it("removes a stale single-release version for a multi-release publication", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-multi-release-"));
    writeFileSync(join(cwd, ".release-version"), "1.0.1\n");

    writeReleaseOutputs(cwd, config(), multiPlan());

    expect(existsSync(join(cwd, ".release-version"))).toBe(false);
  });

  it("does not truncate shared GitHub output", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-github-"));
    const outputPath = join(cwd, "github-output");
    writeFileSync(outputPath, "previous=value\n");
    const previous = process.env.GITHUB_OUTPUT;
    process.env.GITHUB_OUTPUT = outputPath;
    try {
      clearReleaseOutputs(cwd);
      expect(readFileSync(outputPath, "utf8")).toBe("previous=value\n");
    } finally {
      if (previous === undefined) delete process.env.GITHUB_OUTPUT;
      else process.env.GITHUB_OUTPUT = previous;
    }
  });
});
