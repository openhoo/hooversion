import { existsSync, readFileSync, writeFileSync } from "node:fs";
import { basename, join, normalize, relative } from "node:path";
import { pathToFileURL } from "node:url";
import { HooversionError } from "./errors";
import { defaultManifestPath, readManifest } from "./manifest";
import type {
  HooversionConfig,
  NormalizedConfig,
  NormalizedPackageConfig,
  PackageConfig,
  PackageType,
} from "./types";

const configFiles = [
  "hooversion.config.ts",
  "hooversion.config.mjs",
  "hooversion.config.js",
  "hooversion.config.cjs",
  "hooversion.config.json",
];

export async function loadConfig(cwd: string, explicitPath?: string): Promise<NormalizedConfig> {
  const configPath = explicitPath ? join(cwd, explicitPath) : findConfigPath(cwd);
  if (!configPath) {
    throw new HooversionError("No hooversion config found. Run `hooversion init` first.");
  }

  let raw: HooversionConfig;
  if (configPath.endsWith(".json")) {
    raw = JSON.parse(readFileSync(configPath, "utf8")) as HooversionConfig;
  } else {
    const module = await import(`${pathToFileURL(configPath).href}?t=${Date.now()}`);
    raw = (module.default ?? module.config ?? module) as HooversionConfig;
  }

  return normalizeConfig(cwd, raw);
}

export function findConfigPath(cwd: string): string | undefined {
  return configFiles.map((name) => join(cwd, name)).find((path) => existsSync(path));
}

export function normalizeConfig(cwd: string, raw: HooversionConfig): NormalizedConfig {
  if (!raw.packages || raw.packages.length === 0) {
    throw new HooversionError("Config must define at least one package.");
  }
  const packages = raw.packages.map((pkg) => normalizePackage(cwd, pkg));

  const packageNames = new Map<string, NormalizedPackageConfig>();
  for (const pkg of packages) {
    const normalizedName = normalizeGraphName(pkg.name);
    const duplicate = packageNames.get(normalizedName);
    if (duplicate) {
      throw new HooversionError(`Duplicate package name after normalization: ${duplicate.name} and ${pkg.name}`);
    }
    packageNames.set(normalizedName, pkg);
  }

  const graph = new Map<string, string[]>();
  for (const pkg of packages) {
    const dependencies: string[] = [];
    for (const dependency of pkg.dependencies) {
      const target = packageNames.get(normalizeGraphName(dependency));
      if (!target) {
        throw new HooversionError(`Package ${pkg.name} depends on unknown package ${dependency}`);
      }
      if (target === pkg) {
        throw new HooversionError(`Package ${pkg.name} cannot depend on itself`);
      }
      dependencies.push(target.name);
    }
    pkg.dependencies = dependencies;
    graph.set(normalizeGraphName(pkg.name), dependencies.map(normalizeGraphName));
  }
  assertAcyclicPackageGraph(packages, graph);

  return {
    ...raw,
    branches: raw.branches ?? ["main"],
    tagFormat: raw.tagFormat ?? "v${version}",
    independentTagFormat: raw.independentTagFormat ?? "${name}@v${version}",
    packages,
    hooks: {
      beforeRelease: raw.hooks?.beforeRelease ?? [],
      afterVersion: raw.hooks?.afterVersion ?? [],
      afterRelease: raw.hooks?.afterRelease ?? [],
    },
    github:
      raw.github === false
        ? false
        : {
            releases: raw.github?.releases ?? true,
            repository: raw.github?.repository ?? "",
            apiUrl: raw.github?.apiUrl ?? "https://api.github.com",
          },
    outputDir: raw.outputDir ?? ".hooversion",
    push: raw.push ?? true,
  };
}

export function detectPackages(cwd: string): NormalizedPackageConfig[] {
  const candidates: PackageConfig[] = [];

  if (existsSync(join(cwd, "package.json"))) {
    candidates.push({ type: "node", path: ".", name: readJsonName(join(cwd, "package.json")) });
  }

  if (existsSync(join(cwd, "Cargo.toml"))) {
    candidates.push(...detectCargoPackages(cwd));
  }

  if (existsSync(join(cwd, "pyproject.toml"))) {
    candidates.push({ type: "python", path: ".", name: readTomlName(join(cwd, "pyproject.toml"), "project") });
  }

  if (existsSync(join(cwd, "version"))) {
    candidates.push({ type: "version-file", path: ".", name: basename(cwd) });
  }

  const seen = new Set<string>();
  return candidates
    .filter((pkg) => {
      const key = `${pkg.type}:${normalize(pkg.path)}`;
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    })
    .map((pkg) => normalizePackage(cwd, pkg));
}

export function writeDefaultConfig(cwd: string, packages = detectPackages(cwd)): string {
  if (packages.length === 0) {
    throw new HooversionError("Could not detect package.json, Cargo.toml, pyproject.toml, or version.");
  }

  const body = `export default {
  branches: ["main"],
  packages: [
${packages
  .map(
    (pkg) => `    {
      name: ${JSON.stringify(pkg.name)},
      path: ${JSON.stringify(pkg.path)},
      type: ${JSON.stringify(pkg.type)},
      manifest: ${JSON.stringify(pkg.manifest)},
      changelog: ${JSON.stringify(pkg.changelog)},
      scopes: ${JSON.stringify(pkg.scopes)},
      dependencies: ${JSON.stringify(pkg.dependencies)},
    },`,
  )
  .join("\n")}
  ],
  hooks: {
    afterVersion: [],
  },
  github: {
    releases: true,
  },
};
`;

  const path = join(cwd, "hooversion.config.ts");
  writeFileSync(path, body);
  return path;
}

