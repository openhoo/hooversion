import type { NormalizedConfig, PackageRelease } from "./types";
export declare function publishGitHubRelease(cwd: string, config: NormalizedConfig, release: PackageRelease, options?: {
    token?: string;
}): Promise<string | undefined>;
