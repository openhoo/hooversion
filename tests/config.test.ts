import { describe, expect, it } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { normalizeConfig } from "../src/config";

describe("configuration package graph validation", () => {
  it("rejects duplicate names after normalization", () => {
    const cwd = tempDirectory();
    writeJson(cwd, "one.json", "one");
    writeJson(cwd, "two.json", "two");

    expect(() => normalizeConfig(cwd, {
      packages: [
        { name: "Package", path: ".", type: "node", manifest: "one.json" },
        { name: " package ", path: ".", type: "node", manifest: "two.json" },
      ],
    })).toThrow(/Duplicate package name/);
  });

  it("rejects unknown and self dependency references", () => {
    const cwd = tempDirectory();
    writeJson(cwd, "package.json", "owner");

    expect(() => normalizeConfig(cwd, {
      packages: [{ name: "owner", path: ".", type: "node", manifest: "package.json", dependencies: ["missing"] }],
    })).toThrow(/unknown package missing/);

    expect(() => normalizeConfig(cwd, {
      packages: [{ name: "owner", path: ".", type: "node", manifest: "package.json", dependencies: ["owner"] }],
    })).toThrow(/cannot depend on itself/);
  });

  it("rejects dependency cycles", () => {
    const cwd = tempDirectory();
    mkdirSync(join(cwd, "a"));
    mkdirSync(join(cwd, "b"));
    writeJson(join(cwd, "a"), "package.json", "a");
    writeJson(join(cwd, "b"), "package.json", "b");

    expect(() => normalizeConfig(cwd, {
      packages: [
        { name: "a", path: "a", type: "node", manifest: "a/package.json", dependencies: ["b"] },
        { name: "b", path: "b", type: "node", manifest: "b/package.json", dependencies: ["a"] },
      ],
    })).toThrow(/cycle detected/);
  });

  it("rejects option-like, leading-dash, and control-character refs", () => {
    const cwd = tempDirectory();
    writeJson(cwd, "package.json", "app");
    for (const branch of ["--upload-pack=evil", "-release", "release\nbranch"]) {
      expect(() =>
        normalizeConfig(cwd, {
          branches: [branch],
          packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
        }),
      ).toThrow(/Invalid Git branch/);
    }

    expect(() =>
      normalizeConfig(cwd, {
        tagFormat: "--upload-pack=${version}",
        packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
      }),
    ).toThrow(/Invalid Git tag/);
  });

  it("accepts main branches and scoped release tag formats", () => {
    const cwd = tempDirectory();
    writeJson(cwd, "package.json", "app");
    const config = normalizeConfig(cwd, {
      branches: ["main"],
      tagFormat: "release/${version}",
      packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
    });
    expect(config.branches).toEqual(["main"]);
    expect(config.tagFormat).toBe("release/${version}");
  });
});

function tempDirectory(): string {
  return mkdtempSync(join(tmpdir(), "hooversion-config-"));
}

function writeJson(directory: string, filename: string, name: string): void {
  writeFileSync(join(directory, filename), JSON.stringify({ name, version: "1.0.0" }));
}
