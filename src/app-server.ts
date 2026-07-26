import { readGitHubAppPrivateKey, verifyGitHubWebhookSignature, createInstallationAccessToken } from "./app-auth";
import { runVersionhooRelease, type VersionhooReleaseJob, type VersionhooReleaseResult } from "./app-runner";
import {
  completeReleaseCheckRun,
  createReleaseCheckRun,
  releaseCheckResult,
  releaseFailureCheckResult,
  type CheckRun,
} from "./app-github";
import { HooversionError } from "./errors";

export interface VersionhooAppConfig {
  appId: string;
  privateKey: string;
  webhookSecret: string;
  apiUrl: string;
  trustedApiUrls: string[];
  trustedCloneHosts: string[];
  host: string;
  port: number;
  workDir?: string;
  configPath?: string;
  installCommand?: string;
  allowedRepositories: string[];
  releaseBranches: string[];
  ciWorkflowNames: string[];
  gitAuthorName?: string;
  gitAuthorEmail?: string;
  keepWorkDir?: boolean;
  webhookMaxBodyBytes?: number;
}

export interface VersionhooWebhookResult {
  status: "accepted" | "ignored";
  reason?: string;
}

interface GitHubRepositoryPayload {
  id: number;
  full_name: string;
  clone_url: string;
  default_branch: string;
}

interface GitHubInstallationPayload {
  id: number;
}

interface WorkflowRunPayload {
  action: string;
  repository: GitHubRepositoryPayload;
  installation?: GitHubInstallationPayload;
  workflow_run: {
    name: string;
    event: string;
    conclusion: string | null;
    head_branch: string | null;
    head_sha: string;
    id?: number;
    head_commit?: {
      message?: string;
    } | null;
    head_repository?: {
      full_name?: string;
    } | null;
  };
}

type DedupeState = "in_flight" | "succeeded";

interface DedupeEntry {
  state: DedupeState;
  expiresAt: number;
}

export const DEFAULT_WEBHOOK_MAX_BODY_BYTES = 1024 * 1024;
function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}


type ReleaseRunner = (job: VersionhooReleaseJob) => Promise<VersionhooReleaseResult>;
interface ReleaseTaskQueueOptions {
  maxAttempts?: number;
  retryDelayMs?: number;
}

export class ReleaseTaskQueue {
  private tails = new Map<string, Promise<void>>();
  private lastFailure: unknown;
  private readonly onFailure: (error: unknown) => void;
  private readonly maxAttempts: number;
  private readonly retryDelayMs: number;

  constructor(
    onFailure: (error: unknown) => void = (error) => {
      console.error(error);
    },
    options: ReleaseTaskQueueOptions = {},
  ) {
    this.onFailure = onFailure;
    this.maxAttempts = Math.max(1, Math.min(3, Math.floor(options.maxAttempts ?? 1)));
    this.retryDelayMs = Math.max(0, Math.min(30_000, Math.floor(options.retryDelayMs ?? 0)));
  }

  enqueue(
    key: string,
    task: () => Promise<void>,
    onFinalFailure?: (error: unknown) => void,
  ): Promise<void> {
    const previous = this.tails.get(key) ?? Promise.resolve();
    const next = previous
      .then(() => this.runWithRetry(task), () => this.runWithRetry(task))
      .catch((error) => {
        this.lastFailure = error;
        try {
          onFinalFailure?.(error);
          this.onFailure(error);
        } catch (callbackError) {
          this.lastFailure = callbackError;
        }
      })
      .finally(() => {
        if (this.tails.get(key) === next) this.tails.delete(key);
      });
    this.tails.set(key, next);
    return next;
  }

  get failure(): unknown {
    return this.lastFailure;
  }

