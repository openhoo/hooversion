import { existsSync, readFileSync, writeFileSync } from "node:fs";
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
  if (type === "version-file") return join(packagePath, "version");
  return join(packagePath, "pyproject.toml");
}

export function readManifest(cwd: string, pkg: NormalizedPackageConfig): ManifestInfo {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node") return readPackageJson(path);
  if (pkg.type === "rust") return readTomlPackage(path, "package");
  if (pkg.type === "version-file") return readVersionFile(path, pkg.name);
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
  if (pkg.type === "version-file") {
    writeFileSync(path, `${version}\n`);
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
  const packageNames = new Map(packages.map((pkg) => [normalizePackageName(pkg.name), pkg.name]));
  const rustReleasedVersions = new Map(
    Array.from(releasedVersions).filter(([name]) => packages.some((pkg) => pkg.type === "rust" && normalizePackageName(pkg.name) === normalizePackageName(name))),
  );

  for (const pkg of packages) {
    const localVersions = new Map<string, string>();
    for (const dependency of pkg.dependencies) {
      const actualName = packageNames.get(normalizePackageName(dependency));
      const version = actualName ? releasedVersions.get(actualName) ?? releasedVersions.get(dependency) : undefined;
      if (actualName && version) localVersions.set(actualName, version);
    }
    if (localVersions.size === 0) continue;

    const path = join(cwd, pkg.manifest);
    if (pkg.type === "node") {
      updateNodeLocalDependencies(path, pkg, localVersions);
    } else if (pkg.type === "python") {
      updatePythonLocalDependencies(path, pkg, localVersions);
    } else if (pkg.type === "rust") {
      updateRustLocalDependencies(path, pkg, localVersions);
    }
  }

  const workspaceManifest = join(cwd, "Cargo.toml");
  if (rustReleasedVersions.size > 0 && existsSync(workspaceManifest)) {
    updateRustWorkspaceDependencies(workspaceManifest, rustReleasedVersions);
  }
  if (rustReleasedVersions.size > 0) updateCargoLock(cwd, rustReleasedVersions);
}
function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function normalizePackageName(name: string): string {
  return name.trim().toLowerCase();
}

function findReleasedName(name: string, releasedVersions: Map<string, string>): string | undefined {
  const normalized = normalizePackageName(name);
  return Array.from(releasedVersions.keys()).find((candidate) => normalizePackageName(candidate) === normalized);
}

function assertAllDependenciesFound(
  path: string,
  owner: NormalizedPackageConfig,
  releasedVersions: Map<string, string>,
  found: Set<string>,
): void {
  for (const target of releasedVersions.keys()) {
    if (!found.has(target)) {
      throw new HooversionError(`${path} package ${owner.name} declares local dependency ${target}, but it was not found`);
    }
  }
}

function rewriteNodeRequirement(current: string, version: string, path: string, name: string): string {
  if (current.startsWith("workspace:")) return current;
  if (/^(?:file|git|https?):/.test(current)) {
    throw new HooversionError(`${path} dependency ${name} has unsupported specifier ${current}`);
  }
  const prefix = current.match(/^[~^]/)?.[0] ?? "";
  return `${prefix}${version}`;
}

function isPythonDependencySection(section: string): boolean {
  return (
    section === "project" ||
    section === "project.optional-dependencies" ||
    section.startsWith("project.optional-dependencies.") ||
    section === "tool.poetry.dependencies" ||
    (section.startsWith("tool.poetry.group.") && section.endsWith(".dependencies"))
  );
}

function rewritePythonRequirementsLine(
  line: string,
  releasedVersions: Map<string, string>,
  found: Set<string>,
  path: string,
): { line: string; changed: boolean } {
  let changed = false;
  const output = line.replace(/(["'])([^"']*)\1/g, (full, quote: string, requirement: string) => {
    const nameMatch = /^\s*([A-Za-z0-9][A-Za-z0-9._-]*)/.exec(requirement);
    if (!nameMatch) return full;
    const target = findReleasedName(nameMatch[1], releasedVersions);
    if (!target) return full;
    found.add(target);
    const next = rewritePythonRequirement(requirement, releasedVersions.get(target)!, path, nameMatch[1]);
    if (next === requirement) return full;
    changed = true;
    return `${quote}${next}${quote}`;
  });
  return { line: output, changed };
}

function rewritePythonRequirement(requirement: string, version: string, path: string, name: string): string {
  const suffix = requirement.slice(name.length);
  if (suffix.trimStart().startsWith("@")) {
    throw new HooversionError(`${path} dependency ${name} has unsupported direct URL syntax`);
  }
  return `${name}${rewritePythonConstraint(suffix, version, path, name)}`;
}

function rewritePythonConstraint(current: string, version: string, path: string, name: string): string {
  if (current.includes("@")) {
    throw new HooversionError(`${path} dependency ${name} has unsupported direct URL syntax`);
  }
  const match = /([<>=!~]{1,3})\s*([0-9][^,\s;]*)/.exec(current);
  if (match) return current.replace(match[2], version);
  const marker = current.search(/\s*;/);
  if (marker >= 0) return `${current.slice(0, marker)}==${version}${current.slice(marker)}`;
  return `${current}==${version}`;
}

function isRustDependencySection(section: string, workspaceOnly: boolean): boolean {
  if (workspaceOnly) return section === "workspace.dependencies";
  return (
    ["dependencies", "dev-dependencies", "build-dependencies"].includes(section) ||
    /^target\..+\.(dependencies|dev-dependencies|build-dependencies)$/.test(section)
  );
}

function findRustDottedDependency(
  section: string,
  releasedVersions: Map<string, string>,
  workspaceOnly: boolean,
): string | undefined {
  const match = (
    workspaceOnly
      ? /^workspace\.dependencies\.((?:"[^"]+"|[A-Za-z0-9_-]+))$/
      : /^(?:(?:dependencies|dev-dependencies|build-dependencies)|(?:target\..+\.(?:dependencies|dev-dependencies|build-dependencies)))\.((?:"[^"]+"|[A-Za-z0-9_-]+))$/
  ).exec(section);
  if (!match) return undefined;
  const name = match[1].replace(/^"|"$/g, "");
  return findReleasedName(name, releasedVersions);
}

function finishRustDottedDependency(
  path: string,
  dependency: { target: string; workspace: boolean; versionUpdated: boolean },
  found: Set<string>,
): void {
  if (!dependency.workspace && !dependency.versionUpdated) {
    throw new HooversionError(`${path} dependency ${dependency.target} has no supported version field`);
  }
  found.add(dependency.target);
}

function braceDelta(value: string): number {
  return (value.match(/\{/g)?.length ?? 0) - (value.match(/\}/g)?.length ?? 0);
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

function readVersionFile(path: string, name: string): ManifestInfo {
  const version = readFileSync(path, "utf8").trim();
  if (!version) {
    throw new HooversionError(`${path} must contain a version`);
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

function updateNodeLocalDependencies(
  path: string,
  owner: NormalizedPackageConfig,
  releasedVersions: Map<string, string>,
): void {
  const json = JSON.parse(readFileSync(path, "utf8")) as Record<string, unknown>;
  const sections = ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"];
  const found = new Set<string>();
  let changed = false;

  for (const section of sections) {
    const value = json[section];
    if (value === undefined) continue;
    if (!isRecord(value)) throw new HooversionError(`${path} ${section} must be an object`);
    for (const [name, current] of Object.entries(value)) {
      const target = findReleasedName(name, releasedVersions);
      if (!target) continue;
      found.add(target);
      if (typeof current !== "string") {
        throw new HooversionError(`${path} package ${owner.name} has unsupported dependency ${name}`);
      }
      const next = rewriteNodeRequirement(current, releasedVersions.get(target)!, path, name);
      if (next !== current) {
        value[name] = next;
        changed = true;
      }
    }
  }

  assertAllDependenciesFound(path, owner, releasedVersions, found);
  if (changed) writeFileSync(path, `${JSON.stringify(json, null, 2)}\n`);
}

function updatePythonLocalDependencies(
  path: string,
  owner: NormalizedPackageConfig,
  releasedVersions: Map<string, string>,
): void {
  const lines = readFileSync(path, "utf8").split(/\r?\n/);
  const found = new Set<string>();
  let changed = false;
  let section = "";
  let inArray = false;

  for (let index = 0; index < lines.length; index += 1) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(lines[index]);
    if (heading) {
      section = heading[1];
      inArray = false;
      continue;
    }
    const relevant = isPythonDependencySection(section);
    if (!relevant) continue;

    if (inArray) {
      const result = rewritePythonRequirementsLine(lines[index], releasedVersions, found, path);
      lines[index] = result.line;
      changed ||= result.changed;
      if (/\]/.test(lines[index])) inArray = false;
      continue;
    }

    const assignment = /^\s*([A-Za-z0-9_.-]+)\s*=\s*(.*)$/.exec(lines[index]);
    if (!assignment) continue;
    const key = assignment[1];
    const value = assignment[2];
    if (
      value.startsWith("[") &&
      (key === "dependencies" || section === "project.optional-dependencies" || section.startsWith("project.optional-dependencies."))
    ) {
      const result = rewritePythonRequirementsLine(lines[index], releasedVersions, found, path);
      lines[index] = result.line;
      changed ||= result.changed;
      if (!/\]/.test(value)) inArray = true;
      continue;
    }
    if (section.startsWith("tool.poetry") && findReleasedName(key, releasedVersions)) {
      if (!/^["']/.test(value)) {
        throw new HooversionError(`${path} package ${owner.name} has unsupported dependency ${key}`);
      }
      const quote = value[0];
      const end = value.indexOf(quote, 1);
      if (end < 0) throw new HooversionError(`${path} has malformed dependency ${key}`);
      const target = findReleasedName(key, releasedVersions)!;
      const next = rewritePythonConstraint(value.slice(1, end), releasedVersions.get(target)!, path, key);
      found.add(target);
      if (next !== value.slice(1, end)) {
        lines[index] = `${lines[index].slice(0, lines[index].indexOf(value) + 1)}${next}${value.slice(end)}`;
        changed = true;
      }
    } else if (key === "dependencies" && value.startsWith("{")) {
      if (Array.from(releasedVersions.keys()).some((name) => value.includes(name))) {
        throw new HooversionError(`${path} has unsupported inline dependency table`);
      }
    }
  }

  assertAllDependenciesFound(path, owner, releasedVersions, found);
  if (changed) writeFileSync(path, lines.join("\n"));
}

function updateRustLocalDependencies(
  path: string,
  owner: NormalizedPackageConfig,
  releasedVersions: Map<string, string>,
): void {
  updateRustDependencyTables(path, owner, releasedVersions, false);
}

function updateRustWorkspaceDependencies(path: string, releasedVersions: Map<string, string>): void {
  updateRustDependencyTables(path, undefined, releasedVersions, true);
}

function updateRustDependencyTables(
  path: string,
  owner: NormalizedPackageConfig | undefined,
  releasedVersions: Map<string, string>,
  workspaceOnly: boolean,
): void {
  const lines = readFileSync(path, "utf8").split(/\r?\n/);
  const found = new Set<string>();
  let section = "";
  let active:
    | { target: string; depth: number; workspace: boolean; versionUpdated: boolean }
    | undefined;
  let dotted: { target: string; workspace: boolean; versionUpdated: boolean } | undefined;
  let changed = false;

  for (let index = 0; index < lines.length; index += 1) {
    const line = lines[index];
    if (active) {
      if (/workspace\s*=\s*true/.test(line)) active.workspace = true;
      if (!active.workspace && /^\s*version\s*=/.test(line)) {
        lines[index] = line.replace(/=\s*["'][^"']+["']/, `= "${releasedVersions.get(active.target)!}"`);
        active.versionUpdated = true;
        changed = true;
      }
      active.depth += braceDelta(line);
      if (active.depth <= 0) {
        if (!active.workspace && !active.versionUpdated) {
          throw new HooversionError(`${path} dependency ${active.target} has no supported version field`);
        }
        found.add(active.target);
        active = undefined;
      }
      continue;
    }

    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      if (dotted) finishRustDottedDependency(path, dotted, found);
      section = heading[1];
      const target = findRustDottedDependency(section, releasedVersions, workspaceOnly);
      dotted = target ? { target, workspace: false, versionUpdated: false } : undefined;
      continue;
    }
    if (dotted) {
      if (/workspace\s*=\s*true/.test(line)) dotted.workspace = true;
      if (!dotted.workspace && /^\s*version\s*=/.test(line)) {
        lines[index] = line.replace(/=\s*["'][^"']+["']/, `= "${releasedVersions.get(dotted.target)!}"`);
        dotted.versionUpdated = true;
        changed = true;
      }
      continue;
    }
    if (!isRustDependencySection(section, workspaceOnly)) continue;

    const entry = /^\s*(?:"([^"]+)"|([A-Za-z0-9_-]+))\s*=\s*(.*)$/.exec(line);
    if (!entry) continue;
    const name = entry[1] ?? entry[2];
    const target = findReleasedName(name, releasedVersions);
    if (!target) continue;
    const value = entry[3].trim();
    if (value.startsWith("{")) {
      const workspace = /workspace\s*=\s*true/.test(value);
      const versionMatch = /version\s*=\s*["'][^"']+["']/.test(value);
      const depth = braceDelta(value);
      if (depth > 0) {
        active = { target, depth, workspace, versionUpdated: false };
      } else if (!workspace && versionMatch) {
        lines[index] = line.replace(/(version\s*=\s*)["'][^"']+["']/, `$1"${releasedVersions.get(target)!}"`);
        found.add(target);
        changed = true;
      } else if (workspace) {
        found.add(target);
      } else {
        throw new HooversionError(`${path} dependency ${name} has unsupported table syntax`);
      }
      continue;
    }
    if (/^["']/.test(value)) {
      const quote = value[0];
      const end = value.indexOf(quote, 1);
      if (end < 0) throw new HooversionError(`${path} has malformed dependency ${name}`);
      lines[index] = `${line.slice(0, line.indexOf(value) + 1)}${releasedVersions.get(target)!}${value.slice(end)}`;
      found.add(target);
      changed = true;
      continue;
    }
    throw new HooversionError(`${path} dependency ${name} has unsupported value`);
  }

  if (dotted) finishRustDottedDependency(path, dotted, found);
  if (!workspaceOnly && owner) assertAllDependenciesFound(path, owner, releasedVersions, found);
  if (changed) writeFileSync(path, lines.join("\n"));
}

function updateCargoLock(cwd: string, releasedVersions: Map<string, string>): void {
  const path = join(cwd, "Cargo.lock");
  if (!existsSync(path)) return;
  const lines = readFileSync(path, "utf8").split(/\r?\n/);
  const starts = lines.flatMap((line, index) => (line.trim() === "[[package]]" ? [index] : []));
  let changed = false;

  for (let block = 0; block < starts.length; block += 1) {
    const start = starts[block];
    const end = starts[block + 1] ?? lines.length;
    const nameLine = lines.slice(start, end).find((line) => /^\s*name\s*=/.test(line));
    const name = nameLine ? readTomlString(nameLine, "name") : undefined;
    const target = name ? findReleasedName(name, releasedVersions) : undefined;
    const hasSource = lines.slice(start, end).some((line) => /^\s*source\s*=/.test(line));
    if (target && !hasSource) {
      const versionIndex = lines.findIndex((line, index) => index >= start && index < end && /^\s*version\s*=/.test(line));

      if (versionIndex < 0) throw new HooversionError(`${path} package ${name} has no version field`);
      const version = releasedVersions.get(target)!;
      const updatedVersion = lines[versionIndex].replace(/=\s*["'][^"']+["']/, `= "${version}"`);
      if (updatedVersion !== lines[versionIndex]) {
        lines[versionIndex] = updatedVersion;
        changed = true;
      }
    }

    let inDependencies = false;
    for (let index = start; index < end; index += 1) {
      if (/^\s*dependencies\s*=\s*\[/.test(lines[index])) {
        inDependencies = true;
        if (/\]/.test(lines[index])) inDependencies = false;
        continue;
      }
      if (!inDependencies || hasSource) continue;
      lines[index] = lines[index].replace(/(["'])([^"']+) \d[^"']*\1/g, (full, quote: string, dependencyName: string) => {
        const dependencyTarget = findReleasedName(dependencyName, releasedVersions);
        if (!dependencyTarget || /\s\(/.test(full)) return full;
        changed = true;
        return `${quote}${dependencyName} ${releasedVersions.get(dependencyTarget)!}${quote}`;
      });
      if (/\]/.test(lines[index])) inDependencies = false;
    }
  }

  if (changed) writeFileSync(path, lines.join("\n"));
}

export function changelogPathForPackage(pkg: NormalizedPackageConfig): string {
  return pkg.changelog || join(dirname(pkg.manifest), "CHANGELOG.md");
}

function escapeRegExp(value: string): string {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
