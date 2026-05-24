import { relative } from "node:path";
import type { NormalizedPackageConfig, ParsedCommit } from "./types";

export function directAffectedPackages(
  commit: ParsedCommit,
  packages: NormalizedPackageConfig[],
): Set<string> {
  const affected = new Set<string>();
  const scoped = scopeTargets(commit.scope, packages);
  if (scoped.size > 0) {
    for (const name of scoped) affected.add(name);
  }

  for (const file of commit.files) {
    for (const pkg of packages) {
      if (fileBelongsToPackage(file, pkg, packages)) affected.add(pkg.name);
    }
  }

  return affected;
}

export function propagateDependencies(
  affected: Set<string>,
  packages: NormalizedPackageConfig[],
): Set<string> {
  const result = new Set(affected);
  let changed = true;
  while (changed) {
    changed = false;
    for (const pkg of packages) {
      if (result.has(pkg.name)) continue;
      if (pkg.dependencies.some((dependency) => result.has(dependency))) {
        result.add(pkg.name);
        changed = true;
      }
    }
  }
  return result;
}

function scopeTargets(scope: string | undefined, packages: NormalizedPackageConfig[]): Set<string> {
  const result = new Set<string>();
  if (!scope) return result;
  const parts = scope.split(",").map((part) => part.trim()).filter(Boolean);
  for (const part of parts) {
    for (const pkg of packages) {
      if (pkg.scopes.includes(part) || pkg.name === part) {
        result.add(pkg.name);
      }
    }
  }
  return result;
}

function fileBelongsToPackage(
  file: string,
  pkg: NormalizedPackageConfig,
  packages: NormalizedPackageConfig[],
): boolean {
  if (pkg.path === ".") {
    return !packages.some((other) => other.path !== "." && isInside(file, other.path));
  }
  return isInside(file, pkg.path);
}

function isInside(file: string, packagePath: string): boolean {
  const rel = relative(packagePath, file);
  return rel === "" || (!rel.startsWith("..") && rel !== ".");
}
