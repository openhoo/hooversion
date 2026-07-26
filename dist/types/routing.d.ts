import type { NormalizedPackageConfig, ParsedCommit } from "./types";
export declare function directAffectedPackages(commit: ParsedCommit, packages: NormalizedPackageConfig[]): Set<string>;
export declare function propagateDependencies(affected: Set<string>, packages: NormalizedPackageConfig[]): Set<string>;
