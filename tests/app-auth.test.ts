import { describe, expect, it } from "bun:test";
import { createHmac, generateKeyPairSync } from "node:crypto";
import {
  createGitHubAppJwt,
  createInstallationAccessToken,
  validateGitHubApiUrl,
  verifyGitHubWebhookSignature,
} from "../src/app-auth";

type FetchInput = Parameters<typeof fetch>[0];
type FetchInit = Parameters<typeof fetch>[1];

describe("GitHub App auth", () => {
  it("creates a GitHub App JWT with the app id issuer", () => {
    const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
    const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
    const jwt = createGitHubAppJwt({ appId: 12345, privateKey: pem }, 1_700_000_000);
    const [header, payload, signature] = jwt.split(".");

    expect(JSON.parse(decodeBase64Url(header))).toEqual({ alg: "RS256", typ: "JWT" });
    expect(JSON.parse(decodeBase64Url(payload))).toEqual({
      iat: 1_699_999_940,
      exp: 1_700_000_540,
      iss: "12345",
    });
    expect(signature.length).toBeGreaterThan(32);
  });

  it("verifies GitHub webhook signatures using sha256 HMAC", () => {
    const secret = "webhook-secret";
    const body = JSON.stringify({ zen: "Practicality beats purity." });
    const signature = `sha256=${createHmac("sha256", secret).update(body).digest("hex")}`;

    expect(verifyGitHubWebhookSignature(secret, body, signature)).toBe(true);
    expect(verifyGitHubWebhookSignature(secret, `${body}\n`, signature)).toBe(false);
    expect(verifyGitHubWebhookSignature(secret, body, "sha1=bad")).toBe(false);
  });
  it("scopes an installation token request to the validated repository id", async () => {
    const { privateKey } = generateKeyPairSync("rsa", { modulusLength: 2048 });
    const pem = privateKey.export({ type: "pkcs8", format: "pem" }).toString();
    const originalFetch: typeof fetch = globalThis.fetch;
    let request: Request | undefined;
    const mockFetch = async (input: FetchInput, init?: FetchInit): Promise<Response> => {
      request = input instanceof Request ? new Request(input, init) : new Request(input.toString(), init);
      return new Response(JSON.stringify({ token: "installation-token", expires_at: "tomorrow" }), { status: 201 });
    };
    globalThis.fetch = Object.assign(mockFetch, { preconnect: originalFetch.preconnect });
    try {
      await expect(
        createInstallationAccessToken({ appId: "123", privateKey: pem }, 42, {
          id: 987,
          fullName: "openhoo/app",
        }),
      ).resolves.toEqual({ token: "installation-token", expiresAt: "tomorrow" });
      expect(request?.url).toBe("https://api.github.com/app/installations/42/access_tokens");
      expect(await request?.text()).toBe(JSON.stringify({ repository_ids: [987] }));
    } finally {
      globalThis.fetch = originalFetch;
    }
  });

  it("rejects an untrusted API host and malformed repository before token request", async () => {
    const originalFetch: typeof fetch = globalThis.fetch;
    let called = false;
    const mockFetch = async (_input: FetchInput, _init?: FetchInit): Promise<Response> => {
      called = true;
      return new Response("unexpected", { status: 500 });
    };
    globalThis.fetch = Object.assign(mockFetch, { preconnect: originalFetch.preconnect });
    try {
      expect(() => validateGitHubApiUrl("https://attacker.example/api")).toThrow("Untrusted GitHub API URL");
      await expect(
        createInstallationAccessToken(
          { appId: "123", privateKey: "not-used", apiUrl: "https://attacker.example/api" },
          42,
          { id: 987, fullName: "openhoo/../app" },
        ),
      ).rejects.toThrow("Invalid GitHub repository identity");
      expect(called).toBe(false);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
  it("does not send an installation token request to an untrusted API URL", async () => {
    const originalFetch: typeof fetch = globalThis.fetch;
    let called = false;
    const mockFetch = async (_input: FetchInput, _init?: FetchInit): Promise<Response> => {
      called = true;
      return new Response("unexpected", { status: 500 });
    };
    globalThis.fetch = Object.assign(mockFetch, { preconnect: originalFetch.preconnect });
    try {
      await expect(
        createInstallationAccessToken(
          { appId: "123", privateKey: "not-used", apiUrl: "https://attacker.example/api" },
          42,
          { id: 987, fullName: "openhoo/app" },
        ),
      ).rejects.toThrow("Untrusted GitHub API URL");
      expect(called).toBe(false);
    } finally {
      globalThis.fetch = originalFetch;
    }
  });
  it("permits an enterprise API only when explicitly trusted", () => {
    expect(
      validateGitHubApiUrl("https://github.enterprise.example/api/v3/", ["https://github.enterprise.example/api/v3"]),
    ).toBe("https://github.enterprise.example/api/v3");
  });
});

function decodeBase64Url(value: string): string {
  const padded = `${value}${"=".repeat((4 - (value.length % 4)) % 4)}`;
  return Buffer.from(padded.replaceAll("-", "+").replaceAll("_", "/"), "base64").toString("utf8");
}
