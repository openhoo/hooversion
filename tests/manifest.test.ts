import { describe, expect, it } from "bun:test";
import { mkdtempSync, readFileSync, symlinkSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { updateLocalDependencyVersions, updateManifestVersion } from "../src/manifest";
import type { NormalizedPackageConfig } from "../src/types";

describe("manifest dependency updates", () => {
  it("updates only declared Node dependency edges", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "package.json"),
      JSON.stringify(
        {
          name: "owner",
          version: "1.0.0",
          dependencies: { local: "^1.0.0", unrelated: "1.0.0" },
          description: "local 1.0.0",
          scripts: { local: "1.0.0" },
        },
        null,
        2,
      ),
    );
    writeFileSync(join(cwd, "local.json"), JSON.stringify({ name: "local", version: "1.0.0" }));

    updateLocalDependencyVersions(
      cwd,
      [pkg("owner", "node", "package.json", ["local"]), pkg("local", "node", "local.json", [])],
      new Map([["local", "2.0.0"]]),
    );

    const manifest = JSON.parse(readFileSync(join(cwd, "package.json"), "utf8"));
    expect(manifest.dependencies.local).toBe("^2.0.0");
    expect(manifest.dependencies.unrelated).toBe("1.0.0");
    expect(manifest.description).toBe("local 1.0.0");
    expect(manifest.scripts.local).toBe("1.0.0");
  });

  it("updates Python project and optional dependencies", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "pyproject.toml"),
      "[project]\nname = \"owner\"\nversion = \"1.0.0\"\ndependencies = [\"local>=1.0.0\"]\n\n[project.optional-dependencies]\ndev = [\"local\"]\n",
    );

    updateLocalDependencyVersions(
      cwd,
      [pkg("owner", "python", "pyproject.toml", ["local"]), pkg("local", "python", "local.toml", [])],
      new Map([["local", "2.0.0"]]),
    );

    const manifest = readFileSync(join(cwd, "pyproject.toml"), "utf8");
    expect(manifest).toContain("local>=2.0.0");
    expect(manifest).toContain("local==2.0.0");
  });

  it("updates every Rust dependency table but preserves workspace inheritance", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "Cargo.toml"),
      `[package]\nname = "owner"\nversion = "1.0.0"\n\n[dependencies]\nlocal = { version = "1.0.0", path = "../local" }\nunrelated = "1.0.0"\n\n[dev-dependencies.local]\nversion = "1.0.0"\npath = "../local"\n\n[target.'cfg(unix)'.dependencies.local]\nversion = "1.0.0"\npath = "../local"\n\n[workspace.dependencies.local]\nversion = "1.0.0"\n\n[build-dependencies]\nworkspace-local = { workspace = true }\n`,
    );

    updateLocalDependencyVersions(
      cwd,
      [
        pkg("owner", "rust", "Cargo.toml", ["local", "workspace-local"]),
        pkg("local", "rust", "local/Cargo.toml", []),
        pkg("workspace-local", "rust", "workspace-local/Cargo.toml", []),
      ],
      new Map([
        ["local", "2.0.0"],
        ["workspace-local", "3.0.0"],
      ]),
    );

    const manifest = readFileSync(join(cwd, "Cargo.toml"), "utf8");
    expect(manifest.match(/version = "2\.0\.0"/g)).toHaveLength(4);
    expect(manifest).toContain("workspace-local = { workspace = true }");
    expect(manifest).toContain('[workspace.dependencies.local]\nversion = "2.0.0"');
    expect(manifest).toContain('unrelated = "1.0.0"');
  });

  it("updates local Cargo.lock package versions without registry records", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "Cargo.toml"),
      "[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n",
    );
    writeFileSync(
      join(cwd, "Cargo.lock"),
      `[[package]]\nname = "owner"\nversion = "1.0.0"\ndependencies = [\n "local 1.0.0",\n]\n\n[[package]]\nname = "local"\nversion = "1.0.0"\ndependencies = [\n "other 1.0.0",\n]\n\n[[package]]\nname = "local"\nversion = "1.0.0"\nsource = "registry+https://example.invalid"\n`,
    );

    updateLocalDependencyVersions(
      cwd,
      [pkg("owner", "rust", "Cargo.toml", ["local"]), pkg("local", "rust", "local/Cargo.toml", [])],
      new Map([["local", "2.0.0"]]),
    );

    const lock = readFileSync(join(cwd, "Cargo.lock"), "utf8");
    expect(lock).toContain('name = "local"\nversion = "2.0.0"');
    expect(lock).toContain('name = "local"\nversion = "1.0.0"\nsource');
    expect(lock).toContain(' "local 2.0.0",');
  });

  it("allows a missing Cargo.lock", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "Cargo.toml"),
      "[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n",
    );
    writeFileSync(join(cwd, "local.Cargo.toml"), "[package]\nname = \"local\"\nversion = \"1.0.0\"\n");

    expect(() =>
      updateLocalDependencyVersions(
        cwd,
        [pkg("owner", "rust", "Cargo.toml", ["local"]), pkg("local", "rust", "local.Cargo.toml", [])],
        new Map([["local", "2.0.0"]]),
      ),
    ).not.toThrow();
    expect(() => readFileSync(join(cwd, "Cargo.lock"), "utf8")).toThrow();
  });

  it("rejects a symlinked Cargo.lock", () => {
    const cwd = tempDirectory();
    writeFileSync(
      join(cwd, "Cargo.toml"),
      "[package]\nname = \"owner\"\nversion = \"1.0.0\"\n\n[dependencies]\nlocal = { path = \"local\", version = \"1.0.0\" }\n",
    );
    writeFileSync(join(cwd, "local.Cargo.toml"), "[package]\nname = \"local\"\nversion = \"1.0.0\"\n");
    writeFileSync(join(cwd, "Cargo.lock.target"), "lock contents\n");
    symlinkSync("Cargo.lock.target", join(cwd, "Cargo.lock"));

    expect(() =>
      updateLocalDependencyVersions(
        cwd,
        [pkg("owner", "rust", "Cargo.toml", ["local"]), pkg("local", "rust", "local.Cargo.toml", [])],
        new Map([["local", "2.0.0"]]),
      ),
    ).toThrow();
    expect(readFileSync(join(cwd, "Cargo.lock.target"), "utf8")).toBe("lock contents\n");
  });
  it("keeps first-class version-file updates plain", () => {
    const cwd = tempDirectory();
    writeFileSync(join(cwd, "version"), "1.0.0\n");
    updateManifestVersion(cwd, pkg("image", "version-file", "version", []), "2.0.0");
    expect(readFileSync(join(cwd, "version"), "utf8")).toBe("2.0.0\n");
  });
});

function pkg(
  name: string,
  type: NormalizedPackageConfig["type"],
  manifest: string,
  dependencies: string[],
): NormalizedPackageConfig {
  return {
    name,
    path: ".",
    type,
    manifest,
    changelog: "CHANGELOG.md",
    scopes: [name],
    dependencies,
    assets: [],
  };
}

function tempDirectory(): string {
  return mkdtempSync(join(tmpdir(), "hooversion-manifest-"));
}
