import type { NormalizedConfig, NormalizedPackageConfig, ReleasePlan } from "./types";
export declare function renderTag(format: string, pkg: NormalizedPackageConfig, version: string): string;
export declare function tagPatternForPackage(config: NormalizedConfig, pkg: NormalizedPackageConfig): string;
export declare function tagForPackage(config: NormalizedConfig, pkg: NormalizedPackageConfig, version: string): string;
export declare function createReleasePlan(cwd: string, config: NormalizedConfig): ReleasePlan;