  private async runWithRetry(task: () => Promise<void>): Promise<void> {
    for (let attempt = 1; ; attempt += 1) {
      try {
        await task();
        return;
      } catch (error) {
        if (attempt >= this.maxAttempts) throw error;
        if (this.retryDelayMs > 0) {
          const { promise, resolve } = Promise.withResolvers<void>();
          setTimeout(resolve, this.retryDelayMs);
          await promise;
        }
      }
    }
  }
}

export class WebhookDeduper {
  private seen = new Map<string, DedupeEntry>();

  constructor(
    private readonly ttlMs = 24 * 60 * 60 * 1000,
    private readonly now = () => Date.now(),
  ) {}

  reserve(key: string | undefined): boolean {
    if (!key) return true;
    this.prune();
    if (this.seen.has(key)) return false;
    this.seen.set(key, { state: "in_flight", expiresAt: this.now() + this.ttlMs });
    return true;
  }

  succeed(key: string | undefined): void {
    if (!key) return;
    const entry = this.seen.get(key);
    if (entry) {
      entry.state = "succeeded";
      entry.expiresAt = this.now() + this.ttlMs;
    }
  }

  release(key: string | undefined): void {
    if (key) this.seen.delete(key);
  }

  remember(key: string | undefined): boolean {
    const reserved = this.reserve(key);
    if (reserved) this.succeed(key);
    return reserved;
  }

  private prune(): void {
    const now = this.now();
    for (const [key, entry] of this.seen) {
      if (entry.expiresAt <= now) this.seen.delete(key);
    }
  }
}


export function loadVersionhooAppConfigFromEnv(
  env: Record<string, string | undefined> = process.env,
): VersionhooAppConfig {
  const appId = readRequiredEnv(env, ["VERSIONHOO_APP_ID", "HOOVERSION_APP_ID"]);
  const webhookSecret = readRequiredEnv(env, ["VERSIONHOO_WEBHOOK_SECRET", "HOOVERSION_WEBHOOK_SECRET"]);
  const port = Number(readEnv(env, ["VERSIONHOO_PORT", "HOOVERSION_PORT"]) ?? "3000");
  if (!Number.isInteger(port) || port <= 0) {
    throw new HooversionError("VERSIONHOO_PORT must be a positive integer.");
  }
  const webhookMaxBodyBytes = Number(
    readEnv(env, ["VERSIONHOO_WEBHOOK_MAX_BODY_BYTES", "HOOVERSION_WEBHOOK_MAX_BODY_BYTES"]) ??
      String(DEFAULT_WEBHOOK_MAX_BODY_BYTES),
  );
  if (!Number.isInteger(webhookMaxBodyBytes) || webhookMaxBodyBytes <= 0) {
    throw new HooversionError("VERSIONHOO_WEBHOOK_MAX_BODY_BYTES must be a positive integer.");
  }
  const apiUrl = readEnv(env, ["VERSIONHOO_GITHUB_API_URL", "HOOVERSION_GITHUB_API_URL"]) ?? "https://api.github.com";
  const trustedApiUrls = splitCsv(
    readEnv(env, [
      "VERSIONHOO_TRUSTED_GITHUB_API_URLS",
      "HOOVERSION_TRUSTED_GITHUB_API_URLS",
      "VERSIONHOO_TRUSTED_API_URLS",
      "HOOVERSION_TRUSTED_API_URLS",
    ]),
  );
  const trustedCloneHosts = splitCsv(
    readEnv(env, [
      "VERSIONHOO_TRUSTED_GITHUB_CLONE_HOSTS",
      "HOOVERSION_TRUSTED_GITHUB_CLONE_HOSTS",
      "VERSIONHOO_TRUSTED_CLONE_HOSTS",
      "HOOVERSION_TRUSTED_CLONE_HOSTS",
    ]),
  );

  return {
    appId,
    privateKey: readGitHubAppPrivateKey(env),
    webhookSecret,
    apiUrl,
    trustedApiUrls,
    trustedCloneHosts,
    host: readEnv(env, ["VERSIONHOO_HOST", "HOOVERSION_HOST"]) ?? "0.0.0.0",
    port,
    workDir: readEnv(env, ["VERSIONHOO_WORKDIR", "HOOVERSION_WORKDIR"]),
    configPath: readEnv(env, ["VERSIONHOO_CONFIG", "HOOVERSION_CONFIG"]),
    installCommand: readEnv(env, ["VERSIONHOO_INSTALL_COMMAND", "HOOVERSION_INSTALL_COMMAND"]),
    allowedRepositories: splitCsv(readEnv(env, ["VERSIONHOO_ALLOWED_REPOS", "HOOVERSION_ALLOWED_REPOS"])),
    releaseBranches: splitCsv(readEnv(env, ["VERSIONHOO_RELEASE_BRANCHES", "HOOVERSION_RELEASE_BRANCHES"]) ?? "main"),
    ciWorkflowNames: splitCsv(readEnv(env, ["VERSIONHOO_CI_WORKFLOWS", "HOOVERSION_CI_WORKFLOWS"]) ?? "CI"),
    gitAuthorName: readEnv(env, ["VERSIONHOO_GIT_AUTHOR_NAME", "HOOVERSION_GIT_AUTHOR_NAME"]),
    gitAuthorEmail: readEnv(env, ["VERSIONHOO_GIT_AUTHOR_EMAIL", "HOOVERSION_GIT_AUTHOR_EMAIL"]),
    keepWorkDir: readBoolean(readEnv(env, ["VERSIONHOO_KEEP_WORKDIR", "HOOVERSION_KEEP_WORKDIR"])),
    webhookMaxBodyBytes,
  };

}

