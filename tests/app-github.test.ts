import { describe, expect, it } from "bun:test";
import {
  completeReleaseCheckRun,
  createReleaseCheckRun,
  releaseCheckResult,
  releaseFailureCheckResult,
} from "../src/app-github";
import { validateCloneUrl } from "../src/app-runner";
import type { VersionhooReleaseResult } from "../src/app-runner";

type FetchInput = Parameters<typeof fetch>[0];
type FetchInit = Parameters<typeof fetch>[1];

describe("Versionhoo GitHub check reporting", () => {
  it("renders published, no-op, stale, and failed check results", () => {
    expect(releaseCheckResult(result({ outcome: "published", published: true })).title).toBe(
      "Versionhoo published releases",
    );
    expect(releaseCheckResult(result({ outcome: "no_release", published: false })).conclusion).toBe("neutral");
    expect(releaseCheckResult(result({ outcome: "stale", published: false, message: "stale branch" })).summary).toBe(
      "stale branch",
    );
    expect(releaseFailureCheckResult(new Error("push rejected"))).toMatchObject({
      conclusion: "failure",
      title: "Versionhoo release failed",
      summary: "push rejected",
    });
  });
  it("sends authorized checks only to the bound github.com repository", async () => {
    const originalFetch: typeof fetch = globalThis.fetch;
    let request: Request | undefined;
    const mockFetch = async (input: FetchInput, init?: FetchInit): Promise<Response> => {
      request = input instanceof Request ? new Request(input, init) : new Request(input.toString(), init);
      return new Response(JSON.stringify({ id: 17, html_url: "https://github.com/openhoo/app/checks/17" }), {
        status: 201,
      });
    };
    globalThis.fetch = Object.assign(mockFetch, { preconnect: originalFetch.preconnect });
    try {
      await expect(
        createReleaseCheckRun("https://api.github.com", "openhoo/app", "secret-token", "abc", "openhoo/app", []),
      ).resolves.toEqual({
        id: 17,
        htmlUrl: "https://github.com/openhoo/app/checks/17",
      });
      expect(request?.url).toBe("https://api.github.com/repos/openhoo/app/check-runs");
      expect(request?.headers.get("authorization")).toBe("Bearer secret-token");
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("rejects attacker API hosts and repository mismatches before sending tokens", async () => {
    const originalFetch: typeof fetch = globalThis.fetch;
    let called = false;
    const mockFetch = async (_input: FetchInput, _init?: FetchInit): Promise<Response> => {
      called = true;
      return new Response("unexpected", { status: 500 });
    };
    globalThis.fetch = Object.assign(mockFetch, { preconnect: originalFetch.preconnect });
    try {
      await expect(
        createReleaseCheckRun("https://attacker.example/api", "openhoo/app", "secret-token", "abc", "openhoo/app", []),
      ).rejects.toThrow("Untrusted GitHub API URL");
      await expect(
        completeReleaseCheckRun(
          "https://api.github.com",
          "evil/repository",
          "secret-token",
          17,
          "failure",
          "failed",
          "summary",
          "openhoo/app",
          [],
        ),
      ).rejects.toThrow("GitHub repository mismatch");
      expect(called).toBe(false);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("validates clone scheme, host, and repository identity before credentials", () => {
    expect(validateCloneUrl("https://github.com/openhoo/app.git", "openhoo/app")).toBe(
      "https://github.com/openhoo/app.git",
    );
    expect(() => validateCloneUrl("https://attacker.example/openhoo/app.git", "openhoo/app")).toThrow(
      "Untrusted GitHub clone host",
    );
    expect(() => validateCloneUrl("https://github.com/evil/app.git", "openhoo/app")).toThrow(
      "GitHub clone repository mismatch",
    );
    expect(() => validateCloneUrl("https://x-access-token:secret@github.com/openhoo/app.git", "openhoo/app")).toThrow(
      "Invalid GitHub clone URL",
    );
  });
});

function result(overrides: Partial<VersionhooReleaseResult>): VersionhooReleaseResult {
  return {
    repositoryFullName: "openhoo/app",
    branch: "main",
    headSha: "abc123",
    workDir: "/tmp/versionhoo",
    outcome: "no_release",
    published: false,
    releases: [{ name: "app", version: "1.0.1", tag: "v1.0.1" }],
    ...overrides,
  };
}
