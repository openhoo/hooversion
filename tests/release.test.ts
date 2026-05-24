import { describe, expect, it } from "bun:test";
import { mkdtempSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { normalizeConfig } from "../src/config";
import { createReleasePlan } from "../src/plan";
import { executeRelease } from "../src/release";

describe("release execution", () => {
  it("updates files, commits, tags, and writes outputs without GitHub or push", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0");
    writeFileSync(join(cwd, "app.ts"), "export const app = true;\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "fix: repair app");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);
    await executeRelease(cwd, config, plan, { push: false, github: false });

    const pkg = JSON.parse(readFileSync(join(cwd, "package.json"), "utf8")) as { version: string };
    expect(pkg.version).toBe("1.0.1");
    expect(readFileSync(join(cwd, "CHANGELOG.md"), "utf8")).toContain("## 1.0.1");
    expect(git(cwd, "tag", "--list", "v1.0.1")).toBe("v1.0.1");
    expect(existsSync(join(cwd, ".release-version"))).toBe(true);
    expect(readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8")).toContain('"published": true');
  });
});

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}