export function startVersionhooApp(config: VersionhooAppConfig): Bun.Server<undefined> {
  const queue = new ReleaseTaskQueue();
  const deduper = new WebhookDeduper();
  const handler = createVersionhooWebhookHandler(config, runVersionhooRelease, queue, deduper);
  const server = Bun.serve({
    hostname: config.host,
    port: config.port,
    async fetch(request) {
      const url = new URL(request.url);
      if (request.method === "GET" && url.pathname === "/health") {
        return json({ ok: true });
      }

      if (request.method === "POST" && url.pathname === "/webhooks/github") {
        return handler(request);
      }

      return json({ error: "not found" }, 404);
    },
  });
  console.log(`versionhoo app listening on http://${server.hostname}:${server.port}`);
  return server;
}

export function createVersionhooWebhookHandler(
  config: VersionhooAppConfig,
  runner: ReleaseRunner,
  queue = new ReleaseTaskQueue(),
  deduper = new WebhookDeduper(),
): (request: Request) => Promise<Response> {
  return async (request) => {
    const event = request.headers.get("x-github-event");
    const delivery = request.headers.get("x-github-delivery") ?? "unknown";
    const maxBodyBytes = resolveWebhookMaxBodyBytes(config.webhookMaxBodyBytes);
    const body = await readWebhookBody(request, maxBodyBytes);
    if (body instanceof Response) return body;

    if (!verifyGitHubWebhookSignature(config.webhookSecret, body, request.headers.get("x-hub-signature-256"))) {
      return json({ error: "invalid webhook signature" }, 401);
    }

    if (event === "ping") {
      return json({ ok: true, delivery });
    }

    if (event !== "workflow_run") {
      return json({ ok: true, status: "ignored", reason: `unsupported event: ${event ?? "unknown"}` }, 202);
    }

    let parsed: unknown;
    try {
      parsed = JSON.parse(body);
    } catch {
      return json({ error: "invalid JSON webhook body" }, 400);
    }
    const validationError = validateWorkflowRunPayload(parsed, config.trustedCloneHosts);
    if (validationError) return json({ error: validationError }, 400);
    assertWorkflowRunPayload(parsed, config.trustedCloneHosts);
    const payload = parsed;

    const deliveryKey = delivery === "unknown" ? undefined : `delivery:${delivery}`;
    const workflowKey = `workflow_run:${workflowRunKey(payload)}`;
    if (!deduper.reserve(deliveryKey)) {
      return json({ ok: true, status: "ignored", reason: "duplicate delivery", delivery }, 202);
    }
    if (!deduper.reserve(workflowKey)) {
      deduper.release(deliveryKey);
      return json({ ok: true, status: "ignored", reason: "duplicate workflow run", delivery }, 202);
    }

    queue.enqueue(
      releaseQueueKey(payload),
      async () => {
        await releaseFromWorkflowRun(payload, config, runner);
        deduper.succeed(deliveryKey);
        deduper.succeed(workflowKey);
      },
      () => {
        deduper.release(deliveryKey);
        deduper.release(workflowKey);
      },
    );

    return json({ ok: true, status: "accepted", delivery }, 202);
  };
}
function resolveWebhookMaxBodyBytes(value: number | undefined): number {
  const resolvedValue = value ?? DEFAULT_WEBHOOK_MAX_BODY_BYTES;
  return isPositiveInteger(resolvedValue) ? resolvedValue : DEFAULT_WEBHOOK_MAX_BODY_BYTES;
}

