import { HooversionError } from "./errors";
import type { ReleaseType } from "./types";

export interface SemverParts {
  major: number;
  minor: number;
  patch: number;
}

const versionPattern = /^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/;

export function parseVersion(version: string): SemverParts {
  const match = versionPattern.exec(version.trim());
  if (!match) {
    throw new HooversionError(`Invalid semantic version: ${version}`);
  }

  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3]),
  };
}

export function bumpVersion(version: string, releaseType: ReleaseType): string {
  const parts = parseVersion(version);
  if (releaseType === "major") {
    return `${parts.major + 1}.0.0`;
  }
  if (releaseType === "minor") {
    return `${parts.major}.${parts.minor + 1}.0`;
  }
  return `${parts.major}.${parts.minor}.${parts.patch + 1}`;
}

export function highestReleaseType(types: Iterable<ReleaseType | undefined>): ReleaseType | undefined {
  let result: ReleaseType | undefined;
  for (const type of types) {
    if (!type) continue;
    if (type === "major") return "major";
    if (type === "minor") result = result === "major" ? result : "minor";
    if (type === "patch" && !result) result = "patch";
  }
  return result;
}

export function minReleaseType(current: ReleaseType | undefined, minimum: ReleaseType): ReleaseType {
  return highestReleaseType([current, minimum]) ?? minimum;
}
