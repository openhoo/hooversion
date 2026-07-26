import { describe, expect, it } from "bun:test";
import { existsSync, lstatSync, mkdirSync, mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { clearReleaseOutputs, getReleaseOutputPaths, writeReleaseOutputs } from "../src/output";
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

  it("treats missing managed files as already clean", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-missing-"));

    expect(() => clearReleaseOutputs(cwd)).not.toThrow();
    expect(() => clearReleaseOutputs(cwd)).not.toThrow();
  });

  it("ignores malformed stale output when inferring note paths", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-malformed-"));
    const outputDir = join(cwd, ".hooversion");
    mkdirSync(outputDir);
    writeFileSync(join(outputDir, "outputs.json"), "{not-json\n");
    writeFileSync(join(outputDir, "v1.2.3-notes.md"), "keep\n");

    clearReleaseOutputs(cwd);

    expect(existsSync(join(outputDir, "outputs.json"))).toBe(false);
    expect(readFileSync(join(outputDir, "v1.2.3-notes.md"), "utf8")).toBe("keep\n");
  });

  it("does not follow a symlinked stale output payload", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-symlink-"));
    const outputDir = join(cwd, ".hooversion");
    const outsideDir = mkdtempSync(join(tmpdir(), "hooversion-output-target-"));
    const payloadPath = join(outsideDir, "outputs.json");
    const notePath = join(outsideDir, "v1.2.3-notes.md");
    mkdirSync(outputDir);
    writeFileSync(payloadPath, JSON.stringify({ releases: [{ tag: "v1.2.3" }] }));
    writeFileSync(notePath, "outside\n");
    symlinkSync(payloadPath, join(outputDir, "outputs.json"));

    expect(getReleaseOutputPaths(cwd)).toEqual([
      join(outputDir, "outputs.json"),
      join(cwd, ".release-version"),
    ]);

    clearReleaseOutputs(cwd);

    expect(() => lstatSync(join(outputDir, "outputs.json"))).toThrow();
    expect(readFileSync(payloadPath, "utf8")).toContain("v1.2.3");
    expect(readFileSync(notePath, "utf8")).toBe("outside\n");
  });

  it("rejects a directory at a managed output path", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-directory-"));
    const outputDir = join(cwd, ".hooversion");
    mkdirSync(outputDir);
    mkdirSync(join(outputDir, "outputs.json"));

    expect(() => clearReleaseOutputs(cwd)).toThrow();
    expect(lstatSync(join(outputDir, "outputs.json")).isDirectory()).toBe(true);
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

  it("removes a symlinked release version without following its target", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-output-version-symlink-"));
    const targetPath = join(cwd, "version-target");
    writeFileSync(targetPath, "keep\n");
    symlinkSync(targetPath, join(cwd, ".release-version"));

    writeReleaseOutputs(cwd, config(), multiPlan());

    expect(() => lstatSync(join(cwd, ".release-version"))).toThrow();
    expect(readFileSync(targetPath, "utf8")).toBe("keep\n");
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
