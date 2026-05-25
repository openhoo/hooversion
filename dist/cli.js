#!/usr/bin/env bun
// @bun

// src/cli.ts
import { existsSync as existsSync4, readFileSync as readFileSync6 } from "fs";
import { basename as basename2 } from "path";

// src/errors.ts
class HooversionError extends Error {
  code;
  constructor(message, code = 1) {
    super(message);
    this.code = code;
    this.name = "HooversionError";
  }
}

// src/commit.ts
var conventionalHeaderPattern = /^([a-z][a-z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$/;
var breakingFooterPattern = /(?:^|\n)BREAKING[ -]CHANGE: /;
var ignoredSubjectPatterns = [
  /^Merge /,
  /^Revert "/,
  /^revert: /i,
  /^chore\(release\)!?: /
];
var releaseRules = {
  feat: "minor",
  fix: "patch",
  perf: "patch"
};
function isIgnoredSubject(subject) {
  return ignoredSubjectPatterns.some((pattern) => pattern.test(subject));
}
function parseCommit(raw) {
  const ignored = isIgnoredSubject(raw.subject);
  const match = conventionalHeaderPattern.exec(raw.subject);
  if (!match) {
    return {
      ...raw,
      type: "",
      description: raw.subject,
      breaking: false,
      ignored
    };
  }
  const [, type, scope, bang, description] = match;
  const breaking = Boolean(bang) || breakingFooterPattern.test(raw.body);
  return {
    ...raw,
    type,
    scope,
    description,
    breaking,
    releaseType: breaking ? "major" : releaseRules[type],
    ignored
  };
}
function lintCommit(raw) {
  if (isIgnoredSubject(raw.subject))
    return [];
  const issues = [];
  const match = conventionalHeaderPattern.exec(raw.subject);
  if (!match) {
    issues.push({
      hash: raw.hash,
      subject: raw.subject,
      message: "header must match '<type>(optional-scope)!: description'"
    });
    return issues;
  }
  const [, type, , , description] = match;
  if (!type) {
    issues.push({ hash: raw.hash, subject: raw.subject, message: "type is required" });
  }
  if (!description.trim()) {
    issues.push({ hash: raw.hash, subject: raw.subject, message: "description is required" });
  }
  if (raw.subject.length > 100) {
    issues.push({
      hash: raw.hash,
      subject: raw.subject,
      message: "header must not exceed 100 characters"
    });
  }
  return issues;
}
function parseCommits(rawCommits) {
  return rawCommits.map(parseCommit);
}

// src/config.ts
import { existsSync, readFileSync as readFileSync2, writeFileSync as writeFileSync2 } from "fs";
import { join as join2, normalize, relative } from "path";
import { pathToFileURL } from "url";

// src/manifest.ts
import { readFileSync, writeFileSync } from "fs";
import { dirname, join } from "path";
function defaultManifestPath(type, packagePath) {
  if (type === "node")
    return join(packagePath, "package.json");
  if (type === "rust")
    return join(packagePath, "Cargo.toml");
  return join(packagePath, "pyproject.toml");
}
function readManifest(cwd, pkg) {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node")
    return readPackageJson(path);
  if (pkg.type === "rust")
    return readTomlPackage(path, "package");
  return readTomlPackage(path, "project");
}
function updateManifestVersion(cwd, pkg, version) {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node") {
    const json = JSON.parse(readFileSync(path, "utf8"));
    json.version = version;
    writeFileSync(path, `${JSON.stringify(json, null, 2)}
`);
    return;
  }
  const section = pkg.type === "rust" ? "package" : "project";
  updateTomlSectionVersion(path, section, version);
}
function updateLocalDependencyVersions(cwd, packages, releasedVersions) {
  for (const pkg of packages) {
    const path = join(cwd, pkg.manifest);
    if (pkg.type === "node") {
      updateNodeLocalDependencies(path, releasedVersions);
    } else if (pkg.type === "rust") {
      updateRustLocalDependencies(path, releasedVersions);
    }
  }
}
function readPackageJson(path) {
  const json = JSON.parse(readFileSync(path, "utf8"));
  if (!json.name || !json.version) {
    throw new HooversionError(`${path} must contain name and version`);
  }
  return { name: json.name, version: json.version };
}
function readTomlPackage(path, sectionName) {
  const text = readFileSync(path, "utf8");
  const section = getTomlSection(text, sectionName);
  const name = readTomlString(section, "name");
  const version = readTomlString(section, "version");
  if (!name || !version) {
    throw new HooversionError(`${path} [${sectionName}] must contain name and version`);
  }
  return { name, version };
}
function getTomlSection(text, sectionName) {
  const lines = text.split(/\r?\n/);
  let inSection = false;
  const sectionLines = [];
  for (const line of lines) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      if (inSection)
        break;
      inSection = heading[1] === sectionName;
      continue;
    }
    if (inSection)
      sectionLines.push(line);
  }
  return sectionLines.join(`
`);
}
function readTomlString(section, key) {
  const pattern = new RegExp(`^\\s*${escapeRegExp(key)}\\s*=\\s*["']([^"']+)["']\\s*$`, "m");
  return pattern.exec(section)?.[1];
}
function updateTomlSectionVersion(path, sectionName, version) {
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
  writeFileSync(path, output.join(`
`));
}
function updateNodeLocalDependencies(path, releasedVersions) {
  const json = JSON.parse(readFileSync(path, "utf8"));
  let changed = false;
  for (const section of ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"]) {
    const deps = json[section];
    if (!deps || typeof deps !== "object")
      continue;
    for (const [name, version] of releasedVersions) {
      if (typeof deps[name] === "string") {
        const current = deps[name];
        const prefix = current.match(/^[~^]/)?.[0] ?? "";
        deps[name] = `${prefix}${version}`;
        changed = true;
      }
    }
  }
  if (changed) {
    writeFileSync(path, `${JSON.stringify(json, null, 2)}
`);
  }
}
function updateRustLocalDependencies(path, releasedVersions) {
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
function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// src/config.ts
var configFiles = [
  "hooversion.config.ts",
  "hooversion.config.mjs",
  "hooversion.config.js",
  "hooversion.config.cjs",
  "hooversion.config.json"
];
async function loadConfig(cwd, explicitPath) {
  const configPath = explicitPath ? join2(cwd, explicitPath) : findConfigPath(cwd);
  if (!configPath) {
    throw new HooversionError("No hooversion config found. Run `hooversion init` first.");
  }
  let raw;
  if (configPath.endsWith(".json")) {
    raw = JSON.parse(readFileSync2(configPath, "utf8"));
  } else {
    const module = await import(`${pathToFileURL(configPath).href}?t=${Date.now()}`);
    raw = module.default ?? module.config ?? module;
  }
  return normalizeConfig(cwd, raw);
}
function findConfigPath(cwd) {
  return configFiles.map((name) => join2(cwd, name)).find((path) => existsSync(path));
}
function normalizeConfig(cwd, raw) {
  if (!raw.packages || raw.packages.length === 0) {
    throw new HooversionError("Config must define at least one package.");
  }
  const packages = raw.packages.map((pkg) => normalizePackage(cwd, pkg));
  const packageNames = new Set(packages.map((pkg) => pkg.name));
  for (const pkg of packages) {
    for (const dependency of pkg.dependencies) {
      if (!packageNames.has(dependency)) {
        throw new HooversionError(`Package ${pkg.name} depends on unknown package ${dependency}`);
      }
    }
  }
  return {
    ...raw,
    branches: raw.branches ?? ["main"],
    tagFormat: raw.tagFormat ?? "v${version}",
    independentTagFormat: raw.independentTagFormat ?? "${name}@v${version}",
    packages,
    hooks: {
      beforeRelease: raw.hooks?.beforeRelease ?? [],
      afterVersion: raw.hooks?.afterVersion ?? [],
      afterRelease: raw.hooks?.afterRelease ?? []
    },
    github: raw.github === false ? false : {
      releases: raw.github?.releases ?? true,
      repository: raw.github?.repository ?? "",
      apiUrl: raw.github?.apiUrl ?? "https://api.github.com"
    },
    outputDir: raw.outputDir ?? ".hooversion",
    push: raw.push ?? true
  };
}
function detectPackages(cwd) {
  const candidates = [];
  if (existsSync(join2(cwd, "package.json"))) {
    candidates.push({ type: "node", path: ".", name: readJsonName(join2(cwd, "package.json")) });
  }
  if (existsSync(join2(cwd, "Cargo.toml"))) {
    candidates.push(...detectCargoPackages(cwd));
  }
  if (existsSync(join2(cwd, "pyproject.toml"))) {
    candidates.push({ type: "python", path: ".", name: readTomlName(join2(cwd, "pyproject.toml"), "project") });
  }
  const seen = new Set;
  return candidates.filter((pkg) => {
    const key = `${pkg.type}:${normalize(pkg.path)}`;
    if (seen.has(key))
      return false;
    seen.add(key);
    return true;
  }).map((pkg) => normalizePackage(cwd, pkg));
}
function writeDefaultConfig(cwd, packages = detectPackages(cwd)) {
  if (packages.length === 0) {
    throw new HooversionError("Could not detect package.json, Cargo.toml, or pyproject.toml.");
  }
  const body = `export default {
  branches: ["main"],
  packages: [
${packages.map((pkg) => `    {
      name: ${JSON.stringify(pkg.name)},
      path: ${JSON.stringify(pkg.path)},
      type: ${JSON.stringify(pkg.type)},
      manifest: ${JSON.stringify(pkg.manifest)},
      changelog: ${JSON.stringify(pkg.changelog)},
      scopes: ${JSON.stringify(pkg.scopes)},
      dependencies: ${JSON.stringify(pkg.dependencies)},
    },`).join(`
`)}
  ],
  hooks: {
    afterVersion: [],
  },
  github: {
    releases: true,
  },
};
`;
  const path = join2(cwd, "hooversion.config.ts");
  writeFileSync2(path, body);
  return path;
}
function normalizePackage(cwd, pkg) {
  const packagePath = normalizeRelative(pkg.path || ".");
  const manifest = normalizeRelative(pkg.manifest ?? defaultManifestPath(pkg.type, packagePath));
  const manifestPackage = { ...pkg, path: packagePath, manifest };
  const info = readManifest(cwd, {
    ...manifestPackage,
    changelog: pkg.changelog ?? defaultChangelog(packagePath),
    scopes: pkg.scopes ?? [],
    dependencies: pkg.dependencies ?? [],
    assets: pkg.assets ?? []
  });
  const name = pkg.name || info.name;
  return {
    ...pkg,
    name,
    path: packagePath,
    type: pkg.type,
    manifest,
    changelog: normalizeRelative(pkg.changelog ?? defaultChangelog(packagePath)),
    scopes: [...new Set([name, ...pkg.scopes ?? []])],
    dependencies: pkg.dependencies ?? [],
    assets: pkg.assets ?? []
  };
}
function detectCargoPackages(cwd) {
  const root = join2(cwd, "Cargo.toml");
  const text = readFileSync2(root, "utf8");
  const packages = [];
  if (/\[package\]/.test(text)) {
    packages.push({ type: "rust", path: ".", name: readTomlName(root, "package") });
  }
  const members = readTomlArray(text, "workspace", "members");
  for (const member of members) {
    const manifest = join2(cwd, member, "Cargo.toml");
    if (existsSync(manifest)) {
      packages.push({
        type: "rust",
        path: normalizeRelative(member),
        name: readTomlName(manifest, "package")
      });
    }
  }
  return packages;
}
function defaultChangelog(packagePath) {
  return packagePath === "." ? "CHANGELOG.md" : join2(packagePath, "CHANGELOG.md");
}
function readJsonName(path) {
  const json = JSON.parse(readFileSync2(path, "utf8"));
  if (!json.name)
    throw new HooversionError(`${path} must contain a name`);
  return json.name;
}
function readTomlName(path, sectionName) {
  const section = readTomlSection(readFileSync2(path, "utf8"), sectionName);
  const name = readTomlString2(section, "name");
  if (!name)
    throw new HooversionError(`${path} [${sectionName}] must contain a name`);
  return name;
}
function readTomlSection(text, sectionName) {
  const lines = text.split(/\r?\n/);
  let inSection = false;
  const section = [];
  for (const line of lines) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(line);
    if (heading) {
      if (inSection)
        break;
      inSection = heading[1] === sectionName;
      continue;
    }
    if (inSection)
      section.push(line);
  }
  return section.join(`
`);
}
function readTomlString2(section, key) {
  const match = new RegExp(`^\\s*${key}\\s*=\\s*["']([^"']+)["']`, "m").exec(section);
  return match?.[1];
}
function readTomlArray(text, sectionName, key) {
  const section = readTomlSection(text, sectionName);
  const oneLine = new RegExp(`^\\s*${key}\\s*=\\s*\\[([^\\]]*)\\]`, "m").exec(section);
  if (oneLine) {
    return Array.from(oneLine[1].matchAll(/["']([^"']+)["']/g)).map((match) => match[1]);
  }
  const lines = section.split(/\r?\n/);
  const result = [];
  let inArray = false;
  for (const line of lines) {
    if (!inArray && new RegExp(`^\\s*${key}\\s*=\\s*\\[`).test(line)) {
      inArray = true;
    }
    if (inArray) {
      for (const match of line.matchAll(/["']([^"']+)["']/g))
        result.push(match[1]);
      if (/\]/.test(line))
        break;
    }
  }
  return result;
}
function normalizeRelative(path) {
  const normalized = normalize(path);
  if (normalized.startsWith("..")) {
    throw new HooversionError(`Path must stay inside the repository: ${path}`);
  }
  return normalized === "" ? "." : normalized;
}

