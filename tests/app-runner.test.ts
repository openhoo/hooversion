import { describe, expect, it, mock } from "bun:test";
import { existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";

interface CommandCall {
  command: string;
  args: string[];
  cwd: string;
  env?: NodeJS.ProcessEnv;
}

const commandCalls: CommandCall[] = [];
const askpassSnapshots: Array<{ content: string; mode: number }> = [];
const tokenSnapshots: Array<{ content: string; mode: number; path: string }> = [];
const shellCalls: CommandCall[] = [];
const releaseOptions: Array<Record<string, unknown>> = [];
let resumedRelease = false;
let cloneFailure = false;
let releaseFailure = false;

function filesUnder(root: string): string[] {
  return readdirSync(root, { withFileTypes: true }).flatMap((entry) => {
    const path = join(root, entry.name);
    return entry.isDirectory() ? filesUnder(path) : [path];
  });
}

mock.module("../src/process", () => ({
  runCommand(command: string, args: string[], cwd: string, env?: NodeJS.ProcessEnv) {
    commandCalls.push({ command, args, cwd, env });
    if (args.includes("clone") && env?.GIT_ASKPASS) {
      askpassSnapshots.push({
        content: readFileSync(env.GIT_ASKPASS, "utf8"),
        mode: statSync(env.GIT_ASKPASS).mode & 0o777,
      });
    }
    if (args.includes("clone") && typeof env?.VERSIONHOO_GIT_TOKEN_FILE === "string") {
      tokenSnapshots.push({
        content: readFileSync(env.VERSIONHOO_GIT_TOKEN_FILE, "utf8"),
        mode: statSync(env.VERSIONHOO_GIT_TOKEN_FILE).mode & 0o777,
        path: env.VERSIONHOO_GIT_TOKEN_FILE,
      });
    }
    if (args.includes("clone")) {
      if (cloneFailure) return { code: 1, stdout: "", stderr: "clone failed" };
      const repoDir = args.at(-1);
      const origin = args.at(-2);
      if (repoDir && origin) {
        mkdirSync(join(repoDir, ".git"), { recursive: true });
        writeFileSync(join(repoDir, ".git", "config"), `[remote "origin"]\n\turl = ${origin}\n`);
      }
      return { code: 0, stdout: "", stderr: "" };
    }
    if (args[0] === "rev-parse" && args[1] === "HEAD") {
      return { code: 0, stdout: "head-sha\n", stderr: "" };
    }
    return { code: 0, stdout: "", stderr: "" };
  },
  runShell(command: string, cwd: string, env?: NodeJS.ProcessEnv) {
    shellCalls.push({ command, args: [], cwd, env });
    return { code: 0, stdout: "", stderr: "" };
  },
}));

mock.module("../src/config", () => ({
  loadConfig: async () => ({ github: false }),
}));
type MockPlan = {
  branch: string;
  sourceSha: string;
  releases: Array<{ package: { name: string }; nextVersion: string; tag: string }>;
  unmatchedCommits: unknown[];
};

mock.module("../src/plan", () => ({
  createReleasePlan: (): MockPlan => ({ branch: "main", sourceSha: "head-sha", releases: [], unmatchedCommits: [] }),
}));
mock.module("../src/release", () => ({
  executeRelease: async (
    _cwd: string,
    _config: unknown,
    inputPlan: MockPlan,
    options: Record<string, unknown>,
  ) => {
    releaseOptions.push(options);
    if (releaseFailure) throw new Error("release failed");
    if (!resumedRelease) return { plan: inputPlan, published: false };
    return {
      plan: {
        ...inputPlan,
        releases: [{ package: { name: "app" }, nextVersion: "2.4.0", tag: "v2.4.0" }],
      },
      published: true,
    };
  },
}));

// Dynamic imports are required so Bun applies the process/release mocks before loading these seams.
const { runVersionhooRelease } = await import("../src/app-runner");
const { pushRelease } = await import("../src/git");

describe("Versionhoo app Git credential plumbing", () => {
  it("passes auth only to atomic push network commands", () => {
    commandCalls.length = 0;
    pushRelease("/repo", "main", ["v1.2.3"], {
      GIT_ASKPASS: "/tmp/askpass",
      VERSIONHOO_GIT_TOKEN_FILE: "/tmp/token",
    });
    const push = commandCalls.find((call) => call.args[0] === "push");
    expect(push?.args).toEqual([
      "push",
      "--atomic",
      "--no-verify",
      "--",
      "origin",
      "HEAD:refs/heads/main",
      "refs/tags/v1.2.3",
    ]);
    expect(push?.args.join(" ")).not.toContain("push-token");
    expect(push?.env?.GIT_ASKPASS).toBe("/tmp/askpass");
    expect(push?.env?.VERSIONHOO_GIT_TOKEN_FILE).toBe("/tmp/token");
    expect(push?.env?.VERSIONHOO_GIT_TOKEN).toBeUndefined();
  });
  it("keeps credentials out of argv and ordinary child environments", async () => {
    commandCalls.length = 0;
    shellCalls.length = 0;
    askpassSnapshots.length = 0;
    tokenSnapshots.length = 0;
    releaseOptions.length = 0;
    const parent = mkdtempSync(join(tmpdir(), "versionhoo-app-test-"));
    const token = "super-secret-token";
    try {
      const result = await runVersionhooRelease({
        repositoryFullName: "openhoo/app",
        cloneUrl: "https://github.com/openhoo/app.git",
        branch: "main",
        headSha: "head-sha",
        token,
        workDir: parent,
        installCommand: "install-hook",
        keepWorkDir: true,
      });
      expect(result.outcome).toBe("no_release");
      const clone = commandCalls.find((call) => call.args.includes("clone"));
      expect(clone).toBeDefined();
      expect(clone?.args.join(" ")).not.toContain(token);
      expect(clone?.args.at(-2)).toBe("https://github.com/openhoo/app.git");
      expect(clone?.env?.VERSIONHOO_GIT_TOKEN).toBeUndefined();
      expect(clone?.env?.VERSIONHOO_GIT_TOKEN_FILE).toBe(tokenSnapshots[0]?.path);
      expect(clone?.env?.GIT_CONFIG_NOSYSTEM).toBe("1");
      expect(askpassSnapshots).toHaveLength(1);
      expect(askpassSnapshots[0]?.mode).toBe(0o700);
      expect(askpassSnapshots[0]?.content).not.toContain(token);
      expect(tokenSnapshots).toHaveLength(1);
      expect(tokenSnapshots[0]?.content).toBe(`${token}\n`);
      expect(tokenSnapshots[0]?.mode).toBe(0o600);
      expect(clone?.env?.GIT_ASKPASS).toBeDefined();
      expect(commandCalls.filter((call) => !call.args.includes("clone")).every((call) => call.env === undefined)).toBe(true);
      expect(shellCalls.every((call) => call.env === undefined)).toBe(true);
      const auth = releaseOptions[0]?.gitAuth;
      expect(auth && typeof auth === "object" ? Object.keys(auth).sort() : []).toEqual([
        "GIT_ASKPASS",
        "GIT_TERMINAL_PROMPT",
        "VERSIONHOO_GIT_TOKEN_FILE",
      ]);
      const authTokenPath =
        auth && typeof auth === "object" && "VERSIONHOO_GIT_TOKEN_FILE" in auth
          ? auth.VERSIONHOO_GIT_TOKEN_FILE
          : undefined;
      expect(authTokenPath).toBe(tokenSnapshots[0]?.path);
      expect(process.env.VERSIONHOO_GIT_TOKEN).toBeUndefined();
      expect(process.env.VERSIONHOO_GIT_TOKEN_FILE).toBeUndefined();
      expect(process.env.GIT_ASKPASS).toBeUndefined();
      const workDir = result.workDir;
      if (typeof authTokenPath !== "string") throw new Error("missing token file path");
      expect(authTokenPath.startsWith(join(workDir, "repo"))).toBe(false);
      expect(existsSync(authTokenPath)).toBe(false);
      expect(existsSync(join(workDir, ".git-askpass"))).toBe(false);
      expect(readFileSync(join(workDir, "repo", ".git", "config"), "utf8")).not.toContain(token);
      expect(filesUnder(workDir).every((path) => !readFileSync(path, "utf8").includes(token))).toBe(true);
    } finally {
      rmSync(parent, { recursive: true, force: true });
    }
  });
  it("reports the effective resumed releases returned by executeRelease", async () => {
    resumedRelease = true;
    const parent = mkdtempSync(join(tmpdir(), "versionhoo-app-resume-test-"));
    try {
      const result = await runVersionhooRelease({
        repositoryFullName: "openhoo/app",
        cloneUrl: "https://github.com/openhoo/app.git",
        branch: "main",
        headSha: "head-sha",
        token: "resume-token",
        workDir: parent,
        keepWorkDir: true,
      });

      expect(result.outcome).toBe("published");
      expect(result.published).toBe(true);
      expect(result.releases).toEqual([{ name: "app", version: "2.4.0", tag: "v2.4.0" }]);
    } finally {
      resumedRelease = false;
      rmSync(parent, { recursive: true, force: true });
    }
  });

  it("rejects an invalid clone URL before creating credential artifacts", async () => {
    commandCalls.length = 0;
    const token = "invalid-url-token";
    const parent = mkdtempSync(join(tmpdir(), "versionhoo-app-invalid-url-"));
    try {
      await expect(
        runVersionhooRelease({
          repositoryFullName: "openhoo/app",
          cloneUrl: "https://github.com/other/repo.git",
          branch: "main",
          headSha: "head-sha",
          token,
          workDir: parent,
          keepWorkDir: true,
        }),
      ).rejects.toThrow("mismatch");
      expect(commandCalls).toHaveLength(0);
      for (const entry of readdirSync(parent)) {
        const entryPath = join(parent, entry);
        expect(existsSync(join(entryPath, ".git-askpass"))).toBe(false);
        expect(filesUnder(entryPath).every((path) => !readFileSync(path, "utf8").includes(token))).toBe(true);
      }
    } finally {
      rmSync(parent, { recursive: true, force: true });
    }
  });

  it("cleans credentials after clone failure", async () => {
    cloneFailure = true;
    const token = "clone-failure-token";
    const parent = mkdtempSync(join(tmpdir(), "versionhoo-app-test-"));
    try {
      await expect(
        runVersionhooRelease({
          repositoryFullName: "openhoo/app",
          cloneUrl: "https://github.com/openhoo/app.git",
          branch: "main",
          headSha: "head-sha",
          token,
          workDir: parent,
          keepWorkDir: true,
        }),
      ).rejects.toThrow("clone failed");
      for (const entry of readdirSync(parent)) {
        const entryPath = join(parent, entry);
        expect(existsSync(join(entryPath, ".git-askpass"))).toBe(false);
        expect(filesUnder(entryPath).every((path) => !readFileSync(path, "utf8").includes(token))).toBe(true);
      }
    } finally {
      cloneFailure = false;
      rmSync(parent, { recursive: true, force: true });
    }
  });

  it("cleans credentials after release failure", async () => {
    releaseFailure = true;
    const token = "release-failure-token";
    const parent = mkdtempSync(join(tmpdir(), "versionhoo-app-test-"));
    try {
      await expect(
        runVersionhooRelease({
          repositoryFullName: "openhoo/app",
          cloneUrl: "https://github.com/openhoo/app.git",
          branch: "main",
          headSha: "head-sha",
          token,
          workDir: parent,
          keepWorkDir: true,
        }),
      ).rejects.toThrow("release failed");
      for (const entry of readdirSync(parent)) {
        const entryPath = join(parent, entry);
        expect(existsSync(join(entryPath, ".git-askpass"))).toBe(false);
        expect(filesUnder(entryPath).every((path) => !readFileSync(path, "utf8").includes(token))).toBe(true);
      }
    } finally {
      releaseFailure = false;
      rmSync(parent, { recursive: true, force: true });
    }
  });
});