function normalizePackage(cwd: string, pkg: PackageConfig): NormalizedPackageConfig {
  const packagePath = normalizeRelative(pkg.path || ".");
  const manifest = normalizeRelative(pkg.manifest ?? defaultManifestPath(pkg.type, packagePath));
  const manifestPackage: PackageConfig = { ...pkg, path: packagePath, manifest };
  const info = readManifest(cwd, {
    ...manifestPackage,
    changelog: pkg.changelog ?? defaultChangelog(packagePath),
    scopes: pkg.scopes ?? [],
    dependencies: pkg.dependencies ?? [],
    assets: pkg.assets ?? [],
  } as NormalizedPackageConfig);

  const name = (pkg.name || info.name).trim();
  return {
    ...pkg,
    name,
    path: packagePath,
    type: pkg.type,
    manifest,
    changelog: normalizeRelative(pkg.changelog ?? defaultChangelog(packagePath)),
    scopes: [...new Set([name, ...(pkg.scopes ?? [])])],
    dependencies: (pkg.dependencies ?? []).map((dependency) => dependency.trim()),
    assets: pkg.assets ?? [],
  };
}

function normalizeGraphName(name: string): string {
  return name.trim().toLowerCase();
}

function assertAcyclicPackageGraph(packages: NormalizedPackageConfig[], graph: Map<string, string[]>): void {
  const state = new Map<string, "visiting" | "visited">();
  const stack: string[] = [];

  const visit = (name: string): void => {
    if (state.get(name) === "visited") return;
    if (state.get(name) === "visiting") {
      const cycleStart = stack.indexOf(name);
      throw new HooversionError(`Package dependency cycle detected: ${[...stack.slice(cycleStart), name].join(" -> ")}`);
    }
    state.set(name, "visiting");
    stack.push(name);
    for (const dependency of graph.get(name) ?? []) visit(dependency);
    stack.pop();
    state.set(name, "visited");
  };

  for (const pkg of packages) visit(normalizeGraphName(pkg.name));
}

function detectCargoPackages(cwd: string): PackageConfig[] {
  const root = join(cwd, "Cargo.toml");
  const text = readFileSync(root, "utf8");
  const packages: PackageConfig[] = [];
  if (/\[package\]/.test(text)) {
    packages.push({ type: "rust", path: ".", name: readTomlName(root, "package") });
  }

  const members = readTomlArray(text, "workspace", "members");
  for (const member of members) {
    const manifest = join(cwd, member, "Cargo.toml");
    if (existsSync(manifest)) {
      packages.push({
        type: "rust",
        path: normalizeRelative(member),
        name: readTomlName(manifest, "package"),
      });
    }
  }

  return packages;
}

function defaultChangelog(packagePath: string): string {
  return packagePath === "." ? "CHANGELOG.md" : join(packagePath, "CHANGELOG.md");
}

function readJsonName(path: string): string {
  const json = JSON.parse(readFileSync(path, "utf8")) as { name?: string };
  if (!json.name) throw new HooversionError(`${path} must contain a name`);
  return json.name;
}

function readTomlName(path: string, sectionName: string): string {
  const section = readTomlSection(readFileSync(path, "utf8"), sectionName);
  const name = readTomlString(section, "name");
  if (!name) throw new HooversionError(`${path} [${sectionName}] must contain a name`);
  return name;
}

function readTomlSection(text: string, sectionName: string): string {
  const lines = text.split(/\r?\n/);
  let inSection = false;
  const section: string[] = [];
  for (const line of lines) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      if (inSection) break;
      inSection = heading[1] === sectionName;
      continue;
    }
    if (inSection) section.push(line);
  }
  return section.join("\n");
}

function readTomlString(section: string, key: string): string | undefined {
  const match = new RegExp(`^\\s*${key}\\s*=\\s*["']([^"']+)["']`, "m").exec(section);
  return match?.[1];
}

function readTomlArray(text: string, sectionName: string, key: string): string[] {
  const section = readTomlSection(text, sectionName);
  const oneLine = new RegExp(`^\\s*${key}\\s*=\\s*\\[([^\\]]*)\\]`, "m").exec(section);
  if (oneLine) {
    return Array.from(oneLine[1].matchAll(/["']([^"']+)["']/g)).map((match) => match[1]);
  }

  const lines = section.split(/\r?\n/);
  const result: string[] = [];
  let inArray = false;
  for (const line of lines) {
    if (!inArray && new RegExp(`^\\s*${key}\\s*=\\s*\\[`).test(line)) {
      inArray = true;
    }
    if (inArray) {
      for (const match of line.matchAll(/["']([^"']+)["']/g)) result.push(match[1]);
      if (/\]/.test(line)) break;
    }
  }
  return result;
}

function normalizeRelative(path: string): string {
  const normalized = normalize(path);
  if (normalized.startsWith("..")) {
    throw new HooversionError(`Path must stay inside the repository: ${path}`);
  }
  return normalized === "" ? "." : normalized;
}

export function relativeToCwd(cwd: string, path: string): string {
  return normalizeRelative(relative(cwd, path));
}
