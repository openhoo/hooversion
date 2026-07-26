import type { VersionhooReleaseResult } from "./app-runner";
export type CheckConclusion = "success" | "failure" | "neutral";
export interface CheckRun {
    id: number;
    htmlUrl?: string;
}
export declare function createReleaseCheckRun(apiUrl: string, repository: string, token: string, headSha: string, expectedRepository: string, trustedApiUrls?: readonly string[]): Promise<CheckRun>;
export declare function completeReleaseCheckRun(apiUrl: string, repository: string, token: string, checkRunId: number, conclusion: CheckConclusion, title: string, summary: string, expectedRepository: string, trustedApiUrls?: readonly string[]): Promise<void>;
export declare function releaseCheckResult(result: VersionhooReleaseResult): {
    conclusion: CheckConclusion;
    title: string;
    summary: string;
};
export declare function releaseFailureCheckResult(error: unknown): {
    conclusion: CheckConclusion;
    title: string;
    summary: string;
};
