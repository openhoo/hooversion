import type { PackageRelease } from "./types";
export declare function generateReleaseNotes(release: Omit<PackageRelease, "notes">): string;
export declare function updateChangelog(cwd: string, release: PackageRelease): void;
