import { describe, expect, it } from "bun:test";
import { execFileSync, spawnSync } from "node:child_process";
import { mkdirSync, mkdtempSync, readFileSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join, resolve } from "node:path";

const cli = resolve("src/cli.ts");

describe("CLI argument validation", () => {
  it("rejects unknown options before release side effects", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-release-"));
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }));
    writeFileSync(
      join(cwd, "hooversion.config.ts"),
      'export default { packages: [{ name: "app", path: ".", type: "node" }], github: false, push: false };\n',
    );
    const before = readFileSync(join(cwd, "package.json"), "utf8");

    const result = runCli(cwd, "release", "--unknown");

    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("Unknown option");
    expect(readFileSync(join(cwd, "package.json"), "utf8")).toBe(before);

    const unknownCommand = runCli(cwd, "wat");
    expect(unknownCommand.status).not.toBe(0);
    expect(unknownCommand.stderr).toContain("Unknown command");
  });

  it("does not write config when workflow initialization collides", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-init-collision-"));
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }));
    mkdirSync(join(cwd, ".github", "workflows"), { recursive: true });
    writeFileSync(join(cwd, ".github", "workflows", "ci.yml"), "name: User CI\n");

    const result = runCli(cwd, "init");

    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain("Refusing to overwrite existing workflow");
    expect(() => readFileSync(join(cwd, "hooversion.config.ts"), "utf8")).toThrow();
  });

  it("rejects missing values, conflicting lint selectors, positional garbage, and invalid refs", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-lint-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "message.txt"), "fix: repair\n");

    expect(runCli(cwd, "plan", "--config").status).not.toBe(0);
    expect(runCli(cwd, "lint", "--last", "--edit", "message.txt").status).not.toBe(0);
    expect(runCli(cwd, "lint", "--last", "garbage").status).not.toBe(0);
    const invalidRef = runCli(cwd, "lint", "--from", "does-not-exist");
    expect(invalidRef.status).not.toBe(0);
    expect(invalidRef.stderr).toContain("Invalid git ref");
  });

  it("keeps an ordinary no-release plan successful", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-plan-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }));
    writeFileSync(
      join(cwd, "hooversion.config.ts"),
      'export default { packages: [{ name: "app", path: ".", type: "node" }], github: false, push: false };\n',
    );
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "chore: initial import");

    const result = runCli(cwd, "plan");

    expect(result.status).toBe(0);
    expect(result.stdout).toContain("No release needed.");
  });
  it("reports a resumed release as complete when the fresh plan is empty", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-resume-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }));
    writeFileSync(
      join(cwd, "hooversion.config.ts"),
      'export default { packages: [{ name: "app", path: ".", type: "node" }], github: false, push: false };\n',
    );
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "chore: initial import");
    git(cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0");
    writeFileSync(join(cwd, "app.ts"), "export const app = true;\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "fix: repair app");

    const first = runCli(cwd, "release", "--no-push", "--no-github");
    expect(first.status).toBe(0);
    const resumed = runCli(cwd, "release", "--no-push", "--no-github");

    expect(resumed.status).toBe(0);
    expect(resumed.stdout).toContain("Release complete.");
    expect(resumed.stdout).not.toContain("No release needed.");
  });
  it("makes an explicitly unmatched plan nonzero", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-cli-unmatched-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    mkdirSync(join(cwd, "a"), { recursive: true });
    mkdirSync(join(cwd, "b"), { recursive: true });
    writeFileSync(join(cwd, "a", "package.json"), JSON.stringify({ name: "a", version: "1.0.0" }));
    writeFileSync(join(cwd, "b", "package.json"), JSON.stringify({ name: "b", version: "1.0.0" }));
    writeFileSync(
      join(cwd, "hooversion.config.ts"),
      'export default { packages: [{ name: "a", path: "a", type: "node" }, { name: "b", path: "b", type: "node" }], github: false, push: false };\n',
    );
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "chore: initial import");
    git(cwd, "tag", "a@v1.0.0");
    git(cwd, "tag", "b@v1.0.0");
    writeFileSync(join(cwd, "README.txt"), "root change\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "feat: change outside packages");

    const result = runCli(cwd, "plan");

    expect(result.status).not.toBe(0);
    expect(result.stdout).toContain("Unmatched release commits:");
  });
});

function runCli(cwd: string, ...args: string[]) {
  return spawnSync("bun", ["run", cli, ...args], { cwd, encoding: "utf8" });
}

function git(cwd: string, ...args: string[]): void {
  execFileSync("git", args, { cwd, stdio: "ignore" });
}