async function readWebhookBody(request: Request, maxBytes: number): Promise<string | Response> {
  const declaredLength = request.headers.get("content-length");
  if (declaredLength !== null) {
    const length = Number(declaredLength);
    if (Number.isFinite(length) && length > maxBytes) {
      return json({ error: "webhook payload too large" }, 413);
    }
  }

  if (!request.body) return "";
  const reader = request.body.getReader();
  const chunks: Uint8Array[] = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const chunk = value instanceof Uint8Array ? value : new Uint8Array(value);
      if (total + chunk.byteLength > maxBytes) {
        await reader.cancel().catch(() => undefined);
        return json({ error: "webhook payload too large" }, 413);
      }
      chunks.push(chunk);
      total += chunk.byteLength;
    }
  } finally {
    reader.releaseLock();
  }

  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(body);
}
function validateWorkflowRunPayload(value: unknown, trustedCloneHosts: readonly string[] = []): string | undefined {
  if (!value || typeof value !== "object") return "invalid webhook payload: expected an object";
  const payload = value as Partial<WorkflowRunPayload>;
  if (typeof payload.action !== "string") return "invalid webhook payload: missing workflow_run action";
  if (!payload.repository || typeof payload.repository !== "object") {
    return "invalid webhook payload: missing repository";
  }
  const repository = payload.repository as Partial<GitHubRepositoryPayload>;
  const repositoryId = repository.id;
  if (
    !isPositiveInteger(repositoryId) ||
    typeof repository.full_name !== "string" ||
    !/^[^/\s]+\/[^/\s]+$/.test(repository.full_name) ||
    typeof repository.clone_url !== "string" ||
    typeof repository.default_branch !== "string" ||
    repository.default_branch.length === 0
  ) {
    return "invalid webhook payload: malformed repository metadata";
  }
  let cloneUrl: URL;
  try {
    cloneUrl = new URL(repository.clone_url);
  } catch {
    return "invalid webhook payload: malformed clone metadata";
  }
  if (
    cloneUrl.protocol !== "https:" ||
    !["github.com", ...trustedCloneHosts.map((host) => host.toLowerCase())].includes(cloneUrl.hostname.toLowerCase()) ||
    cloneUrl.port ||
    cloneUrl.username ||
    cloneUrl.password ||
    cloneUrl.search ||
    cloneUrl.hash ||
    cloneUrl.pathname !== `/${repository.full_name}.git`
  ) {
    return "invalid webhook payload: malformed clone metadata";
  }
  if (!payload.installation || typeof payload.installation !== "object") {
    return "invalid webhook payload: missing installation";
  }
  const installation = payload.installation as Partial<GitHubInstallationPayload>;
  const installationId = installation.id;
  if (!isPositiveInteger(installationId)) {
    return "invalid webhook payload: malformed installation";
  }
  if (!payload.workflow_run || typeof payload.workflow_run !== "object") {
    return "invalid webhook payload: missing workflow_run";
  }
  const workflowRun = payload.workflow_run as Partial<WorkflowRunPayload["workflow_run"]>;
  if (
    typeof workflowRun.name !== "string" ||
    typeof workflowRun.event !== "string" ||
    (typeof workflowRun.conclusion !== "string" && workflowRun.conclusion !== null) ||
    (typeof workflowRun.head_branch !== "string" && workflowRun.head_branch !== null) ||
    typeof workflowRun.head_sha !== "string" ||
    workflowRun.head_sha.length === 0
  ) {
    return "invalid webhook payload: malformed workflow_run metadata";
  }
  if (
    workflowRun.head_repository !== undefined &&
    workflowRun.head_repository !== null &&
    (typeof workflowRun.head_repository !== "object" ||
      typeof workflowRun.head_repository.full_name !== "string")
  ) {
    return "invalid webhook payload: malformed workflow_run repository metadata";
  }
  return undefined;
}
function assertWorkflowRunPayload(
  value: unknown,
  trustedCloneHosts: readonly string[] = [],
): asserts value is WorkflowRunPayload {
  const validationError = validateWorkflowRunPayload(value, trustedCloneHosts);
  if (validationError) throw new HooversionError(validationError);
}



