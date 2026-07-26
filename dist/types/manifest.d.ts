import type { NormalizedPackageConfig, PackageType } from "./types";
export interface ManifestInfo {
    name: string;
    version: string;
}
export declare function defaultManifestPath(type: PackageType, packagePath: string): string;
export declare function readManifest(cwd: string, pkg: NormalizedPackageConfig): ManifestInfo;
export declare function updateManifestVersion(cwd: string, pkg: NormalizedPackageConfig, version: string): void;
export declare function updateLocalDependencyVersions(cwd: string, packages: NormalizedPackageConfig[], releasedVersions: Map<string, string>): void;
export declare function changelogPathForPackage(pkg: NormalizedPackageConfig): string;
