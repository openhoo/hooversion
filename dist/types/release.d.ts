import { type GitNetworkAuth } from "./git";
import type { NormalizedConfig, ReleasePlan } from "./types";
export interface ReleaseOptions {
    dryRun?: boolean;
    push?: boolean;
    github?: boolean;
    githubToken?: string;
    gitAuth?: GitNetworkAuth;
}
export interface ReleaseExecutionResult {
    plan: ReleasePlan;
    published: boolean;
}
export declare function executeRelease(cwd: string, config: NormalizedConfig, plan: ReleasePlan, options?: ReleaseOptions): Promise<ReleaseExecutionResult>;
export declare function validatePlan(cwd: string, config: NormalizedConfig, plan: ReleasePlan, resumable?: boolean): void;