export function shouldHandleWorkflowRun(
  payload: WorkflowRunPayload,
  config: Pick<VersionhooAppConfig, "allowedRepositories" | "releaseBranches" | "ciWorkflowNames" | "trustedCloneHosts">,
): VersionhooWebhookResult {
  const validationError = validateWorkflowRunPayload(payload, config.trustedCloneHosts);
  if (validationError) return ignored(validationError);
  if (payload.action !== "completed") return ignored(`workflow_run action is ${payload.action}`);
  if (payload.workflow_run.conclusion !== "success") {
    return ignored(`workflow_run conclusion is ${payload.workflow_run.conclusion ?? "missing"}`);
  }
  if (payload.workflow_run.event !== "push") return ignored(`workflow_run event is ${payload.workflow_run.event}`);
  if (!config.ciWorkflowNames.includes(payload.workflow_run.name)) {
    return ignored(`workflow ${payload.workflow_run.name} is not configured for releases`);
  }

  const branch = payload.workflow_run.head_branch;
  if (!branch || !config.releaseBranches.includes(branch)) {
    return ignored(`branch ${branch ?? "missing"} is not a release branch`);
  }
  if (
    payload.workflow_run.head_repository?.full_name &&
    payload.workflow_run.head_repository.full_name !== payload.repository.full_name
  ) {
    return ignored("workflow_run came from a fork");
  }
  if (config.allowedRepositories.length > 0 && !config.allowedRepositories.includes(payload.repository.full_name)) {
    return ignored(`repository ${payload.repository.full_name} is not allowed`);
  }
  if (isReleaseCommit(payload.workflow_run.head_commit?.message ?? "")) return ignored("release commit");
  if (!payload.installation?.id) return ignored("missing installation id");
  if (!payload.workflow_run.head_sha) return ignored("missing workflow head sha");
  return { status: "accepted" };
}

