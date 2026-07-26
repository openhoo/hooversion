import { describe, expect, it } from "bun:test";
import { createHmac } from "node:crypto";
import {
  createVersionhooWebhookHandler,
  ReleaseTaskQueue,
  shouldHandleWorkflowRun,
  WebhookDeduper,
  type VersionhooAppConfig,
} from "../src/app-server";
import type { VersionhooReleaseResult } from "../src/app-runner";

interface TestWorkflowRun {
  name: string;
  event: string;
  conclusion: string | null;
  head_branch: string | null;
  head_sha: string;
  head_commit: { message?: string };
  head_repository: { full_name?: string };
}

function hasTruthyReason(value: unknown): value is { reason: unknown } {
  if (typeof value !== "object" || value === null) return false;
  const record = value as Record<string, unknown>;
  return Boolean(record.reason);
}

describe("Versionhoo GitHub App webhook handling", () => {
  it("accepts successful CI workflow runs on release branches", () => {
    expect(shouldHandleWorkflowRun(workflowRun(), config()).status).toBe("accepted");
  });

  it("ignores workflow runs that should not trigger releases", () => {
    expect(shouldHandleWorkflowRun(workflowRun({ conclusion: "failure" }), config()).reason).toContain("failure");
    expect(shouldHandleWorkflowRun(workflowRun({ name: "Release" }), config()).reason).toContain("not configured");
    expect(shouldHandleWorkflowRun(workflowRun({ head_branch: "feature/demo" }), config()).reason).toContain(
      "not a release branch",
    );
    expect(
      shouldHandleWorkflowRun(workflowRun({ head_commit: { message: "chore(release): app 1.0.1" } }), config())
        .reason,
    ).toBe("release commit");
    expect(shouldHandleWorkflowRun(workflowRun(), { ...config(), allowedRepositories: ["other/repo"] }).reason).toContain(
      "not allowed",
    );
  });

  it("rejects unsigned webhook requests", async () => {
    const handler = createVersionhooWebhookHandler(config(), fakeRunner);
    const response = await handler(
      new Request("https://versionhoo.test/webhooks/github", {
        method: "POST",
        headers: {
          "x-github-event": "ping",
          "x-hub-signature-256": "sha256=bad",
        },
        body: "{}",
      }),
    );

    expect(response.status).toBe(401);
  });

  it("queues accepted workflow_run webhooks after signature verification", async () => {
    const body = JSON.stringify(workflowRun());
    let queued = false;
    let queuedKey = "";
    const handler = createVersionhooWebhookHandler(config(), fakeRunner, {
      enqueue(key: string) {
        queued = true;
        queuedKey = key;
      },
    } as never);
    const response = await handler(
      new Request("https://versionhoo.test/webhooks/github", {
        method: "POST",
        headers: {
          "x-github-event": "workflow_run",
          "x-github-delivery": "delivery-1",
          "x-hub-signature-256": sign(body),
        },
        body,
      }),
    );

    expect(response.status).toBe(202);
    expect(await response.json()).toMatchObject({ status: "accepted", delivery: "delivery-1" });
    expect(queued).toBe(true);
    expect(queuedKey).toBe("openhoo/app:main");
  });

  it("deduplicates redelivered workflow_run webhooks", async () => {
    const body = JSON.stringify(workflowRun());
    let queued = 0;
    const handler = createVersionhooWebhookHandler(
      config(),
      fakeRunner,
      {
        enqueue() {
          queued += 1;
        },
      } as never,
      new WebhookDeduper(),
    );

    const first = await handler(signedWorkflowRequest(body, "delivery-1"));
    const second = await handler(signedWorkflowRequest(body, "delivery-1"));
    const third = await handler(signedWorkflowRequest(body, "delivery-2"));

    expect(first.status).toBe(202);
    expect(await second.json()).toMatchObject({ status: "ignored", reason: "duplicate delivery" });
    expect(await third.json()).toMatchObject({ status: "ignored", reason: "duplicate workflow run" });
    expect(queued).toBe(1);
  });

  it("rejects oversized webhook bodies before parsing", async () => {
    const handler = createVersionhooWebhookHandler({ ...config(), webhookMaxBodyBytes: 4 }, fakeRunner);
    const body = "12345";
    const response = await handler(
      new Request("https://versionhoo.test/webhooks/github", {
        method: "POST",
        headers: {
          "x-github-event": "ping",
          "x-hub-signature-256": sign(body),
        },
        body,
      }),
    );

    expect(response.status).toBe(413);
    expect(await response.json()).toEqual({ error: "webhook payload too large" });
  });

  it("rejects malformed repository and clone metadata before enqueueing", async () => {
    let queued = 0;
    const queue = {
      enqueue() {
        queued += 1;
      },
    };
    const malformedRepositoryPayload = workflowRun();
    malformedRepositoryPayload.repository.id = 0;
    const malformedClonePayload = workflowRun();
    malformedClonePayload.repository.clone_url = "https://evil.test/openhoo/app.git";
    const handler = createVersionhooWebhookHandler(config(), fakeRunner, queue as never);

    const repositoryResponse = await handler(
      signedWorkflowRequest(JSON.stringify(malformedRepositoryPayload), "bad-repository"),
    );
    const cloneResponse = await handler(signedWorkflowRequest(JSON.stringify(malformedClonePayload), "bad-clone"));

    expect(repositoryResponse.status).toBe(400);
    expect(cloneResponse.status).toBe(400);
    expect(queued).toBe(0);
  });

  it("coalesces concurrent duplicate deliveries while work is in flight", async () => {
    const queue = new ControlledQueue();
    const handler = createVersionhooWebhookHandler(config(), fakeRunner, queue as never, new WebhookDeduper());
    const body = JSON.stringify(workflowRun());

    const [first, second] = await Promise.all([
      handler(signedWorkflowRequest(body, "concurrent")),
      handler(signedWorkflowRequest(body, "concurrent")),
    ]);

    expect([first.status, second.status].sort()).toEqual([202, 202]);
    expect(queue.tasks).toHaveLength(1);
    expect((await Promise.all([first.json(), second.json()])).filter(hasTruthyReason)).toHaveLength(1);
  });
  it("suppresses successful duplicate deliveries", async () => {
    const deduper = new WebhookDeduper();
    expect(deduper.reserve("delivery:successful")).toBe(true);
    expect(deduper.reserve("workflow_run:openhoo/app:100:CI:main")).toBe(true);
    deduper.succeed("delivery:successful");
    deduper.succeed("workflow_run:openhoo/app:100:CI:main");

    expect(deduper.reserve("delivery:successful")).toBe(false);
    expect(deduper.reserve("workflow_run:openhoo/app:100:CI:main")).toBe(false);
  });
  it("allows webhook redelivery after a queued task failure", async () => {
    const queue = new ControlledQueue();
    const handler = createVersionhooWebhookHandler(config(), fakeRunner, queue as never, new WebhookDeduper());
    const body = JSON.stringify(workflowRun());

    expect((await handler(signedWorkflowRequest(body, "failed-delivery"))).status).toBe(202);
    await expect(queue.runNext()).rejects.toBeTruthy();
    const redelivery = await handler(signedWorkflowRequest(body, "failed-delivery"));

    expect(redelivery.status).toBe(202);
    expect(queue.tasks).toHaveLength(1);
  });


  it("releases delivery and workflow suppression after task failure", async () => {
    const failures: unknown[] = [];
    const queue = new ReleaseTaskQueue((error) => failures.push(error));
    const deduper = new WebhookDeduper();
    const deliveryKey = "delivery:retryable";
    const workflowKey = "workflow_run:openhoo/app:100:CI:main";
    expect(deduper.reserve(deliveryKey)).toBe(true);
    expect(deduper.reserve(workflowKey)).toBe(true);

    await queue.enqueue("openhoo/app:main", async () => {
      deduper.release(deliveryKey);
      deduper.release(workflowKey);
      throw new Error("transient failure");
    });
    expect(failures[0]).toBeInstanceOf(Error);
    expect(deduper.reserve(deliveryKey)).toBe(true);
    expect(deduper.reserve(workflowKey)).toBe(true);

    let attempts = 0;
    await queue.enqueue("openhoo/app:main", async () => {
      attempts += 1;
      deduper.succeed(deliveryKey);
      deduper.succeed(workflowKey);
    });
    expect(attempts).toBe(1);
    expect(deduper.reserve(deliveryKey)).toBe(false);
    expect(deduper.reserve(workflowKey)).toBe(false);
  });
  it("retries transient queue failures within bounded attempts", async () => {
    const queue = new ReleaseTaskQueue(() => undefined, { maxAttempts: 2, retryDelayMs: 0 });
    let attempts = 0;

    await queue.enqueue("openhoo/app:main", async () => {
      attempts += 1;
      if (attempts === 1) throw new Error("transient failure");
    });

    expect(attempts).toBe(2);
    expect(queue.failure).toBeUndefined();

    let failedAttempts = 0;
    await queue.enqueue("openhoo/app:main", async () => {
      failedAttempts += 1;
      throw new Error("permanent failure");
    });
    expect(failedAttempts).toBe(2);
    expect(queue.failure).toBeInstanceOf(Error);
  });
  it("serializes release tasks per repository branch", async () => {
    const queue = new ReleaseTaskQueue();
    const events: string[] = [];

    queue.enqueue("openhoo/app:main", async () => {
      events.push("first-start");
      await Promise.resolve();
      events.push("first-end");
    });
    queue.enqueue("openhoo/app:main", async () => {
      events.push("second-start");
    });

    await new Promise((resolve) => setTimeout(resolve, 0));
    expect(events).toEqual(["first-start", "first-end", "second-start"]);
  });
});

