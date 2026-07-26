import { describe, expect, it } from "bun:test";
import { mkdtempSync, mkdirSync, writeFileSync, readFileSync, existsSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { execFileSync } from "node:child_process";
import { normalizeConfig } from "../src/config";
import { createAnnotatedTag, getRemoteBranchSha, pushRelease } from "../src/git";
import { createReleasePlan } from "../src/plan";
import { executeRelease } from "../src/release";
type FetchInput = Parameters<typeof fetch>[0];
type FetchInit = Parameters<typeof fetch>[1];

describe("release execution", () => {
  it("rejects malicious branches and tags before invoking Git", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-ref-safety-"));
    for (const branch of ["--upload-pack=evil", "-release", "release\nbranch"]) {
      expect(() => getRemoteBranchSha(cwd, branch)).toThrow(/Invalid Git branch/);
      expect(() => pushRelease(cwd, branch, ["v1.0.1"])).toThrow(/Invalid Git branch/);
    }
    for (const tag of ["--upload-pack=evil", "-release", "release\nv1.0.1"]) {
      expect(() => pushRelease(cwd, "main", [tag])).toThrow(/Invalid Git tag/);
      expect(() => createAnnotatedTag(cwd, tag, "release")).toThrow(/Invalid Git tag/);
    }
  });

  it("skips repository pre-push hooks during authenticated release pushes", () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-hook-safety-"));
    const remote = mkdtempSync(join(tmpdir(), "hooversion-hook-safety-remote-"));
    git(remote, "init", "--bare");
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }));
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "remote", "add", "origin", remote);
    git(cwd, "tag", "-a", "release/v1.2.3", "-m", "release/v1.2.3");
    const marker = join(cwd, "hook-observed-token");
    writeFileSync(
      join(cwd, ".git", "hooks", "pre-push"),
      `#!/bin/sh\nprintf '%s' "$VERSIONHOO_GIT_TOKEN" > "${marker}"\n`,
      { mode: 0o755 },
    );

    pushRelease(cwd, "main", ["release/v1.2.3"], { VERSIONHOO_GIT_TOKEN: "secret-token" });
    expect(existsSync(marker)).toBe(false);
  });
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
    const result = await executeRelease(cwd, config, plan, { push: false, github: false });
    expect(result.published).toBe(true);

    const pkg = JSON.parse(readFileSync(join(cwd, "package.json"), "utf8")) as { version: string };
    expect(pkg.version).toBe("1.0.1");
    expect(readFileSync(join(cwd, "CHANGELOG.md"), "utf8")).toContain("## 1.0.1");
    expect(git(cwd, "tag", "--list", "v1.0.1")).toBe("v1.0.1");
    expect(existsSync(join(cwd, ".release-version"))).toBe(true);
    expect(readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8")).toContain('"published": true');
  });

  it("rejects a plan when local HEAD advanced before any release mutation", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-drift-"));
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
    writeFileSync(join(cwd, "unrelated.txt"), "drift\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "chore: unrelated drift");

    await expect(executeRelease(cwd, config, plan, { push: false, github: false })).rejects.toThrow(
      "Release source changed locally",
    );
    expect(JSON.parse(readFileSync(join(cwd, "package.json"), "utf8")).version).toBe("1.0.0");
    expect(existsSync(join(cwd, "CHANGELOG.md"))).toBe(false);
    expect(git(cwd, "tag", "--list", "v1.0.1")).toBe("");
  });

  it("pushes the release branch and all tags atomically", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-atomic-"));
    const remote = mkdtempSync(join(tmpdir(), "hooversion-release-remote-"));
    git(remote, "init", "--bare");
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0");
    git(cwd, "remote", "add", "origin", remote);
    git(cwd, "push", "origin", "main", "--tags");
    writeFileSync(join(cwd, "app.ts"), "export const app = true;\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "fix: repair app");
    git(cwd, "push", "origin", "main");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
      github: false,
      push: true,
    });
    const plan = createReleasePlan(cwd, config);
    const sourceSha = git(cwd, "rev-parse", "HEAD");
    execFileSync("git", ["-c", "user.email=test@example.com", "-c", "user.name=Hooversion Test", "--git-dir", remote,
      "tag", "-a", "v1.0.1", "-m", "conflicting tag", sourceSha], { encoding: "utf8" });

    await expect(executeRelease(cwd, config, plan, { push: true, github: false })).rejects.toThrow();
    expect(execFileSync("git", ["--git-dir", remote, "rev-parse", "refs/heads/main"], { encoding: "utf8" }).trim()).toBe(
      sourceSha,
    );
  });

  it("resumes an exact generated release commit without creating another commit", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-resume-"));
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
    const firstResult = await executeRelease(cwd, config, plan, { push: false, github: false });
    expect(firstResult.published).toBe(true);
    const releaseHead = git(cwd, "rev-parse", "HEAD");
    const commitCount = git(cwd, "rev-list", "--count", "HEAD");

    const rerunPlan = createReleasePlan(cwd, config);
    expect(rerunPlan.releases).toHaveLength(0);
    const rerunResult = await executeRelease(cwd, config, rerunPlan, { push: false, github: false });
    expect(rerunResult.published).toBe(true);
    expect(rerunResult.plan.sourceSha).toBe(plan.sourceSha);
    expect(rerunResult.plan.releases).toHaveLength(1);

    expect(git(cwd, "rev-parse", "HEAD")).toBe(releaseHead);
    expect(git(cwd, "rev-list", "--count", "HEAD")).toBe(commitCount);
  });

  it("preserves managed outputs when unrelated untracked files block release", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-untracked-"));
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
    mkdirSync(join(cwd, ".hooversion"));
    writeFileSync(join(cwd, ".hooversion", "outputs.json"), '{"published":true,"releases":[]}\n');
    writeFileSync(join(cwd, ".release-version"), "1.0.1\n");
    writeFileSync(join(cwd, "unrelated.txt"), "keep me\n");

    await expect(executeRelease(cwd, config, plan, { push: false, github: false })).rejects.toThrow(
      "Working tree must be clean before release",
    );
    expect(readFileSync(join(cwd, "unrelated.txt"), "utf8")).toBe("keep me\n");
    expect(existsSync(join(cwd, ".hooversion", "outputs.json"))).toBe(true);
    expect(existsSync(join(cwd, ".release-version"))).toBe(true);
  });

  it("blocks custom files inside outputDir while allowing managed outputs", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-custom-output-"));
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
    mkdirSync(join(cwd, ".hooversion"));
    writeFileSync(join(cwd, ".hooversion", "outputs.json"), '{"published":true,"releases":[]}\n');
    writeFileSync(join(cwd, ".release-version"), "1.0.1\n");
    writeFileSync(join(cwd, ".hooversion", "custom.txt"), "keep me\n");

    await expect(executeRelease(cwd, config, plan, { push: false, github: false })).rejects.toThrow(
      "Working tree must be clean before release",
    );
    expect(existsSync(join(cwd, ".hooversion", "outputs.json"))).toBe(true);
    expect(existsSync(join(cwd, ".release-version"))).toBe(true);
  });

  it("allows managed outputs alone before replacing them", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-managed-output-"));
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
    mkdirSync(join(cwd, ".hooversion"));
    writeFileSync(join(cwd, ".hooversion", "outputs.json"), '{"published":true,"releases":[]}\n');
    writeFileSync(join(cwd, ".release-version"), "1.0.1\n");

    const result = await executeRelease(cwd, config, plan, { push: false, github: false });
    expect(result.published).toBe(true);
    expect(readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8")).toContain('"published": true');
  });

  it("returns unpublished for a valid no-release plan", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-no-release-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0");
    writeFileSync(join(cwd, "app.ts"), "export const app = true;\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "chore: maintain app");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "app", path: ".", type: "node", manifest: "package.json" }],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);
    expect(plan.releases).toHaveLength(0);
    const result = await executeRelease(cwd, config, plan, { push: false, github: false });
    expect(result.published).toBe(false);
    expect(result.plan.releases).toHaveLength(0);
  });

  it("does not mutate managed outputs during a dry run", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-dry-run-"));
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
    mkdirSync(join(cwd, ".hooversion"));
    writeFileSync(join(cwd, ".hooversion", "outputs.json"), "managed outputs\n");
    writeFileSync(join(cwd, ".release-version"), "managed version\n");
    const outputsBefore = readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8");
    const versionBefore = readFileSync(join(cwd, ".release-version"), "utf8");

    const result = await executeRelease(cwd, config, plan, { dryRun: true, push: false, github: false });
    expect(result.published).toBe(false);

    expect(readFileSync(join(cwd, ".hooversion", "outputs.json"), "utf8")).toBe(outputsBefore);
    expect(readFileSync(join(cwd, ".release-version"), "utf8")).toBe(versionBefore);
  });

  it("retries publication after a post-push failure without a second commit", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-retry-"));
    const remote = mkdtempSync(join(tmpdir(), "hooversion-release-retry-remote-"));
    git(remote, "init", "--bare");
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "package.json"), JSON.stringify({ name: "app", version: "1.0.0" }, null, 2));
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "tag", "-a", "v1.0.0", "-m", "v1.0.0");
    git(cwd, "remote", "add", "origin", remote);
    git(cwd, "push", "origin", "main", "--tags");
    writeFileSync(join(cwd, "app.ts"), "export const app = true;\n");
    writeFileSync(join(cwd, "artifact.txt"), "artifact\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "fix: repair app");
    git(cwd, "push", "origin", "main");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "app", path: ".", type: "node", manifest: "package.json", assets: ["artifact.txt"] }],
      github: { releases: true, repository: "owner/repo", apiUrl: "https://api.example.test" },
      push: true,
    });
    const plan = createReleasePlan(cwd, config);
    const originalFetch = globalThis.fetch;
    let attempt = 0;
    const fetchMock: typeof fetch = Object.assign(
      async (_input: FetchInput, init?: FetchInit) => {
        const method = init?.method ?? "GET";
        if (attempt++ === 0) return new Response("", { status: 404 });
        if (method === "POST" && attempt === 2) {
          return new Response(
            JSON.stringify({
              id: 1,
              html_url: "https://github.example.test/releases/v1.0.1",
              upload_url: "https://api.example.test/uploads/{?name}",
              tag_name: "v1.0.1",
              name: "app 1.0.1",
              body: plan.releases[0].notes,
              draft: false,
              prerelease: false,
            }),
            { status: 201, headers: { "content-type": "application/json" } },
          );
        }
        if (attempt === 3) return new Response("upload failed", { status: 500, statusText: "Failure" });
        return new Response(
          JSON.stringify({
            id: 1,
            html_url: "https://github.example.test/releases/v1.0.1",
            upload_url: "https://api.example.test/uploads/{?name}",
            tag_name: "v1.0.1",
            name: "app 1.0.1",
            body: plan.releases[0].notes,
            draft: false,
            prerelease: false,
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      await expect(
        executeRelease(cwd, config, plan, { push: true, github: true, githubToken: "token" }),
      ).rejects.toThrow("GitHub API request failed");
      const releaseHead = git(cwd, "rev-parse", "HEAD");
      const commitCount = git(cwd, "rev-list", "--count", "HEAD");
      const rerunPlan = createReleasePlan(cwd, config);
      expect(rerunPlan.releases).toHaveLength(0);
      const result = await executeRelease(cwd, config, rerunPlan, {
        push: true,
        github: true,
        githubToken: "token",
      });
      expect(result.published).toBe(true);
      expect(rerunPlan.releases).toHaveLength(0);
      expect(result.plan.releases).toHaveLength(1);
      expect(result.plan.releases[0].package.name).toBe("app");
      expect(result.plan.releases[0].currentVersion).toBe("1.0.0");
      expect(result.plan.releases[0].nextVersion).toBe("1.0.1");
      expect(result.plan.releases[0].tag).toBe("v1.0.1");
      expect(git(cwd, "tag", "--list", "v1.0.1")).toBe("v1.0.1");
      expect(git(cwd, "rev-parse", "HEAD")).toBe(releaseHead);
      expect(git(cwd, "rev-list", "--count", "HEAD")).toBe(commitCount);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("updates plain version-file manifests", async () => {
    const cwd = mkdtempSync(join(tmpdir(), "hooversion-release-version-file-"));
    git(cwd, "init", "-b", "main");
    git(cwd, "config", "user.email", "test@example.com");
    git(cwd, "config", "user.name", "Hooversion Test");
    writeFileSync(join(cwd, "version"), "2.4.0\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "initial import");
    git(cwd, "tag", "-a", "v2.4.0", "-m", "v2.4.0");
    writeFileSync(join(cwd, "image.txt"), "metadata\n");
    git(cwd, "add", "--all");
    git(cwd, "commit", "-m", "feat(image): add runtime metadata");

    const config = normalizeConfig(cwd, {
      packages: [{ name: "image", path: ".", type: "version-file", manifest: "version" }],
      github: false,
      push: false,
    });
    const plan = createReleasePlan(cwd, config);
    await executeRelease(cwd, config, plan, { push: false, github: false });

    expect(readFileSync(join(cwd, "version"), "utf8")).toBe("2.5.0\n");
    expect(readFileSync(join(cwd, "CHANGELOG.md"), "utf8")).toContain("## 2.5.0");
    expect(git(cwd, "tag", "--list", "v2.5.0")).toBe("v2.5.0");
  });
});

function git(cwd: string, ...args: string[]): string {
  return execFileSync("git", args, { cwd, encoding: "utf8" }).trim();
}
