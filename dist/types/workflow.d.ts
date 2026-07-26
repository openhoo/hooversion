export interface GitHubWorkflowOptions {
    actionOwnerRepo?: string;
    actionRef?: string;
    hooversionVersion?: string;
    bunVersion?: string;
    releaseBranch?: string;
    force?: boolean;
}
export declare function writeGitHubWorkflow(cwd: string, options?: GitHubWorkflowOptions): string;
export declare function writeGitHubWorkflows(cwd: string, options?: GitHubWorkflowOptions): string[];
export declare function renderGitHubWorkflow(options?: GitHubWorkflowOptions): string;
export declare function renderGitHubWorkflows(options?: GitHubWorkflowOptions): {
    ci: string;
    release: string;
};
