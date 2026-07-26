import { mkdtempSync, rmSync, writeFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it } from "bun:test";
import { publishGitHubRelease } from "../src/github";
import type { NormalizedConfig, PackageRelease } from "../src/types";
type FetchInput = Parameters<typeof fetch>[0];
type FetchInit = Parameters<typeof fetch>[1];

describe("GitHub release publication", () => {
  const release: PackageRelease = {
    tag: "v1.0.1",
    currentVersion: "1.0.0",
    nextVersion: "1.0.1",
    releaseType: "patch",
    commits: [],
    notes: "Release notes",
    changelogPath: "CHANGELOG.md",
    dependencyTriggered: false,
    package: {
      name: "app",
      path: ".",
      type: "node",
      manifest: "package.json",
      changelog: "CHANGELOG.md",
      scopes: [],
      dependencies: [],
      assets: [],
    },
  };
  const config = {
    github: { releases: true, repository: "owner/repo", apiUrl: "https://api.github.com" },
  } as NormalizedConfig;

  it("uploads only missing assets for a matching existing release", async () => {
    const originalFetch = globalThis.fetch;
    const assetDir = mkdtempSync(join(tmpdir(), "hooversion-github-"));
    writeFileSync(join(assetDir, "app.tar.gz"), "existing");
    writeFileSync(join(assetDir, "app.zip"), "missing");
    const releaseWithAssets = {
      ...release,
      package: { ...release.package, assets: [join(assetDir, "app.tar.gz"), join(assetDir, "app.zip")] },
    };
    const requests: string[] = [];
    const fetchMock: typeof fetch = Object.assign(
      async (input: FetchInput, _init?: FetchInit) => {
        const url = String(input);
        requests.push(url);
        return new Response(
          JSON.stringify(
            url.includes("/assets?")
              ? { id: 2 }
              : {
                  id: 1,
                  html_url: "https://github.com/owner/repo/releases/v1.0.1",
                  upload_url: "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
                  tag_name: "v1.0.1",
                  name: "app 1.0.1",
                  body: "Release notes",
                  draft: false,
                  prerelease: false,
                  assets: [{ name: "app.tar.gz" }],
                },
          ),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      await expect(publishGitHubRelease(assetDir, config, releaseWithAssets, { token: "token" })).resolves.toBe(
        "https://github.com/owner/repo/releases/v1.0.1",
      );
      expect(requests).toHaveLength(2);
      expect(requests[0]).toContain("/releases/tags/v1.0.1");
      expect(requests[1]).toContain("name=app.zip");
      expect(requests[1]).not.toContain("app.tar.gz");
    } finally {
      globalThis.fetch = originalFetch;
      rmSync(assetDir, { recursive: true, force: true });
    }
  });

  it("uploads no assets when a matching existing release already has them all", async () => {
    const originalFetch = globalThis.fetch;
    const assetDir = mkdtempSync(join(tmpdir(), "hooversion-github-"));
    writeFileSync(join(assetDir, "app.tar.gz"), "existing");
    writeFileSync(join(assetDir, "app.zip"), "existing");
    const releaseWithAssets = {
      ...release,
      package: { ...release.package, assets: [join(assetDir, "app.tar.gz"), join(assetDir, "app.zip")] },
    };
    let requestCount = 0;
    const fetchMock: typeof fetch = Object.assign(
      async (_input: FetchInput, _init?: FetchInit) => {
        requestCount += 1;
        return new Response(
          JSON.stringify({
            id: 1,
            html_url: "https://github.com/owner/repo/releases/v1.0.1",
            upload_url: "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
            tag_name: "v1.0.1",
            name: "app 1.0.1",
            body: "Release notes",
            draft: false,
            prerelease: false,
            assets: [{ name: "app.tar.gz" }, { name: "app.zip" }],
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      await expect(publishGitHubRelease(assetDir, config, releaseWithAssets, { token: "token" })).resolves.toBe(
        "https://github.com/owner/repo/releases/v1.0.1",
      );
      expect(requestCount).toBe(1);
    } finally {
      globalThis.fetch = originalFetch;
      rmSync(assetDir, { recursive: true, force: true });
    }
  });

  it("creates a release and uploads every configured asset", async () => {
    const originalFetch = globalThis.fetch;
    const assetDir = mkdtempSync(join(tmpdir(), "hooversion-github-"));
    writeFileSync(join(assetDir, "app.tar.gz"), "archive");
    writeFileSync(join(assetDir, "app.zip"), "archive");
    const releaseWithAssets = {
      ...release,
      package: { ...release.package, assets: [join(assetDir, "app.tar.gz"), join(assetDir, "app.zip")] },
    };
    const requests: string[] = [];
    const fetchMock: typeof fetch = Object.assign(
      async (input: FetchInput, init?: FetchInit) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/releases/tags/")) return new Response("missing", { status: 404 });
        if (init?.method === "POST" && url.endsWith("/releases")) {
          return new Response(
            JSON.stringify({
              id: 1,
              html_url: "https://github.com/owner/repo/releases/v1.0.1",
              upload_url: "https://uploads.github.com/repos/owner/repo/releases/1/assets{?name,label}",
            }),
            { status: 201, headers: { "content-type": "application/json" } },
          );
        }
        return new Response(JSON.stringify({ id: 2 }), {
          status: 201,
          headers: { "content-type": "application/json" },
        });
      },
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      await expect(publishGitHubRelease(assetDir, config, releaseWithAssets, { token: "token" })).resolves.toBe(
        "https://github.com/owner/repo/releases/v1.0.1",
      );
      expect(requests).toHaveLength(4);
      expect(requests[1]).toContain("/releases");
      expect(requests[2]).toContain("name=app.tar.gz");
      expect(requests[3]).toContain("name=app.zip");
    } finally {
      globalThis.fetch = originalFetch;
      rmSync(assetDir, { recursive: true, force: true });
    }
  });

  it("rejects an untrusted upload URL before reading asset data", async () => {
    const originalFetch = globalThis.fetch;
    let requestCount = 0;
    const fetchMock: typeof fetch = Object.assign(
      async (_input: FetchInput, _init?: FetchInit) => {
        requestCount += 1;
        return new Response(
          JSON.stringify({
            id: 1,
            html_url: "https://github.com/owner/repo/releases/v1.0.1",
            upload_url: "https://attacker.example/repos/owner/repo/releases/1/assets{?name,label}",
            tag_name: "v1.0.1",
            name: "app 1.0.1",
            body: "Release notes",
            draft: false,
            prerelease: false,
            assets: [],
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      const releaseWithMissingAsset = {
        ...release,
        package: { ...release.package, assets: ["does-not-exist.tar.gz"] },
      };
      await expect(publishGitHubRelease("/tmp", config, releaseWithMissingAsset, { token: "token" })).rejects.toThrow(
        "Untrusted GitHub release upload URL",
      );
      expect(requestCount).toBe(1);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("rejects an existing release with different metadata", async () => {
    const originalFetch = globalThis.fetch;
    const fetchMock: typeof fetch = Object.assign(
      async (_input: FetchInput, _init?: FetchInit) =>
        new Response(
          JSON.stringify({
            id: 1,
            html_url: "https://github.example.test/releases/v1.0.1",
            upload_url: "https://uploads.example.test/{?name}",
            tag_name: "v1.0.1",
            name: "wrong name",
            body: "Release notes",
            draft: false,
            prerelease: false,
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        ),
      { preconnect: originalFetch.preconnect },
    );
    globalThis.fetch = fetchMock;

    try {
      await expect(publishGitHubRelease("/tmp", config, release, { token: "token" })).rejects.toThrow(
        "different metadata",
      );
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
});