export async function releaseFromWorkflowRun(
  payload: WorkflowRunPayload,
  config: VersionhooAppConfig,
  runner: ReleaseRunner = runVersionhooRelease,
): Promise<void> {
  const decision = shouldHandleWorkflowRun(payload, config);
  if (decision.status === "ignored") return;

  assertWorkflowRunPayload(payload, config.trustedCloneHosts);
  const installation = payload.installation;
  if (!installation) throw new HooversionError("Workflow run payload is missing installation id or branch.");
  const installationId = installation.id;
  const branch = payload.workflow_run.head_branch;
  if (branch === null) throw new HooversionError("Workflow run payload is missing installation id or branch.");
  const headSha = payload.workflow_run.head_sha;
  const repositoryId = payload.repository.id;
  const repositoryFullName = payload.repository.full_name;
  const cloneUrl = payload.repository.clone_url;

  const access = await createInstallationAccessToken(
    { appId: config.appId, privateKey: config.privateKey, apiUrl: config.apiUrl, trustedApiUrls: config.trustedApiUrls },
    installationId,
    { id: repositoryId, fullName: repositoryFullName },
  );

  let checkRun: CheckRun | undefined;
  try {
    checkRun = await createReleaseCheckRun(
      config.apiUrl,
      repositoryFullName,
      access.token,
      headSha,
      repositoryFullName,
      config.trustedApiUrls,
    ).catch((error) => {
      console.warn(`Could not create Versionhoo Release check: ${error instanceof Error ? error.message : error}`);
      return undefined;
    });

    const result = await runner({
      repositoryFullName,
      cloneUrl,
      branch,
      headSha,
      token: access.token,
      apiUrl: config.apiUrl,
      trustedApiUrls: config.trustedApiUrls,
      trustedCloneHosts: config.trustedCloneHosts,
      workDir: config.workDir,
      configPath: config.configPath,
      installCommand: config.installCommand,
      gitAuthorName: config.gitAuthorName,
      gitAuthorEmail: config.gitAuthorEmail,
      keepWorkDir: config.keepWorkDir,
    });

    if (checkRun) {
      const check = releaseCheckResult(result);
      await completeReleaseCheckRun(
        config.apiUrl,
        repositoryFullName,
        access.token,
        checkRun.id,
        check.conclusion,
        check.title,
        check.summary,
        repositoryFullName,
        config.trustedApiUrls,
      ).catch((error) => {
        console.warn(`Could not complete Versionhoo Release check: ${error instanceof Error ? error.message : error}`);
      });
    }
  } catch (error) {
    if (checkRun) {
      const check = releaseFailureCheckResult(error);
      await completeReleaseCheckRun(
        config.apiUrl,
        repositoryFullName,
        access.token,
        checkRun.id,
        check.conclusion,
        check.title,
        check.summary,
        repositoryFullName,
        config.trustedApiUrls,
      ).catch((checkError) => {
        console.warn(
          `Could not mark Versionhoo Release check failed: ${
            checkError instanceof Error ? checkError.message : checkError
          }`,
        );
      });
    }
    throw error;
  }
}

function isReleaseCommit(message: string): boolean {
  return /^chore\(release\):/m.test(message) || /\[skip ci\]/i.test(message);
}

function ignored(reason: string): VersionhooWebhookResult {
  return { status: "ignored", reason };
}

function workflowRunKey(payload: WorkflowRunPayload): string {
  return [
    payload.repository.full_name,
    payload.workflow_run.id ?? payload.workflow_run.head_sha,
    payload.workflow_run.name,
    payload.workflow_run.head_branch ?? "",
  ].join(":");
}

function releaseQueueKey(payload: WorkflowRunPayload): string {
  return `${payload.repository.full_name}:${payload.workflow_run.head_branch ?? ""}`;
}

function readRequiredEnv(env: Record<string, string | undefined>, names: string[]): string {
  const value = readEnv(env, names);
  if (!value) throw new HooversionError(`${names.join(" or ")} is required.`);
  return value;
}

function readEnv(env: Record<string, string | undefined>, names: string[]): string | undefined {
  for (const name of names) {
    const value = env[name];
    if (value) return value;
  }
  return undefined;
}

function splitCsv(value?: string): string[] {
  return value
    ? value
        .split(",")
        .map((item) => item.trim())
        .filter(Boolean)
    : [];
}

function readBoolean(value?: string): boolean {
  return value === "1" || value === "true" || value === "yes";
}

function json(value: unknown, status = 200): Response {
  return new Response(JSON.stringify(value, null, 2), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" },
  });
}
