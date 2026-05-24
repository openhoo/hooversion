import { spawnSync } from "node:child_process";
import type { CommandResult } from "./types";

export function runCommand(command: string, args: string[], cwd: string): CommandResult {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    env: process.env,
  });

  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}

export function runShell(command: string, cwd: string): CommandResult {
  const result = spawnSync(command, {
    cwd,
    encoding: "utf8",
    env: process.env,
    shell: process.env.SHELL ?? "/bin/sh",
  });

  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? "",
  };
}
