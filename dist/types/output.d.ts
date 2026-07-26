import type { NormalizedConfig, ReleasePlan } from "./types";
export declare function getReleaseOutputPaths(cwd: string, outputDir?: string): string[];
export declare function clearReleaseOutputs(cwd: string, outputDir?: string): void;
export declare function writeReleaseOutputs(cwd: string, config: NormalizedConfig, plan: ReleasePlan): void;
