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
export declare function readGitHubAppPrivateKey(env?: Record<string, string | undefined>): string;
export declare function createGitHubAppJwt(config: GitHubAppAuthConfig, nowSeconds?: number): string;
export declare function createInstallationAccessToken(config: GitHubAppAuthConfig, installationId: number, repository: {
    id: number;
    fullName: string;
}): Promise<InstallationToken>;
export declare function verifyGitHubWebhookSignature(secret: string, body: string, signatureHeader: string | null): boolean;
export declare function validateGitHubApiUrl(apiUrl: string, trustedApiUrls?: readonly string[]): string;
export declare function validateRepositoryFullName(repository: string): string;
