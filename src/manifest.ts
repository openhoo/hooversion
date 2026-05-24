import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { HooversionError } from "./errors";
import type { NormalizedPackageConfig, PackageType } from "./types";

export interface ManifestInfo {
  name: string;
  version: string;
}

export function defaultManifestPath(type: PackageType, packagePath: string): string {
  if (type === "node") return join(packagePath, "package.json");
  if (type === "rust") return join(packagePath, "Cargo.toml");
  return join(packagePath, "pyproject.toml");
}

export function readManifest(cwd: string, pkg: NormalizedPackageConfig): ManifestInfo {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node") return readPackageJson(path);
  if (pkg.type === "rust") return readTomlPackage(path, "package");
  return readTomlPackage(path, "project");
}

export function updateManifestVersion(cwd: string, pkg: NormalizedPackageConfig, version: string): void {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node") {
    const json = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
    json.version = version;
    writeFileSync(path, `${JSON.stringify(json, null, 2)}\n`);
    return;
  }

  const section = pkg.type === "rust" ? "package" : "project";
  updateTomlSectionVersion(path, section, version);
}

export function updateLocalDependencyVersions(
  cwd: string,
  packages: NormalizedPackageConfig[],
  releasedVersions: Map<string, string>,
): void {
  for (const pkg of packages) {
    const path = join(cwd, pkg.manifest);
    if (pkg.type === "node") {
      updateNodeLocalDependencies(path, releasedVersions);
    } else if (pkg.type === "rust") {
      updateRustLocalDependencies(path, releasedVersions);
    }
  }
}

function readPackageJson(path: string): ManifestInfo {
  const json = JSON.parse(readFileSync(path, "utf8")) as { name?: string; version?: string };
  if (!json.name || !json.version) {
    throw new HooversionError(`${path} must contain name and version`);
  }
  return { name: json.name, version: json.version };
}

function readTomlPackage(path: string, sectionName: string): ManifestInfo {
  const text = readFileSync(path, "utf8");
  const section = getTomlSection(text, sectionName);
  const name = readTomlString(section, "name");
  const version = readTomlString(section, "version");
  if (!name || !version) {
    throw new HooversionError(`${path} [${sectionName}] must contain name and version`);
  }
  return { name, version };
}

function getTomlSection(text: string, sectionName: string): string {
  const lines = text.split(/\r?\n/);
  let inSection = false;
  const sectionLines: string[] = [];

  for (const line of lines) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      if (inSection) break;
      inSection = heading[1] === sectionName;
      continue;
    }
    if (inSection) sectionLines.push(line);
  }

  return sectionLines.join("\n");
}

function readTomlString(section: string, key: string): string | undefined {
  const pattern = new RegExp(`^\\s*${escapeRegExp(key)}\\s*=\\s*["']([^"']+)["']\\s*$`, "m");
  return pattern.exec(section)?.[1];
}

function updateTomlSectionVersion(path: string, sectionName: string, version: string): void {
  const text = readFileSync(path, "utf8");
  const lines = text.split(/\r?\n/);
  let inSection = false;
  let updated = false;

  const output = lines.map((line) => {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      inSection = heading[1] === sectionName;
      return line;
    }
    if (inSection && /^\s*version\s*=/.test(line)) {
      updated = true;
      return line.replace(/=\s*["'][^"']+["']/, `= "${version}"`);
    }
    return line;
  });

  if (!updated) {
    throw new HooversionError(`${path} [${sectionName}] does not contain a version field`);
  }

  writeFileSync(path, output.join("\n"));
}

function updateNodeLocalDependencies(path: string, releasedVersions: Map<string, string>): void {
  const json = JSON.parse(readFileSync(path, "utf8")) as Record<string, any>;
  let changed = false;
  for (const section of ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"]) {
    const deps = json[section];
    if (!deps || typeof deps !== "object") continue;
    for (const [name, version] of releasedVersions) {
      if (typeof deps[name] === "string") {
        const current = deps[name] as string;
        const prefix = current.match(/^[~^]/)?.[0] ?? "";
        deps[name] = `${prefix}${version}`;
        changed = true;
      }
    }
  }
  if (changed) {
    writeFileSync(path, `${JSON.stringify(json, null, 2)}\n`);
  }
}

function updateRustLocalDependencies(path: string, releasedVersions: Map<string, string>): void {
  let text = readFileSync(path, "utf8");
  let changed = false;
  for (const [name, version] of releasedVersions) {
    const escaped = escapeRegExp(name);
    const inlinePattern = new RegExp(`(^\\s*${escaped}\\s*=\\s*\\{[^\\n}]*version\\s*=\\s*)["'][^"']+["']`, "m");
    if (inlinePattern.test(text)) {
      text = text.replace(inlinePattern, `$1"${version}"`);
      changed = true;
    }

    const simplePattern = new RegExp(`(^\\s*${escaped}\\s*=\\s*)["'][^"']+["']`, "m");
    if (simplePattern.test(text) && !new RegExp(`^\\s*${escaped}\\s*=\\s*\\{`, "m").test(text)) {
      text = text.replace(simplePattern, `$1"${version}"`);
      changed = true;
    }
  }
  if (changed) {
    writeFileSync(path, text);
  }
}

export function changelogPathForPackage(pkg: NormalizedPackageConfig): string {
  return pkg.changelog || join(dirname(pkg.manifest), "CHANGELOG.md");
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
