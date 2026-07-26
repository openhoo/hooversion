import type { CommandResult } from "./types";
export declare function runCommand(command: string, args: string[], cwd: string, env?: NodeJS.ProcessEnv): CommandResult;
export declare function runShell(command: string, cwd: string, env?: NodeJS.ProcessEnv): CommandResult;
