import { describe, expect, it } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { normalizeConfig } from "../src/config";
import { createReleasePlan } from "../src/plan";

describe("release planning", () => {
  it("plans a single-package release from commits since the latest tag", () => {
    const cwd = makeRepo();
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "labelhoo", version: "0.1.0" }, null, 2));
    commitAll(cwd, "initial import");
    git(cwd, "tag", "-a", "v0.1.0", "-m", "v0.1.0");
    writeFileSync(join(cwd, "index.ts"), "export const value = 1;\n");
    commitAll(cwd, "feat: add public API");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "labelhoo", path: ".", type: "node", manifest: "package.json" }],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);

    expect(plan.releases).toHaveLength(1);
    expect(plan.releases[0].nextVersion).toBe("0.2.0");
    expect(plan.releases[0].tag).toBe("v0.2.0");
  });

  it("plans a release from a plain version file", () => {
    const cwd = makeRepo();
    writeFileSync(join(cwd, "version"), "1.2.3\n");
    commitAll(cwd, "initial import");
    git(cwd, "tag", "-a", "v1.2.3", "-m", "v1.2.3");
    writeFileSync(join(cwd, "Dockerfile"), "FROM scratch\n");
    commitAll(cwd, "fix(image): repair container metadata");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "image", path: ".", type: "version-file", manifest: "version" }],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);

    expect(plan.releases).toHaveLength(1);
    expect(plan.releases[0].currentVersion).toBe("1.2.3");
    expect(plan.releases[0].nextVersion).toBe("1.2.4");
    expect(plan.releases[0].tag).toBe("v1.2.4");
  });

  it("routes independent releases by path and propagates dependents as patch releases", () => {
    const cwd = makeRepo();
    mkdirSync(join(cwd, "crates", "hoot-plugin-sdk"), { recursive: true });
    mkdirSync(join(cwd, "crates", "hoot-core"), { recursive: true });
    writeFileSync(
      join(cwd, "Cargo.toml"),
      `[package]\nname = "hoot"\nversion = "0.1.0"\n\n[dependencies]\nhoot-core = { path = "crates/hoot-core", version = "0.1.0" }\n`,
    );
    writeFileSync(
      join(cwd, "crates", "hoot-plugin-sdk", "Cargo.toml"),
      `[package]\nname = "hoot-plugin-sdk"\nversion = "0.1.0"\n`,
    );
    writeFileSync(
      join(cwd, "crates", "hoot-core", "Cargo.toml"),
      `[package]\nname = "hoot-core"\nversion = "0.1.0"\n\n[dependencies]\nhoot-plugin-sdk = { path = "../hoot-plugin-sdk", version = "0.1.0" }\n`,
    );
    commitAll(cwd, "initial import");
    git(cwd, "tag", "-a", "hoot@v0.1.0", "-m", "hoot@v0.1.0");
    git(cwd, "tag", "-a", "hoot-core@v0.1.0", "-m", "hoot-core@v0.1.0");
    git(cwd, "tag", "-a", "hoot-plugin-sdk@v0.1.0", "-m", "hoot-plugin-sdk@v0.1.0");

    writeFileSync(join(cwd, "crates", "hoot-plugin-sdk", "lib.rs"), "pub fn parse() {}\n");
    commitAll(cwd, "feat(hoot-plugin-sdk): add parser");

    const config = normalizeConfig(cwd, {
      packages: [
        {
          name: "hoot-plugin-sdk",
          path: "crates/hoot-plugin-sdk",
          type: "rust",
          manifest: "crates/hoot-plugin-sdk/Cargo.toml",
          dependencies: [],
        },
        {
          name: "hoot-core",
          path: "crates/hoot-core",
          type: "rust",
          manifest: "crates/hoot-core/Cargo.toml",
          dependencies: ["hoot-plugin-sdk"],
        },
        {
          name: "hoot",
          path: ".",
          type: "rust",
          manifest: "Cargo.toml",
          dependencies: ["hoot-core"],
        },
      ],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);
    const releases = new Map(plan.releases.map((release) => [release.package.name, release]));

    expect(releases.get("hoot-plugin-sdk")?.nextVersion).toBe("0.2.0");
    expect(releases.get("hoot-core")?.nextVersion).toBe("0.1.1");
    expect(releases.get("hoot")?.nextVersion).toBe("0.1.1");
    expect(releases.get("hoot-core")?.dependencyTriggered).toBe(true);
    expect(releases.get("hoot")?.dependencyTriggered).toBe(true);
  });

  it("fails the plan when a release commit cannot be assigned to a package", () => {
    const cwd = makeRepo();
    mkdirSync(join(cwd, "packages", "one"), { recursive: true });
    mkdirSync(join(cwd, "packages", "two"), { recursive: true });
    writeFileSync(join(cwd, "packages", "one", "package.json"), JSON.stringify({ name: "one", version: "0.1.0" }));
    writeFileSync(join(cwd, "packages", "two", "package.json"), JSON.stringify({ name: "two", version: "0.1.0" }));
    writeFileSync(join(cwd, "README.md"), "hello\n");
    commitAll(cwd, "initial import");
    git(cwd, "tag", "-a", "one@v0.1.0", "-m", "one@v0.1.0");
    git(cwd, "tag", "-a", "two@v0.1.0", "-m", "two@v0.1.0");
    writeFileSync(join(cwd, "outside.txt"), "changed\n");
    commitAll(cwd, "fix: update outside package");

    const config = normalizeConfig(cwd, {
      packages: [
        { name: "one", path: "packages/one", type: "node", manifest: "packages/one/package.json" },
        { name: "two", path: "packages/two", type: "node", manifest: "packages/two/package.json" },
      ],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);

    expect(plan.unmatchedCommits).toHaveLength(1);
    expect(plan.releases).toHaveLength(0);
  });
});

function makeRepo(): string {
  const cwd = mkdtempSync(join(tmpdir(), "hooversion-"));
  git(cwd, "init", "-b", "main");
  git(cwd, "config", "user.email", "test@example.com");
  git(cwd, "config", "user.name", "Hooversion Test");
  return cwd;
}

function commitAll(cwd: string, message: string): void {
  git(cwd, "add", "--all");
  git(cwd, "commit", "-m", message);
}

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}
