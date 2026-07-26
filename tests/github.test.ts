import { mkdirSync, mkdtempSync, renameSync, rmSync, symlinkSync, truncateSync, writeFileSync } from "node:fs";
import * as fsPromises from "node:fs/promises";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { describe, expect, it, spyOn } from "bun:test";
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
      package: { ...release.package, assets: ["app.tar.gz", "app.zip"] },
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
      package: { ...release.package, assets: ["app.tar.gz", "app.zip"] },
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
      package: { ...release.package, assets: ["app.tar.gz", "app.zip"] },
    };
    const requests: string[] = [];
    const uploadBodies: string[] = [];
    const fetchMock: typeof fetch = Object.assign(
      async (input: FetchInput, init?: FetchInit) => {
        const url = String(input);
        requests.push(url);
        if (url.includes("/assets?")) uploadBodies.push(await new Response(init?.body).text());
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
      expect(uploadBodies).toEqual(["archive", "archive"]);
    } finally {
      globalThis.fetch = originalFetch;
      rmSync(assetDir, { recursive: true, force: true });
    }
  });

  it("rejects unsafe configured assets before any upload", async () => {
    const originalFetch = globalThis.fetch;
    const cases = [
      { label: "absolute paths", asset: "/etc/passwd", prepare: (_root: string) => undefined },
      { label: "parent traversal", asset: "../outside.bin", prepare: (_root: string) => undefined },
      {
        label: "symbolic links",
        asset: "asset-link",
        prepare: (root: string) => symlinkSync("/etc/passwd", join(root, "asset-link")),
      },
      {
        label: "directories",
        asset: "asset-dir",
        prepare: (root: string) => mkdirSync(join(root, "asset-dir")),
      },
      {
        label: "oversized files",
        asset: "asset.bin",
        prepare: (root: string) => {
          const assetPath = join(root, "asset.bin");
          writeFileSync(assetPath, "");
          truncateSync(assetPath, 100 * 1024 * 1024 + 1);
        },
      },
    ] as const;

    try {
      for (const testCase of cases) {
        const assetDir = mkdtempSync(join(tmpdir(), "hooversion-github-"));
        testCase.prepare(assetDir);
        let uploadCount = 0;
        const fetchMock: typeof fetch = Object.assign(
          async (input: FetchInput, _init?: FetchInit) => {
            if (String(input).includes("/assets?")) uploadCount += 1;
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
                assets: [],
              }),
              { status: 200, headers: { "content-type": "application/json" } },
            );
          },
          { preconnect: originalFetch.preconnect },
        );
        globalThis.fetch = fetchMock;

        try {
          const releaseWithAsset = {
            ...release,
            package: { ...release.package, assets: [testCase.asset] },
          };
          await expect(publishGitHubRelease(assetDir, config, releaseWithAsset, { token: "token" })).rejects.toThrow();
          expect(uploadCount, testCase.label).toBe(0);
        } finally {
          rmSync(assetDir, { recursive: true, force: true });
        }
      }
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("rejects a file swapped between validation and descriptor open before any upload", async () => {
    const originalFetch = globalThis.fetch;
    const assetDir = mkdtempSync(join(tmpdir(), "hooversion-github-"));
    const assetPath = join(assetDir, "asset.bin");
    writeFileSync(assetPath, "original");
    let uploadCount = 0;
    const fetchMock: typeof fetch = Object.assign(
      async (input: FetchInput, _init?: FetchInit) => {
        if (String(input).includes("/assets?")) uploadCount += 1;
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
            assets: [],
          }),
          { status: 200, headers: { "content-type": "application/json" } },
        );
      },
      { preconnect: originalFetch.preconnect },
    );
    const originalOpen = fsPromises.open.bind(fsPromises);
    const openSpy = spyOn(fsPromises, "open");
    openSpy.mockImplementation(async (path, flags, mode) => {
      if (String(path) === assetPath) {
        const replacement = `${assetPath}.replacement`;
        renameSync(assetPath, replacement);
        writeFileSync(assetPath, "replacement");
      }
      return originalOpen(path, flags as never, mode as never);
    });
    globalThis.fetch = fetchMock;

    try {
      const releaseWithAsset = {
        ...release,
        package: { ...release.package, assets: ["asset.bin"] },
      };
      await expect(publishGitHubRelease(assetDir, config, releaseWithAsset, { token: "token" })).rejects.toThrow(
        "changed while it was being read",
      );
      expect(uploadCount).toBe(0);
    } finally {
      openSpy.mockRestore();
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
