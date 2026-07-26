import type { ReleaseType } from "./types";
export interface SemverParts {
    major: number;
    minor: number;
    patch: number;
}
export declare function parseVersion(version: string): SemverParts;
export declare function bumpVersion(version: string, releaseType: ReleaseType): string;
export declare function highestReleaseType(types: Iterable<ReleaseType | undefined>): ReleaseType | undefined;
export declare function minReleaseType(current: ReleaseType | undefined, minimum: ReleaseType): ReleaseType;
