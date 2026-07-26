import { createHmac, createSign, timingSafeEqual } from "node:crypto";
import { readFileSync } from "node:fs";
import { HooversionError } from "./errors";

export interface GitHubAppAuthConfig {
  appId: string | number;
  privateKey: string;
  apiUrl?: string;
  trustedApiUrls?: string[];
}

export interface InstallationToken {
  token: string;
  expiresAt: string;
}

interface GitHubInstallationTokenResponse {
  token: string;
  expires_at: string;
}

const githubApiVersion = "2022-11-28";

export function readGitHubAppPrivateKey(env: Record<string, string | undefined> = process.env): string {
  const inline = env.VERSIONHOO_PRIVATE_KEY ?? env.HOOVERSION_PRIVATE_KEY;
  if (inline) return normalizePrivateKey(inline);

  const path = env.VERSIONHOO_PRIVATE_KEY_PATH ?? env.HOOVERSION_PRIVATE_KEY_PATH;
  if (path) return readFileSync(path, "utf8");

  throw new HooversionError(
    "VERSIONHOO_PRIVATE_KEY or VERSIONHOO_PRIVATE_KEY_PATH is required to authenticate as a GitHub App.",
  );
}

export function createGitHubAppJwt(
  config: GitHubAppAuthConfig,
  nowSeconds = Math.floor(Date.now() / 1000),
): string {
  const header = base64UrlJson({ alg: "RS256", typ: "JWT" });
  const payload = base64UrlJson({
    iat: nowSeconds - 60,
    exp: nowSeconds + 9 * 60,
    iss: String(config.appId),
  });
  const signingInput = `${header}.${payload}`;
  const signature = createSign("RSA-SHA256").update(signingInput).sign(config.privateKey);
  return `${signingInput}.${base64Url(signature)}`;
}
export async function createInstallationAccessToken(
  config: GitHubAppAuthConfig,
  installationId: number,
  repository: { id: number; fullName: string },
): Promise<InstallationToken> {
  validateRepositoryFullName(repository.fullName);
  if (!Number.isSafeInteger(installationId) || installationId <= 0) {
    throw new HooversionError("GitHub App installation id must be a positive integer.");
  }
  if (!Number.isSafeInteger(repository.id) || repository.id <= 0) {
    throw new HooversionError("GitHub webhook repository id must be a positive integer.");
  }
  const apiUrl = validateGitHubApiUrl(config.apiUrl ?? "https://api.github.com", config.trustedApiUrls);
  const jwt = createGitHubAppJwt(config);
  const response = await fetch(`${apiUrl}/app/installations/${installationId}/access_tokens`, {
    method: "POST",
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${jwt}`,
      "content-type": "application/json",
      "user-agent": "versionhoo-app",
      "x-github-api-version": githubApiVersion,
    },
    body: JSON.stringify({ repository_ids: [repository.id] }),
  });

  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(
      `GitHub App installation token request failed (${response.status} ${response.statusText}): ${body}`,
    );
  }

  const data = (await response.json()) as GitHubInstallationTokenResponse;
  return { token: data.token, expiresAt: data.expires_at };
}

export function verifyGitHubWebhookSignature(secret: string, body: string, signatureHeader: string | null): boolean {
  if (!signatureHeader?.startsWith("sha256=")) return false;

  const expected = Buffer.from(`sha256=${createHmac("sha256", secret).update(body).digest("hex")}`, "utf8");
  const actual = Buffer.from(signatureHeader, "utf8");
  return actual.length === expected.length && timingSafeEqual(actual, expected);
}
export function validateGitHubApiUrl(apiUrl: string, trustedApiUrls: readonly string[] = []): string {
  const parsed = parseHttpsUrl(apiUrl, "GitHub API");
  const normalized = normalizeOrigin(parsed);
  const trusted = trustedApiUrls.map((value) => normalizeOrigin(parseHttpsUrl(value, "trusted GitHub API")));
  if (parsed.hostname !== "api.github.com" || parsed.pathname !== "/") {
    if (!trusted.includes(normalized)) {
      throw new HooversionError(`Untrusted GitHub API URL: ${apiUrl}`);
    }
  }
  return normalized.endsWith("/") ? normalized.slice(0, -1) : normalized;
}

export function validateRepositoryFullName(repository: string): string {
  const parts = repository.split("/");
  if (
    parts.length !== 2 ||
    parts.some((part) => !/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(part)) ||
    parts.some((part) => part === "." || part === "..")
  ) {
    throw new HooversionError(`Invalid GitHub repository identity: ${repository}`);
  }
  return `${parts[0]}/${parts[1]}`;
}

function parseHttpsUrl(value: string, label: string): URL {
  let parsed: URL;
  try {
    parsed = new URL(value);
  } catch {
    throw new HooversionError(`Invalid ${label} URL: ${value}`);
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.port || parsed.search || parsed.hash) {
    throw new HooversionError(`Invalid ${label} URL: ${value}`);
  }
  return parsed;
}

function normalizeOrigin(value: URL): string {
  return `${value.protocol}//${value.host}${value.pathname.replace(/\/+$/, "") || "/"}`;
}


function normalizePrivateKey(value: string): string {
  return value.includes("\\n") ? value.replaceAll("\\n", "\n") : value;
}

function base64UrlJson(value: unknown): string {
  return base64Url(Buffer.from(JSON.stringify(value), "utf8"));
}

function base64Url(value: Buffer): string {
  return value.toString("base64").replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}