// src/process.ts
import { spawnSync } from "child_process";
function runCommand(command, args, cwd) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    env: process.env
  });
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? ""
  };
}
function runShell(command, cwd) {
  const result = spawnSync(command, {
    cwd,
    encoding: "utf8",
    env: process.env,
    shell: process.env.SHELL ?? "/bin/sh"
  });
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? ""
  };
}

// src/git.ts
function git(cwd, args, allowFailure = false) {
  const result = runCommand("git", args, cwd);
  if (result.code !== 0 && !allowFailure) {
    throw new HooversionError(`git ${args.join(" ")} failed:
${result.stderr || result.stdout}`);
  }
  return result.stdout.trimEnd();
}
function isGitRepository(cwd) {
  return runCommand("git", ["rev-parse", "--is-inside-work-tree"], cwd).code === 0;
}
function getCurrentBranch(cwd) {
  const branch = git(cwd, ["branch", "--show-current"]).trim();
  if (branch)
    return branch;
  if (process.env.GITHUB_HEAD_REF)
    return process.env.GITHUB_HEAD_REF;
  if (process.env.GITHUB_REF_TYPE !== "tag" && process.env.GITHUB_REF_NAME)
    return process.env.GITHUB_REF_NAME;
  return git(cwd, ["rev-parse", "--abbrev-ref", "HEAD"]).trim();
}
function ensureCleanWorkingTree(cwd) {
  const status = git(cwd, ["status", "--porcelain"]);
  if (status.trim()) {
    throw new HooversionError(`Working tree must be clean before release:
${status}`);
  }
}
function getLatestTag(cwd, pattern) {
  const output = git(cwd, ["describe", "--tags", "--abbrev=0", "--match", pattern], true).trim();
  return output || undefined;
}
function tagExists(cwd, tag) {
  return runCommand("git", ["rev-parse", "--verify", "--quiet", `refs/tags/${tag}`], cwd).code === 0;
}
function getCommits(cwd, fromRef, toRef = "HEAD") {
  const range = fromRef ? `${fromRef}..${toRef}` : toRef;
  const revList = git(cwd, ["rev-list", "--reverse", range], true).trim();
  if (!revList)
    return [];
  return revList.split(`
`).map((hash) => {
    const subject = git(cwd, ["show", "-s", "--format=%s", hash]);
    const body = git(cwd, ["show", "-s", "--format=%b", hash]);
    const files = git(cwd, ["diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash], true).split(`
`).map((file) => file.trim()).filter(Boolean);
    return { hash, subject, body, files };
  });
}
function getLastCommit(cwd) {
  const hash = git(cwd, ["rev-parse", "HEAD"]).trim();
  const subject = git(cwd, ["show", "-s", "--format=%s", hash]);
  const body = git(cwd, ["show", "-s", "--format=%b", hash]);
  const files = git(cwd, ["diff-tree", "--root", "--no-commit-id", "--name-only", "-r", hash], true).split(`
`).map((file) => file.trim()).filter(Boolean);
  return { hash, subject, body, files };
}
function createReleaseCommit(cwd, message) {
  git(cwd, ["add", "--all"]);
  const status = git(cwd, ["status", "--porcelain"]);
  if (!status.trim())
    return;
  git(cwd, ["commit", "-m", message]);
}
function createAnnotatedTag(cwd, tag, message) {
  git(cwd, ["tag", "-a", tag, "-m", message]);
}
function pushRelease(cwd, branch, tags) {
  git(cwd, ["push", "origin", `HEAD:${branch}`]);
  if (tags.length > 0) {
    git(cwd, ["push", "origin", ...tags]);
  }
}
function getOriginRepository(cwd) {
  const remote = git(cwd, ["config", "--get", "remote.origin.url"], true).trim();
  if (!remote)
    return;
  const sshMatch = /^git@[^:]+:([^/]+\/[^/.]+)(?:\.git)?$/.exec(remote);
  if (sshMatch)
    return sshMatch[1];
  const httpsMatch = /^https?:\/\/[^/]+\/([^/]+\/[^/.]+)(?:\.git)?$/.exec(remote);
  if (httpsMatch)
    return httpsMatch[1];
  return;
}

// src/semver.ts
var versionPattern = /^(\d+)\.(\d+)\.(\d+)(?:[-+].*)?$/;
function parseVersion(version) {
  const match = versionPattern.exec(version.trim());
  if (!match) {
    throw new HooversionError(`Invalid semantic version: ${version}`);
  }
  return {
    major: Number(match[1]),
    minor: Number(match[2]),
    patch: Number(match[3])
  };
}
function bumpVersion(version, releaseType) {
  const parts = parseVersion(version);
  if (releaseType === "major") {
    return `${parts.major + 1}.0.0`;
  }
  if (releaseType === "minor") {
    return `${parts.major}.${parts.minor + 1}.0`;
  }
  return `${parts.major}.${parts.minor}.${parts.patch + 1}`;
}
function highestReleaseType(types) {
  let result;
  for (const type of types) {
    if (!type)
      continue;
    if (type === "major")
      return "major";
    if (type === "minor")
      result = result === "major" ? result : "minor";
    if (type === "patch" && !result)
      result = "patch";
  }
  return result;
}
function minReleaseType(current, minimum) {
  return highestReleaseType([current, minimum]) ?? minimum;
}

// src/changelog.ts
import { existsSync as existsSync2, readFileSync as readFileSync3, writeFileSync as writeFileSync3 } from "fs";
import { dirname as dirname2, join as join3 } from "path";
import { mkdirSync } from "fs";
var groupTitles = {
  major: "Breaking Changes",
  feat: "Features",
  fix: "Bug Fixes",
  perf: "Performance"
};
function generateReleaseNotes(release) {
  const date = new Date().toISOString().slice(0, 10);
  const lines = [`## ${release.nextVersion} (${date})`, ""];
  const groups = groupCommits(release.commits);
  for (const [title, commits] of groups) {
    lines.push(`### ${title}`, "");
    for (const commit of commits) {
      const scope = commit.scope ? `**${commit.scope}:** ` : "";
      lines.push(`- ${scope}${commit.description} (${commit.hash.slice(0, 7)})`);
      if (commit.breaking && commit.body) {
        const breaking = extractBreakingChange(commit.body);
        if (breaking)
          lines.push(`  - BREAKING: ${breaking}`);
      }
    }
    lines.push("");
  }
  return lines.join(`
`).trimEnd();
}
function updateChangelog(cwd, release) {
  const path = join3(cwd, release.changelogPath);
  mkdirSync(dirname2(path), { recursive: true });
  const existing = existsSync2(path) ? readFileSync3(path, "utf8") : "";
  const title = `# ${release.package.name} Changelog`;
  const normalizedExisting = existing.trim() ? existing : `${title}
`;
  const [firstLine, ...rest] = normalizedExisting.split(/\r?\n/);
  const header = firstLine.startsWith("# ") ? firstLine : title;
  const body = firstLine.startsWith("# ") ? rest.join(`
`).replace(/^\n+/, "") : normalizedExisting;
  const next = `${header}

${release.notes}

${body.trimEnd()}
`;
  writeFileSync3(path, next);
}
function groupCommits(commits) {
  const buckets = new Map;
  for (const commit of commits) {
    if (commit.breaking) {
      pushBucket(buckets, groupTitles.major, commit);
    } else if (commit.type in groupTitles) {
      pushBucket(buckets, groupTitles[commit.type], commit);
    } else {
      pushBucket(buckets, "Other Changes", commit);
    }
  }
  return Array.from(buckets.entries()).filter(([, values]) => values.length > 0);
}
function pushBucket(map, key, commit) {
  const bucket = map.get(key) ?? [];
  bucket.push(commit);
  map.set(key, bucket);
}
function extractBreakingChange(body) {
  const match = /(?:^|\n)BREAKING[ -]CHANGE:\s*([^\n]+)/.exec(body);
  return match?.[1]?.trim();
}

// src/routing.ts
import { relative as relative2 } from "path";
function directAffectedPackages(commit, packages) {
  const affected = new Set;
  const scoped = scopeTargets(commit.scope, packages);
  if (scoped.size > 0) {
    for (const name of scoped)
      affected.add(name);
  }
  for (const file of commit.files) {
    for (const pkg of packages) {
      if (fileBelongsToPackage(file, pkg, packages))
        affected.add(pkg.name);
    }
  }
  return affected;
}
function propagateDependencies(affected, packages) {
  const result = new Set(affected);
  let changed = true;
  while (changed) {
    changed = false;
    for (const pkg of packages) {
      if (result.has(pkg.name))
        continue;
      if (pkg.dependencies.some((dependency) => result.has(dependency))) {
        result.add(pkg.name);
        changed = true;
      }
    }
  }
  return result;
}
function scopeTargets(scope, packages) {
  const result = new Set;
  if (!scope)
    return result;
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
function fileBelongsToPackage(file, pkg, packages) {
  if (pkg.path === ".") {
    return !packages.some((other) => other.path !== "." && isInside(file, other.path));
  }
  return isInside(file, pkg.path);
}
function isInside(file, packagePath) {
  const rel = relative2(packagePath, file);
  return rel === "" || !rel.startsWith("..") && rel !== ".";
}

// src/plan.ts
function renderTag(format, pkg, version) {
  return format.replaceAll("${name}", pkg.name).replaceAll("${version}", version);
}
function tagPatternForPackage(config, pkg) {
  if (config.packages.length === 1)
    return "v[0-9]*";
  return config.independentTagFormat.replaceAll("${name}", pkg.name).replaceAll("${version}", "[0-9]*");
}
function tagForPackage(config, pkg, version) {
  return renderTag(config.packages.length === 1 ? config.tagFormat : config.independentTagFormat, pkg, version);
}
function createReleasePlan(cwd, config) {
  const branch = getCurrentBranch(cwd);
  const independent = config.packages.length > 1;
  return independent ? createIndependentPlan(cwd, config, branch) : createSinglePackagePlan(cwd, config, branch);
}
function createSinglePackagePlan(cwd, config, branch) {
  const pkg = config.packages[0];
  const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
  const commits = parseCommits(getCommits(cwd, latestTag)).filter((commit) => !commit.ignored);
  const releaseType = highestReleaseType(commits.map((commit) => commit.releaseType));
  const releases = releaseType ? [buildRelease(cwd, config, pkg, commits, releaseType, latestTag, false)] : [];
  return { cwd, branch, independent: false, releases, unmatchedCommits: [] };
}
function createIndependentPlan(cwd, config, branch) {
  const latestTags = new Map;
  const parsedCommitsByPackage = new Map;
  const candidateCommits = new Map;
  const directAffectedByCommit = new Map;
  const affectedByCommit = new Map;
  for (const pkg of config.packages) {
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    latestTags.set(pkg.name, latestTag);
    const commits = parseCommits(getCommits(cwd, latestTag)).filter((commit) => !commit.ignored);
    parsedCommitsByPackage.set(pkg.name, commits);
    for (const commit of commits) {
      candidateCommits.set(commit.hash, commit);
    }
  }
  for (const commit of candidateCommits.values()) {
    const direct = directAffectedPackages(commit, config.packages);
    directAffectedByCommit.set(commit.hash, direct);
    affectedByCommit.set(commit.hash, propagateDependencies(direct, config.packages));
  }
  const unmatchedCommits = Array.from(candidateCommits.values()).filter((commit) => commit.releaseType && (directAffectedByCommit.get(commit.hash)?.size ?? 0) === 0);
  if (unmatchedCommits.length > 0) {
    return { cwd, branch, independent: true, releases: [], unmatchedCommits };
  }
  const releaseTypes = new Map;
  const releaseCommits = new Map;
  const dependencyTriggered = new Set;
  for (const pkg of config.packages) {
    const packageCommits = parsedCommitsByPackage.get(pkg.name) ?? [];
    for (const commit of packageCommits) {
      const affected = affectedByCommit.get(commit.hash) ?? new Set;
      if (!affected.has(pkg.name))
        continue;
      if (!commit.releaseType)
        continue;
      const direct = directAffectedByCommit.get(commit.hash) ?? new Set;
      const typeForPackage = direct.has(pkg.name) ? commit.releaseType : "patch";
      if (!direct.has(pkg.name))
        dependencyTriggered.add(pkg.name);
      releaseTypes.set(pkg.name, minReleaseType(releaseTypes.get(pkg.name), typeForPackage));
      const commits = releaseCommits.get(pkg.name) ?? [];
      commits.push(commit);
      releaseCommits.set(pkg.name, commits);
    }
  }
  const releases = [];
  for (const pkg of config.packages) {
    const releaseType = releaseTypes.get(pkg.name);
    if (!releaseType)
      continue;
    releases.push(buildRelease(cwd, config, pkg, uniqueCommits(releaseCommits.get(pkg.name) ?? []), releaseType, latestTags.get(pkg.name), dependencyTriggered.has(pkg.name)));
  }
  return { cwd, branch, independent: true, releases, unmatchedCommits: [] };
}
function buildRelease(cwd, config, pkg, commits, releaseType, latestTag, dependencyTriggered) {
  const currentVersion = readManifest(cwd, pkg).version;
  const nextVersion = bumpVersion(currentVersion, releaseType);
  const tag = tagForPackage(config, pkg, nextVersion);
  const releaseWithoutNotes = {
    package: pkg,
    currentVersion,
    nextVersion,
    releaseType,
    tag,
    latestTag,
    commits,
    notes: "",
    changelogPath: pkg.changelog,
    dependencyTriggered
  };
  const notes = generateReleaseNotes(releaseWithoutNotes);
  return { ...releaseWithoutNotes, notes };
}
function uniqueCommits(commits) {
  const seen = new Set;
  return commits.filter((commit) => {
    if (seen.has(commit.hash))
      return false;
    seen.add(commit.hash);
    return true;
  });
}

// src/release.ts
import { mkdirSync as mkdirSync3 } from "fs";
import { join as join6 } from "path";

// src/github.ts
import { readFileSync as readFileSync4 } from "fs";
import { basename, join as join4 } from "path";
async function publishGitHubRelease(cwd, config, release) {
  if (config.github === false || !config.github.releases)
    return;
  const token = process.env.GITHUB_TOKEN || process.env.GH_TOKEN;
  if (!token) {
    throw new HooversionError("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.");
  }
  const repository = config.github.repository || getOriginRepository(cwd);
  if (!repository) {
    throw new HooversionError("Could not determine GitHub repository. Set github.repository in hooversion config.");
  }
  const apiUrl = config.github.apiUrl.replace(/\/$/, "");
  const response = await githubFetch(`${apiUrl}/repos/${repository}/releases`, token, {
    method: "POST",
    body: JSON.stringify({
      tag_name: release.tag,
      name: `${release.package.name} ${release.nextVersion}`,
      body: release.notes,
      draft: false,
      prerelease: false
    }),
    headers: {
      "content-type": "application/json"
    }
  });
  for (const asset of release.package.assets) {
    await uploadAsset(response.upload_url, token, join4(cwd, asset));
  }
  return response.html_url;
}
async function uploadAsset(uploadUrlTemplate, token, path) {
  const uploadUrl = uploadUrlTemplate.replace(/\{.*$/, "");
  const name = basename(path);
  const data = readFileSync4(path);
  await githubFetch(`${uploadUrl}?name=${encodeURIComponent(name)}`, token, {
    method: "POST",
    body: data,
    headers: {
      "content-type": "application/octet-stream"
    }
  });
}
async function githubFetch(url, token, init) {
  const response = await fetch(url, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
      ...init.headers ?? {}
    }
  });
  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }
  return await response.json();
}

// src/output.ts
import { appendFileSync, mkdirSync as mkdirSync2, writeFileSync as writeFileSync4 } from "fs";
import { join as join5 } from "path";
function writeReleaseOutputs(cwd, config, plan) {
  const outputDir = join5(cwd, config.outputDir);
  mkdirSync2(outputDir, { recursive: true });
  for (const release of plan.releases) {
    writeFileSync4(join5(outputDir, `${sanitizeFileName(release.tag)}-notes.md`), `${release.notes}
`);
  }
  const payload = {
    published: plan.releases.length > 0,
    releases: plan.releases.map((release) => ({
      name: release.package.name,
      version: release.nextVersion,
      tag: release.tag,
      type: release.releaseType,
      notesPath: `${config.outputDir}/${sanitizeFileName(release.tag)}-notes.md`
    }))
  };
  writeFileSync4(join5(outputDir, "outputs.json"), `${JSON.stringify(payload, null, 2)}
`);
  if (plan.releases.length === 1) {
    writeFileSync4(join5(cwd, ".release-version"), `${plan.releases[0].nextVersion}
`);
  }
  if (process.env.GITHUB_OUTPUT) {
    const lines = [`published=${payload.published}`, `releases_json=${JSON.stringify(payload.releases)}`];
    if (plan.releases.length === 1) {
      lines.push(`version=${plan.releases[0].nextVersion}`, `tag=${plan.releases[0].tag}`);
    }
    appendFileSync(process.env.GITHUB_OUTPUT, `${lines.join(`
`)}
`);
  }
}
function sanitizeFileName(value) {
  return value.replace(/[^a-zA-Z0-9._@-]/g, "-");
}

// src/release.ts
async function executeRelease(cwd, config, plan, options = {}) {
  validatePlan(cwd, config, plan);
  if (plan.releases.length === 0) {
    if (!options.dryRun)
      writeReleaseOutputs(cwd, config, plan);
    return;
  }
  if (options.dryRun)
    return;
  ensureCleanWorkingTree(cwd);
  runHooks(cwd, config.hooks.beforeRelease);
  const releasedVersions = new Map(plan.releases.map((release) => [release.package.name, release.nextVersion]));
  for (const release of plan.releases) {
    updateManifestVersion(cwd, release.package, release.nextVersion);
  }
  updateLocalDependencyVersions(cwd, config.packages, releasedVersions);
  for (const release of plan.releases) {
    updateChangelog(cwd, release);
  }
  runHooks(cwd, config.hooks.afterVersion);
  createReleaseCommit(cwd, releaseCommitMessage(plan));
  for (const release of plan.releases) {
    createAnnotatedTag(cwd, release.tag, `${release.package.name} ${release.nextVersion}`);
  }
  const shouldPush = options.push ?? config.push;
  if (shouldPush) {
    pushRelease(cwd, plan.branch, plan.releases.map((release) => release.tag));
  }
  mkdirSync3(join6(cwd, config.outputDir), { recursive: true });
  if (options.github ?? true) {
    for (const release of plan.releases) {
      await publishGitHubRelease(cwd, config, release);
    }
  }
  writeReleaseOutputs(cwd, config, plan);
  runHooks(cwd, config.hooks.afterRelease);
}
function validatePlan(cwd, config, plan) {
  if (!config.branches.includes(plan.branch)) {
    throw new HooversionError(`Current branch '${plan.branch}' is not a release branch. Allowed branches: ${config.branches.join(", ")}`);
  }
  if (plan.unmatchedCommits.length > 0) {
    const details = plan.unmatchedCommits.map((commit) => `${commit.hash.slice(0, 7)} ${commit.subject}`).join(`
`);
    throw new HooversionError(`Release-worthy commits could not be assigned to a package:
${details}`);
  }
  for (const release of plan.releases) {
    if (tagExists(cwd, release.tag)) {
      throw new HooversionError(`Tag already exists: ${release.tag}`);
    }
  }
}
function releaseCommitMessage(plan) {
  if (plan.releases.length === 1) {
    const release = plan.releases[0];
    return `chore(release): ${release.package.name} ${release.nextVersion}

${release.notes}`;
  }
  const summary = plan.releases.map((release) => `${release.package.name}@${release.nextVersion}`).join(", ");
  const notes = plan.releases.map((release) => `# ${release.package.name} ${release.nextVersion}

${release.notes}`).join(`

`);
  return `chore(release): ${summary}

${notes}`;
}
function runHooks(cwd, hooks) {
  for (const hook of hooks) {
    const result = runShell(hook, cwd);
    if (result.code !== 0) {
      throw new HooversionError(`Hook failed: ${hook}
${result.stderr || result.stdout}`);
    }
  }
}

// src/doctor.ts
function runDoctor(cwd, config) {
  const result = { errors: [], warnings: [], info: [] };
  if (!isGitRepository(cwd)) {
    result.errors.push("Current directory is not a git repository.");
    return result;
  }
  const branch = getCurrentBranch(cwd);
  if (!config.branches.includes(branch)) {
    result.warnings.push(`Current branch '${branch}' is not a configured release branch.`);
  } else {
    result.info.push(`Release branch: ${branch}`);
  }
  for (const pkg of config.packages) {
    const manifest = readManifest(cwd, pkg);
    result.info.push(`${pkg.name}: manifest version ${manifest.version}`);
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    if (!latestTag) {
      result.warnings.push(`${pkg.name}: no release tag found; first release will use full reachable history.`);
      continue;
    }
    const tagVersion = extractTagVersion(latestTag);
    result.info.push(`${pkg.name}: latest tag ${latestTag}`);
    if (tagVersion && tagVersion !== manifest.version) {
      result.warnings.push(`${pkg.name}: manifest version ${manifest.version} differs from latest tag version ${tagVersion}.`);
    }
  }
  if (config.github !== false && config.github.releases && !process.env.GITHUB_TOKEN && !process.env.GH_TOKEN) {
    result.warnings.push("GITHUB_TOKEN or GH_TOKEN is not set; `release` cannot create GitHub releases.");
  }
  return result;
}
function extractTagVersion(tag) {
  return /(?:^|@)v(\d+\.\d+\.\d+(?:[-+][^\s]+)?)$/.exec(tag)?.[1];
}

// src/workflow.ts
import { existsSync as existsSync3, mkdirSync as mkdirSync4, readFileSync as readFileSync5, writeFileSync as writeFileSync5 } from "fs";
import { join as join7 } from "path";
var defaultActionOwnerRepo = "openhoo/hooversion";
var defaultBunVersion = "1.3.14";
function writeGitHubWorkflows(cwd, options = {}) {
  const workflowDir = join7(cwd, ".github", "workflows");
  const ciPath = join7(workflowDir, "ci.yml");
  const releasePath = join7(workflowDir, "release.yml");
  const workflows = renderGitHubWorkflows(options);
  mkdirSync4(workflowDir, { recursive: true });
  writeFileSync5(ciPath, workflows.ci);
  writeFileSync5(releasePath, workflows.release);
  return [ciPath, releasePath];
}
function renderGitHubWorkflows(options = {}) {
  const hooversionVersion = options.hooversionVersion ?? getPackageVersion();
  const actionOwnerRepo = options.actionOwnerRepo ?? defaultActionOwnerRepo;
  const actionRef = options.actionRef ?? `v${hooversionVersion}`;
  const bunVersion = options.bunVersion ?? defaultBunVersion;
  const ci = `name: CI

on:
  pull_request:
  push:
    branches:
      - main

permissions:
  contents: read

env:
  HOOVERSION_VERSION: ${hooversionVersion}

jobs:
  commitlint:
    name: Lint commits
    runs-on: ubuntu-latest
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0

      - name: Lint commits
        uses: ${actionOwnerRepo}/actions/lint@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}

  build:
    name: Test and build
    runs-on: ubuntu-latest
    needs: commitlint
    steps:
      - name: Checkout
        uses: actions/checkout@v6

      - name: Set up Bun
        uses: oven-sh/setup-bun@v2
        with:
          bun-version: ${bunVersion}

      - name: Install dependencies
        run: bun install --frozen-lockfile

      - name: Run checks
        run: bun run check
`;
  const release = `name: Release

on:
  workflow_run:
    workflows:
      - CI
    branches:
      - main
    types:
      - completed

permissions:
  contents: read

env:
  HOOVERSION_VERSION: ${hooversionVersion}

jobs:
  prepare:
    name: Prepare release PR
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && !contains(github.event.workflow_run.head_commit.message, 'chore(release):')
    runs-on: ubuntu-latest
    permissions:
      contents: write
      pull-requests: write
    steps:
      - name: Require release token
        env:
          RELEASE_TOKEN: \${{ secrets.RELEASE_TOKEN }}
        run: |
          set -euo pipefail
          if [[ -z "\${RELEASE_TOKEN}" ]]; then
            echo "::error::Configure RELEASE_TOKEN with permission to push release PR branches and open pull requests."
            exit 1
          fi

      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: main

      - name: Prepare release
        id: release
        uses: ${actionOwnerRepo}/actions/release@${actionRef}
        with:
          version: \${{ env.HOOVERSION_VERSION }}
          bun-version: ${bunVersion}
          push: "false"
          github: "false"
          github-token: \${{ secrets.RELEASE_TOKEN }}
          install-command: bun install --frozen-lockfile

      - name: Ensure release PR runs CI
        if: steps.release.outputs.published == 'true'
        run: |
          set -euo pipefail
          subject="$(git log -1 --pretty=%s)"
          if [[ "$subject" == *" [skip ci]" ]]; then
            body="$(git log -1 --pretty=%b)"
            subject="\${subject% [skip ci]}"
            git commit --amend -m "$subject" -m "$body"
          fi

      - name: Open release PR
        if: steps.release.outputs.published == 'true'
        env:
          GH_TOKEN: \${{ secrets.RELEASE_TOKEN }}
          RELEASE_VERSION: \${{ steps.release.outputs.version }}
          RELEASE_TAG: \${{ steps.release.outputs.tag }}
        run: |
          set -euo pipefail
          branch="hooversion/release-main"
          title="chore(release): \${RELEASE_VERSION}"
          body_file="$(mktemp)"

          git switch -c "$branch"
          git push --force origin "HEAD:\${branch}"

          cat > "$body_file" <<BODY
          Automated Hooversion release PR.

          - Version: \${RELEASE_VERSION}
          - Tag: \${RELEASE_TAG}

          Merging this PR runs the required CI checks before the release workflow publishes the tag and GitHub release.
          BODY

          pr="$(gh pr list --base main --head "$branch" --state open --json number --jq '.[0].number')"
          if [[ -n "$pr" ]]; then
            gh pr edit "$pr" --title "$title" --body-file "$body_file"
          else
            gh pr create --base main --head "$branch" --title "$title" --body-file "$body_file"
          fi

  publish:
    name: Publish release
    if: github.event.workflow_run.conclusion == 'success' && github.event.workflow_run.event == 'push' && contains(github.event.workflow_run.head_commit.message, 'chore(release):')
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - name: Checkout
        uses: actions/checkout@v6
        with:
          fetch-depth: 0
          ref: main

      - name: Set up Bun
        uses: oven-sh/setup-bun@v2
        with:
          bun-version: ${bunVersion}

      - name: Read package metadata
        id: package
        run: |
          set -euo pipefail
          name="$(bun -e 'const p = await Bun.file("package.json").json(); console.log(p.name)')"
          version="$(bun -e 'const p = await Bun.file("package.json").json(); console.log(p.version)')"
          {
            echo "name=\${name}"
            echo "version=\${version}"
            echo "tag=v\${version}"
          } >> "$GITHUB_OUTPUT"

      - name: Create tag and GitHub release
        env:
          GH_TOKEN: \${{ secrets.GITHUB_TOKEN }}
          PACKAGE_NAME: \${{ steps.package.outputs.name }}
          PACKAGE_VERSION: \${{ steps.package.outputs.version }}
          RELEASE_TAG: \${{ steps.package.outputs.tag }}
        run: |
          set -euo pipefail
          if git ls-remote --exit-code --tags origin "refs/tags/\${RELEASE_TAG}" >/dev/null 2>&1; then
            echo "Tag \${RELEASE_TAG} already exists."
            exit 0
          fi

          git config user.name "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git tag -a "$RELEASE_TAG" -m "\${PACKAGE_NAME} \${PACKAGE_VERSION}"
          git push origin "$RELEASE_TAG"

          awk -v version="$PACKAGE_VERSION" '
            $0 ~ "^## " version " \\\\(" { found=1; print; next }
            found && /^## / { exit }
            found { print }
          ' CHANGELOG.md > release-notes.md
          if [[ ! -s release-notes.md ]]; then
            printf '%s %s\\n' "$PACKAGE_NAME" "$PACKAGE_VERSION" > release-notes.md
          fi
          gh release create "$RELEASE_TAG" --title "\${PACKAGE_NAME} \${PACKAGE_VERSION}" --notes-file release-notes.md --verify-tag
`;
  return { ci, release };
}
function getPackageVersion() {
  const packagePath = new URL("../package.json", import.meta.url);
  if (!existsSync3(packagePath))
    return "0.1.0";
  const json = JSON.parse(readFileSync5(packagePath, "utf8"));
  return json.version ?? "0.1.0";
}

// src/cli.ts
async function main(argv = process.argv.slice(2)) {
  const [command = "help", ...rest] = argv;
  const flags = parseFlags(rest);
  const cwd = process.cwd();
  switch (command) {
    case "init":
      await initCommand(cwd, flags);
      return;
    case "lint":
      lintCommand(cwd, flags);
      return;
    case "plan":
      await planCommand(cwd, flags);
      return;
    case "release":
      await releaseCommand(cwd, flags);
      return;
    case "doctor":
      await doctorCommand(cwd, flags);
      return;
    case "help":
    case "--help":
    case "-h":
      printHelp();
      return;
    case "version":
    case "--version":
    case "-v":
      printVersion();
      return;
    default:
      throw new HooversionError(`Unknown command: ${command}`);
  }
}
async function initCommand(cwd, flags) {
  const force = flags.booleans.has("force");
  if (findConfigPath(cwd) && !force) {
    throw new HooversionError("Hooversion config already exists. Use --force to overwrite.");
  }
  const packages = detectPackages(cwd);
  const configPath = writeDefaultConfig(cwd, packages);
  const workflowPaths = flags.booleans.has("no-workflow") ? undefined : writeGitHubWorkflows(cwd, {
    actionOwnerRepo: flags.values.get("action-owner-repo"),
    actionRef: flags.values.get("action-ref"),
    hooversionVersion: flags.values.get("hooversion-version")
  });
  console.log(`Wrote ${configPath}`);
  for (const workflowPath of workflowPaths ?? []) {
    console.log(`Wrote ${workflowPath}`);
  }
}
function lintCommand(cwd, flags) {
  const commits = readLintCommits(cwd, flags);
  const issues = commits.flatMap(lintCommit);
  if (issues.length > 0) {
    for (const issue of issues) {
      const hash = issue.hash ? `${issue.hash.slice(0, 7)} ` : "";
      console.error(`${hash}${issue.subject}`);
      console.error(`  ${issue.message}`);
    }
    throw new HooversionError(`Commit lint failed with ${issues.length} issue(s).`);
  }
  console.log(`Validated ${commits.length} commit${commits.length === 1 ? "" : "s"}.`);
}
async function planCommand(cwd, flags) {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  printPlan(plan);
}
async function releaseCommand(cwd, flags) {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  const dryRun = flags.booleans.has("dry-run");
  validatePlan(cwd, config, plan);
  printPlan(plan);
  await executeRelease(cwd, config, plan, {
    dryRun,
    push: flags.booleans.has("no-push") ? false : undefined,
    github: flags.booleans.has("no-github") ? false : undefined
  });
  if (dryRun) {
    console.log("Dry run complete; no files, commits, tags, or releases were created.");
  } else if (plan.releases.length > 0) {
    console.log("Release complete.");
  } else {
    console.log("No release needed.");
  }
}
async function doctorCommand(cwd, flags) {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const result = runDoctor(cwd, config);
  for (const line of result.info)
    console.log(`ok: ${line}`);
  for (const line of result.warnings)
    console.warn(`warning: ${line}`);
  for (const line of result.errors)
    console.error(`error: ${line}`);
  if (result.errors.length > 0)
    throw new HooversionError("Doctor found blocking errors.");
}
function readLintCommits(cwd, flags) {
  const editPath = flags.values.get("edit");
  if (editPath) {
    return [
      {
        hash: "",
        subject: readFileSync6(editPath, "utf8").split(/\r?\n/)[0] ?? "",
        body: readFileSync6(editPath, "utf8").split(/\r?\n/).slice(1).join(`
`),
        files: []
      }
    ];
  }
  if (flags.booleans.has("last")) {
    return [getLastCommit(cwd)];
  }
  const from = flags.values.get("from");
  const to = flags.values.get("to") ?? "HEAD";
  if (!from) {
    throw new HooversionError("lint requires --last, --edit <file>, or --from <ref> [--to <ref>].");
  }
  return getCommits(cwd, from, to);
}
function printPlan(plan) {
  console.log(`Branch: ${plan.branch}`);
  if (plan.unmatchedCommits.length > 0) {
    console.log("Unmatched release commits:");
    for (const commit of plan.unmatchedCommits) {
      console.log(`- ${commit.hash.slice(0, 7)} ${commit.subject}`);
    }
    return;
  }
  if (plan.releases.length === 0) {
    console.log("No release needed.");
    return;
  }
  console.log("Planned releases:");
  for (const release of plan.releases) {
    const source = release.latestTag ? `since ${release.latestTag}` : "from repository history";
    const dependency = release.dependencyTriggered ? " dependency-propagated" : "";
    console.log(`- ${release.package.name}: ${release.currentVersion} -> ${release.nextVersion} (${release.releaseType}${dependency}, ${source}) tag ${release.tag}`);
    for (const commit of release.commits) {
      console.log(`  - ${formatCommit(commit)}`);
    }
  }
}
function formatCommit(commit) {
  const parsed = parseCommit(commit);
  const scope = parsed.scope ? `(${parsed.scope})` : "";
  const bang = parsed.breaking ? "!" : "";
  return `${commit.hash.slice(0, 7)} ${parsed.type}${scope}${bang}: ${parsed.description}`;
}
function parseFlags(args) {
  const values = new Map;
  const booleans = new Set;
  const positionals = [];
  for (let index = 0;index < args.length; index += 1) {
    const arg = args[index];
    if (!arg.startsWith("--")) {
      positionals.push(arg);
      continue;
    }
    const [name, inlineValue] = arg.slice(2).split("=", 2);
    if (inlineValue !== undefined) {
      values.set(name, inlineValue);
      continue;
    }
    const next = args[index + 1];
    if (next && !next.startsWith("--") && expectsValue(name)) {
      values.set(name, next);
      index += 1;
    } else {
      booleans.add(name);
    }
  }
  return { values, booleans, positionals };
}
function expectsValue(name) {
  return ["action-owner-repo", "action-ref", "config", "edit", "from", "hooversion-version", "to"].includes(name);
}
function printHelp() {
  console.log(`hooversion

Usage:
  hooversion init [--force] [--no-workflow] [--action-owner-repo <owner/repo>] [--action-ref <ref>] [--hooversion-version <version>]
  hooversion lint --last
  hooversion lint --from <ref> [--to <ref>]
  hooversion lint --edit <commit-msg-file>
  hooversion plan [--config <path>]
  hooversion release [--dry-run] [--no-push] [--no-github] [--config <path>]
  hooversion doctor [--config <path>]
`);
}
function printVersion() {
  const packagePath = new URL("../package.json", import.meta.url);
  if (existsSync4(packagePath)) {
    const json = JSON.parse(readFileSync6(packagePath, "utf8"));
    console.log(`${json.name ?? "hooversion"} ${json.version ?? "unknown"}`);
  } else {
    console.log(`${basename2(process.argv[1] ?? "hooversion")} unknown`);
  }
}
main().catch((error) => {
  if (error instanceof HooversionError) {
    console.error(error.message);
    process.exit(error.code);
  }
  console.error(error);
  process.exit(1);
});
