import type { HooversionConfig, NormalizedConfig, NormalizedPackageConfig } from "./types";
export declare function loadConfig(cwd: string, explicitPath?: string): Promise<NormalizedConfig>;
export declare function findConfigPath(cwd: string): string | undefined;
export declare function normalizeConfig(cwd: string, raw: HooversionConfig): NormalizedConfig;
export declare function detectPackages(cwd: string): NormalizedPackageConfig[];
export declare function writeDefaultConfig(cwd: string, packages?: NormalizedPackageConfig[]): string;
export declare function relativeToCwd(cwd: string, path: string): string;
