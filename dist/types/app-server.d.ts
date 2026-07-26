import { type VersionhooReleaseJob, type VersionhooReleaseResult } from "./app-runner";
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
export declare const DEFAULT_WEBHOOK_MAX_BODY_BYTES: number;
type ReleaseRunner = (job: VersionhooReleaseJob) => Promise<VersionhooReleaseResult>;
interface ReleaseTaskQueueOptions {
    maxAttempts?: number;
    retryDelayMs?: number;
}
export declare class ReleaseTaskQueue {
    private tails;
    private lastFailure;
    private readonly onFailure;
    private readonly maxAttempts;
    private readonly retryDelayMs;
    constructor(onFailure?: (error: unknown) => void, options?: ReleaseTaskQueueOptions);
    enqueue(key: string, task: () => Promise<void>, onFinalFailure?: (error: unknown) => void): Promise<void>;
    get failure(): unknown;
    private runWithRetry;
}
export declare class WebhookDeduper {
    private readonly ttlMs;
    private readonly now;
    private seen;
    constructor(ttlMs?: number, now?: () => number);
    reserve(key: string | undefined): boolean;
    succeed(key: string | undefined): void;
    release(key: string | undefined): void;
    remember(key: string | undefined): boolean;
    private prune;
}
export declare function loadVersionhooAppConfigFromEnv(env?: Record<string, string | undefined>): VersionhooAppConfig;
export declare function startVersionhooApp(config: VersionhooAppConfig): Bun.Server<undefined>;
export declare function createVersionhooWebhookHandler(config: VersionhooAppConfig, runner: ReleaseRunner, queue?: ReleaseTaskQueue, deduper?: WebhookDeduper): (request: Request) => Promise<Response>;
export declare function shouldHandleWorkflowRun(payload: WorkflowRunPayload, config: Pick<VersionhooAppConfig, "allowedRepositories" | "releaseBranches" | "ciWorkflowNames" | "trustedCloneHosts">): VersionhooWebhookResult;
export declare function releaseFromWorkflowRun(payload: WorkflowRunPayload, config: VersionhooAppConfig, runner?: ReleaseRunner): Promise<void>;
export {};