function config(): VersionhooAppConfig {
  return {
    appId: "123",
    privateKey: "unused",
    webhookSecret: "secret",
    apiUrl: "https://api.github.com",
    trustedApiUrls: [],
    trustedCloneHosts: [],
    host: "127.0.0.1",
    port: 3000,
    allowedRepositories: ["openhoo/app"],
    releaseBranches: ["main"],
    ciWorkflowNames: ["CI"],
  };
}

function workflowRun(overrides: Partial<TestWorkflowRun> = {}) {
  return {
    action: "completed",
    repository: {
      id: 99,
      full_name: "openhoo/app",
      clone_url: "https://github.com/openhoo/app.git",
      default_branch: "main",
    },
    installation: { id: 42 },
    workflow_run: {
      name: "CI",
      id: 100,
      event: "push",
      conclusion: "success",
      head_branch: "main",
      head_sha: "abc123",
      head_commit: { message: "feat: add app" },
      head_repository: { full_name: "openhoo/app" },
      ...overrides,
    },
  };
}

class ControlledQueue {
  readonly tasks: Array<{ task: () => Promise<void>; onFinalFailure?: (error: unknown) => void }> = [];

  enqueue(
    _key: string,
    task: () => Promise<void>,
    onFinalFailure?: (error: unknown) => void,
  ): void {
    this.tasks.push({ task, onFinalFailure });
  }

  async runNext(): Promise<void> {
    const queued = this.tasks.shift();
    if (!queued) throw new Error("no queued task");
    try {
      await queued.task();
    } catch (error) {
      queued.onFinalFailure?.(error);
      throw error;
    }
  }
}

function signedWorkflowRequest(body: string, delivery: string): Request {
  return new Request("https://versionhoo.test/webhooks/github", {
    method: "POST",
    headers: {
      "x-github-event": "workflow_run",
      "x-github-delivery": delivery,
      "x-hub-signature-256": sign(body),
    },
    body,
  });
}

function sign(body: string): string {
  return `sha256=${createHmac("sha256", "secret").update(body).digest("hex")}`;
}

async function fakeRunner(): Promise<VersionhooReleaseResult> {
  return {
    repositoryFullName: "openhoo/app",
    branch: "main",
    headSha: "abc123",
    workDir: "/tmp/versionhoo",
    outcome: "no_release",
    published: false,
    releases: [],
  };
}
