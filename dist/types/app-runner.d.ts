export interface VersionhooReleaseJob {
    repositoryFullName: string;
    cloneUrl: string;
    branch: string;
    headSha: string;
    token: string;
    apiUrl?: string;
    trustedApiUrls?: readonly string[];
    trustedCloneHosts?: readonly string[];
    workDir?: string;
    configPath?: string;
    installCommand?: string;
    gitAuthorName?: string;
    gitAuthorEmail?: string;
    keepWorkDir?: boolean;
}
export interface VersionhooReleaseResult {
    repositoryFullName: string;
    branch: string;
    headSha: string;
    workDir: string;
    outcome: "published" | "no_release" | "stale";
    published: boolean;
    message?: string;
    releases: Array<{
        name: string;
        version: string;
        tag: string;
    }>;
}
export declare function runVersionhooRelease(job: VersionhooReleaseJob): Promise<VersionhooReleaseResult>;
export declare function validateCloneUrl(cloneUrl: string, repositoryFullName: string, trustedCloneHosts?: readonly string[]): string;
