#!/usr/bin/env bun
// @bun

// src/errors.ts
class HooversionError extends Error {
  code;
  constructor(message, code = 1) {
    super(message);
    this.code = code;
    this.name = "HooversionError";
  }
}

// src/app-auth.ts
import { createHmac, createSign, timingSafeEqual } from "crypto";
import { readFileSync } from "fs";
var githubApiVersion = "2022-11-28";
function readGitHubAppPrivateKey(env = process.env) {
  const inline = env.VERSIONHOO_PRIVATE_KEY ?? env.HOOVERSION_PRIVATE_KEY;
  if (inline)
    return normalizePrivateKey(inline);
  const path = env.VERSIONHOO_PRIVATE_KEY_PATH ?? env.HOOVERSION_PRIVATE_KEY_PATH;
  if (path)
    return readFileSync(path, "utf8");
  throw new HooversionError("VERSIONHOO_PRIVATE_KEY or VERSIONHOO_PRIVATE_KEY_PATH is required to authenticate as a GitHub App.");
}
function createGitHubAppJwt(config, nowSeconds = Math.floor(Date.now() / 1000)) {
  const header = base64UrlJson({ alg: "RS256", typ: "JWT" });
  const payload = base64UrlJson({
    iat: nowSeconds - 60,
    exp: nowSeconds + 9 * 60,
    iss: String(config.appId)
  });
  const signingInput = `${header}.${payload}`;
  const signature = createSign("RSA-SHA256").update(signingInput).sign(config.privateKey);
  return `${signingInput}.${base64Url(signature)}`;
}
async function createInstallationAccessToken(config, installationId, repository) {
  validateRepositoryFullName(repository.fullName);
  if (!Number.isSafeInteger(installationId) || installationId <= 0) {
    throw new HooversionError("GitHub App installation id must be a positive integer.");
  }
  if (!Number.isSafeInteger(repository.id) || repository.id <= 0) {
    throw new HooversionError("GitHub webhook repository id must be a positive integer.");
  }
  const apiUrl = validateGitHubApiUrl(config.apiUrl ?? "https://api.github.com", config.trustedApiUrls);
  const jwt = createGitHubAppJwt(config);
  const response = await fetch(`${apiUrl}/app/installations/${installationId}/access_tokens`, {
    method: "POST",
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${jwt}`,
      "content-type": "application/json",
      "user-agent": "versionhoo-app",
      "x-github-api-version": githubApiVersion
    },
    body: JSON.stringify({ repository_ids: [repository.id] })
  });
  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub App installation token request failed (${response.status} ${response.statusText}): ${body}`);
  }
  const data = await response.json();
  return { token: data.token, expiresAt: data.expires_at };
}
function verifyGitHubWebhookSignature(secret, body, signatureHeader) {
  if (!signatureHeader?.startsWith("sha256="))
    return false;
  const expected = Buffer.from(`sha256=${createHmac("sha256", secret).update(body).digest("hex")}`, "utf8");
  const actual = Buffer.from(signatureHeader, "utf8");
  return actual.length === expected.length && timingSafeEqual(actual, expected);
}
function validateGitHubApiUrl(apiUrl, trustedApiUrls = []) {
  const parsed = parseHttpsUrl(apiUrl, "GitHub API");
  const normalized = normalizeOrigin(parsed);
  const trusted = trustedApiUrls.map((value) => normalizeOrigin(parseHttpsUrl(value, "trusted GitHub API")));
  if (parsed.hostname !== "api.github.com" || parsed.pathname !== "/") {
    if (!trusted.includes(normalized)) {
      throw new HooversionError(`Untrusted GitHub API URL: ${apiUrl}`);
    }
  }
  return normalized.endsWith("/") ? normalized.slice(0, -1) : normalized;
}
function validateRepositoryFullName(repository) {
  const parts = repository.split("/");
  if (parts.length !== 2 || parts.some((part) => !/^[A-Za-z0-9][A-Za-z0-9_.-]*$/.test(part)) || parts.some((part) => part === "." || part === "..")) {
    throw new HooversionError(`Invalid GitHub repository identity: ${repository}`);
  }
  return `${parts[0]}/${parts[1]}`;
}
function parseHttpsUrl(value, label) {
  let parsed;
  try {
    parsed = new URL(value);
  } catch {
    throw new HooversionError(`Invalid ${label} URL: ${value}`);
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.port || parsed.search || parsed.hash) {
    throw new HooversionError(`Invalid ${label} URL: ${value}`);
  }
  return parsed;
}
function normalizeOrigin(value) {
  return `${value.protocol}//${value.host}${value.pathname.replace(/\/+$/, "") || "/"}`;
}
function normalizePrivateKey(value) {
  return value.includes("\\n") ? value.replaceAll("\\n", `
`) : value;
}
function base64UrlJson(value) {
  return base64Url(Buffer.from(JSON.stringify(value), "utf8"));
}
function base64Url(value) {
  return value.toString("base64").replaceAll("+", "-").replaceAll("/", "_").replace(/=+$/, "");
}

// src/app-runner.ts
import { chmodSync, existsSync as existsSync2, mkdtempSync, mkdirSync as mkdirSync4, rmSync, writeFileSync as writeFileSync4 } from "fs";
import { tmpdir } from "os";
import { join as join6 } from "path";

// src/config.ts
import { existsSync, readFileSync as readFileSync3, writeFileSync as writeFileSync2 } from "fs";
import { basename, join as join2, normalize, relative as relative2 } from "path";
import { pathToFileURL } from "url";

// src/manifest.ts
import {
  closeSync,
  constants as fsConstants,
  fstatSync,
  fsyncSync,
  ftruncateSync,
  openSync,
  readFileSync as readFileSync2,
  writeSync,
  writeFileSync
} from "fs";
import { dirname, join } from "path";
function defaultManifestPath(type, packagePath) {
  if (type === "node")
    return join(packagePath, "package.json");
  if (type === "rust")
    return join(packagePath, "Cargo.toml");
  if (type === "version-file")
    return join(packagePath, "version");
  return join(packagePath, "pyproject.toml");
}
function readManifest(cwd, pkg) {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node")
    return readPackageJson(path);
  if (pkg.type === "rust")
    return readTomlPackage(path, "package");
  if (pkg.type === "version-file")
    return readVersionFile(path, pkg.name);
  return readTomlPackage(path, "project");
}
function updateManifestVersion(cwd, pkg, version) {
  const path = join(cwd, pkg.manifest);
  if (pkg.type === "node") {
    const json = JSON.parse(readFileSync2(path, "utf8"));
    json.version = version;
    writeFileSync(path, `${JSON.stringify(json, null, 2)}
`);
    return;
  }
  if (pkg.type === "version-file") {
    writeFileSync(path, `${version}
`);
    return;
  }
  const section = pkg.type === "rust" ? "package" : "project";
  updateTomlSectionVersion(path, section, version);
}
function updateLocalDependencyVersions(cwd, packages, releasedVersions) {
  const packageNames = new Map(packages.map((pkg) => [normalizePackageName(pkg.name), pkg.name]));
  const rustReleasedVersions = new Map(Array.from(releasedVersions).filter(([name]) => packages.some((pkg) => pkg.type === "rust" && normalizePackageName(pkg.name) === normalizePackageName(name))));
  for (const pkg of packages) {
    const localVersions = new Map;
    for (const dependency of pkg.dependencies) {
      const actualName = packageNames.get(normalizePackageName(dependency));
      const version = actualName ? releasedVersions.get(actualName) ?? releasedVersions.get(dependency) : undefined;
      if (actualName && version)
        localVersions.set(actualName, version);
    }
    if (localVersions.size === 0)
      continue;
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
  if (rustReleasedVersions.size > 0) {
    try {
      updateRustWorkspaceDependencies(workspaceManifest, rustReleasedVersions);
    } catch (error) {
      if (error.code !== "ENOENT")
        throw error;
    }
  }
  if (rustReleasedVersions.size > 0)
    updateCargoLock(cwd, rustReleasedVersions);
}
function isRecord(value) {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}
function normalizePackageName(name) {
  return name.trim().toLowerCase();
}
function findReleasedName(name, releasedVersions) {
  const normalized = normalizePackageName(name);
  return Array.from(releasedVersions.keys()).find((candidate) => normalizePackageName(candidate) === normalized);
}
function assertAllDependenciesFound(path, owner, releasedVersions, found) {
  for (const target of releasedVersions.keys()) {
    if (!found.has(target)) {
      throw new HooversionError(`${path} package ${owner.name} declares local dependency ${target}, but it was not found`);
    }
  }
}
function rewriteNodeRequirement(current, version, path, name) {
  if (current.startsWith("workspace:"))
    return current;
  if (/^(?:file|git|https?):/.test(current)) {
    throw new HooversionError(`${path} dependency ${name} has unsupported specifier ${current}`);
  }
  const prefix = current.match(/^[~^]/)?.[0] ?? "";
  return `${prefix}${version}`;
}
function isPythonDependencySection(section) {
  return section === "project" || section === "project.optional-dependencies" || section.startsWith("project.optional-dependencies.") || section === "tool.poetry.dependencies" || section.startsWith("tool.poetry.group.") && section.endsWith(".dependencies");
}
function rewritePythonRequirementsLine(line, releasedVersions, found, path) {
  let changed = false;
  const output = line.replace(/(["'])([^"']*)\1/g, (full, quote, requirement) => {
    const nameMatch = /^\s*([A-Za-z0-9][A-Za-z0-9._-]*)/.exec(requirement);
    if (!nameMatch)
      return full;
    const target = findReleasedName(nameMatch[1], releasedVersions);
    if (!target)
      return full;
    found.add(target);
    const next = rewritePythonRequirement(requirement, releasedVersions.get(target), path, nameMatch[1]);
    if (next === requirement)
      return full;
    changed = true;
    return `${quote}${next}${quote}`;
  });
  return { line: output, changed };
}
function rewritePythonRequirement(requirement, version, path, name) {
  const suffix = requirement.slice(name.length);
  if (suffix.trimStart().startsWith("@")) {
    throw new HooversionError(`${path} dependency ${name} has unsupported direct URL syntax`);
  }
  return `${name}${rewritePythonConstraint(suffix, version, path, name)}`;
}
function rewritePythonConstraint(current, version, path, name) {
  if (current.includes("@")) {
    throw new HooversionError(`${path} dependency ${name} has unsupported direct URL syntax`);
  }
  const match = /([<>=!~]{1,3})\s*([0-9][^,\s;]*)/.exec(current);
  if (match)
    return current.replace(match[2], version);
  const marker = current.search(/\s*;/);
  if (marker >= 0)
    return `${current.slice(0, marker)}==${version}${current.slice(marker)}`;
  return `${current}==${version}`;
}
function isRustDependencySection(section, workspaceOnly) {
  if (workspaceOnly)
    return section === "workspace.dependencies";
  return ["dependencies", "dev-dependencies", "build-dependencies"].includes(section) || /^target\..+\.(dependencies|dev-dependencies|build-dependencies)$/.test(section);
}
function findRustDottedDependency(section, releasedVersions, workspaceOnly) {
  const match = (workspaceOnly ? /^workspace\.dependencies\.((?:"[^"]+"|[A-Za-z0-9_-]+))$/ : /^(?:(?:dependencies|dev-dependencies|build-dependencies)|(?:target\..+\.(?:dependencies|dev-dependencies|build-dependencies)))\.((?:"[^"]+"|[A-Za-z0-9_-]+))$/).exec(section);
  if (!match)
    return;
  const name = match[1].replace(/^"|"$/g, "");
  return findReleasedName(name, releasedVersions);
}
function finishRustDottedDependency(path, dependency, found) {
  if (!dependency.workspace && !dependency.versionUpdated) {
    throw new HooversionError(`${path} dependency ${dependency.target} has no supported version field`);
  }
  found.add(dependency.target);
}
function braceDelta(value) {
  return (value.match(/\{/g)?.length ?? 0) - (value.match(/\}/g)?.length ?? 0);
}
function readPackageJson(path) {
  const json = JSON.parse(readFileSync2(path, "utf8"));
  if (!json.name || !json.version) {
    throw new HooversionError(`${path} must contain name and version`);
  }
  return { name: json.name, version: json.version };
}
function readTomlPackage(path, sectionName) {
  const text = readFileSync2(path, "utf8");
  const section = getTomlSection(text, sectionName);
  const name = readTomlString(section, "name");
  const version = readTomlString(section, "version");
  if (!name || !version) {
    throw new HooversionError(`${path} [${sectionName}] must contain name and version`);
  }
  return { name, version };
}
function readVersionFile(path, name) {
  const version = readFileSync2(path, "utf8").trim();
  if (!version) {
    throw new HooversionError(`${path} must contain a version`);
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
  const text = readFileSync2(path, "utf8");
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
function updateNodeLocalDependencies(path, owner, releasedVersions) {
  const json = JSON.parse(readFileSync2(path, "utf8"));
  const sections = ["dependencies", "devDependencies", "peerDependencies", "optionalDependencies"];
  const found = new Set;
  let changed = false;
  for (const section of sections) {
    const value = json[section];
    if (value === undefined)
      continue;
    if (!isRecord(value))
      throw new HooversionError(`${path} ${section} must be an object`);
    for (const [name, current] of Object.entries(value)) {
      const target = findReleasedName(name, releasedVersions);
      if (!target)
        continue;
      found.add(target);
      if (typeof current !== "string") {
        throw new HooversionError(`${path} package ${owner.name} has unsupported dependency ${name}`);
      }
      const next = rewriteNodeRequirement(current, releasedVersions.get(target), path, name);
      if (next !== current) {
        value[name] = next;
        changed = true;
      }
    }
  }
  assertAllDependenciesFound(path, owner, releasedVersions, found);
  if (changed)
    writeFileSync(path, `${JSON.stringify(json, null, 2)}
`);
}
function updatePythonLocalDependencies(path, owner, releasedVersions) {
  const lines = readFileSync2(path, "utf8").split(/\r?\n/);
  const found = new Set;
  let changed = false;
  let section = "";
  let inArray = false;
  for (let index = 0;index < lines.length; index += 1) {
    const heading = /^\s*\[([^\]]+)\]\s*$/.exec(lines[index]);
    if (heading) {
      section = heading[1];
      inArray = false;
      continue;
    }
    const relevant = isPythonDependencySection(section);
    if (!relevant)
      continue;
    if (inArray) {
      const result = rewritePythonRequirementsLine(lines[index], releasedVersions, found, path);
      lines[index] = result.line;
      changed ||= result.changed;
      if (/\]/.test(lines[index]))
        inArray = false;
      continue;
    }
    const assignment = /^\s*([A-Za-z0-9_.-]+)\s*=\s*(.*)$/.exec(lines[index]);
    if (!assignment)
      continue;
    const key = assignment[1];
    const value = assignment[2];
    if (value.startsWith("[") && (key === "dependencies" || section === "project.optional-dependencies" || section.startsWith("project.optional-dependencies."))) {
      const result = rewritePythonRequirementsLine(lines[index], releasedVersions, found, path);
      lines[index] = result.line;
      changed ||= result.changed;
      if (!/\]/.test(value))
        inArray = true;
      continue;
    }
    if (section.startsWith("tool.poetry") && findReleasedName(key, releasedVersions)) {
      if (!/^["']/.test(value)) {
        throw new HooversionError(`${path} package ${owner.name} has unsupported dependency ${key}`);
      }
      const quote = value[0];
      const end = value.indexOf(quote, 1);
      if (end < 0)
        throw new HooversionError(`${path} has malformed dependency ${key}`);
      const target = findReleasedName(key, releasedVersions);
      const next = rewritePythonConstraint(value.slice(1, end), releasedVersions.get(target), path, key);
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
  if (changed)
    writeFileSync(path, lines.join(`
`));
}
function updateRustLocalDependencies(path, owner, releasedVersions) {
  updateRustDependencyTables(path, owner, releasedVersions, false);
}
function updateRustWorkspaceDependencies(path, releasedVersions) {
  updateRustDependencyTables(path, undefined, releasedVersions, true);
}
function updateRustDependencyTables(path, owner, releasedVersions, workspaceOnly) {
  const lines = readFileSync2(path, "utf8").split(/\r?\n/);
  const found = new Set;
  let section = "";
  let active;
  let dotted;
  let changed = false;
  for (let index = 0;index < lines.length; index += 1) {
    const line = lines[index];
    if (active) {
      if (/workspace\s*=\s*true/.test(line))
        active.workspace = true;
      if (!active.workspace && /^\s*version\s*=/.test(line)) {
        lines[index] = line.replace(/=\s*["'][^"']+["']/, `= "${releasedVersions.get(active.target)}"`);
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
      if (dotted)
        finishRustDottedDependency(path, dotted, found);
      section = heading[1];
      const target2 = findRustDottedDependency(section, releasedVersions, workspaceOnly);
      dotted = target2 ? { target: target2, workspace: false, versionUpdated: false } : undefined;
      continue;
    }
    if (dotted) {
      if (/workspace\s*=\s*true/.test(line))
        dotted.workspace = true;
      if (!dotted.workspace && /^\s*version\s*=/.test(line)) {
        lines[index] = line.replace(/=\s*["'][^"']+["']/, `= "${releasedVersions.get(dotted.target)}"`);
        dotted.versionUpdated = true;
        changed = true;
      }
      continue;
    }
    if (!isRustDependencySection(section, workspaceOnly))
      continue;
    const entry = /^\s*(?:"([^"]+)"|([A-Za-z0-9_-]+))\s*=\s*(.*)$/.exec(line);
    if (!entry)
      continue;
    const name = entry[1] ?? entry[2];
    const target = findReleasedName(name, releasedVersions);
    if (!target)
      continue;
    const value = entry[3].trim();
    if (value.startsWith("{")) {
      const workspace = /workspace\s*=\s*true/.test(value);
      const versionMatch = /version\s*=\s*["'][^"']+["']/.test(value);
      const depth = braceDelta(value);
      if (depth > 0) {
        active = { target, depth, workspace, versionUpdated: false };
      } else if (!workspace && versionMatch) {
        lines[index] = line.replace(/(version\s*=\s*)["'][^"']+["']/, `$1"${releasedVersions.get(target)}"`);
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
      if (end < 0)
        throw new HooversionError(`${path} has malformed dependency ${name}`);
      lines[index] = `${line.slice(0, line.indexOf(value) + 1)}${releasedVersions.get(target)}${value.slice(end)}`;
      found.add(target);
      changed = true;
      continue;
    }
    throw new HooversionError(`${path} dependency ${name} has unsupported value`);
  }
  if (dotted)
    finishRustDottedDependency(path, dotted, found);
  if (!workspaceOnly && owner)
    assertAllDependenciesFound(path, owner, releasedVersions, found);
  if (changed)
    writeFileSync(path, lines.join(`
`));
}
function updateCargoLock(cwd, releasedVersions) {
  const path = join(cwd, "Cargo.lock");
  let fd;
  try {
    fd = openSync(path, fsConstants.O_RDWR | fsConstants.O_NOFOLLOW | fsConstants.O_NONBLOCK);
    if (!fstatSync(fd).isFile())
      throw new HooversionError(`${path} must be a regular file`);
    const lines = readFileSync2(fd, "utf8").split(/\r?\n/);
    const starts = lines.flatMap((line, index) => line.trim() === "[[package]]" ? [index] : []);
    let changed = false;
    for (let block = 0;block < starts.length; block += 1) {
      const start = starts[block];
      const end = starts[block + 1] ?? lines.length;
      const nameLine = lines.slice(start, end).find((line) => /^\s*name\s*=/.test(line));
      const name = nameLine ? readTomlString(nameLine, "name") : undefined;
      const target = name ? findReleasedName(name, releasedVersions) : undefined;
      const hasSource = lines.slice(start, end).some((line) => /^\s*source\s*=/.test(line));
      if (target && !hasSource) {
        const versionIndex = lines.findIndex((line, index) => index >= start && index < end && /^\s*version\s*=/.test(line));
        if (versionIndex < 0)
          throw new HooversionError(`${path} package ${name} has no version field`);
        const version = releasedVersions.get(target);
        const updatedVersion = lines[versionIndex].replace(/=\s*["'][^"']+["']/, `= "${version}"`);
        if (updatedVersion !== lines[versionIndex]) {
          lines[versionIndex] = updatedVersion;
          changed = true;
        }
      }
      let inDependencies = false;
      for (let index = start;index < end; index += 1) {
        if (/^\s*dependencies\s*=\s*\[/.test(lines[index])) {
          inDependencies = true;
          if (/\]/.test(lines[index]))
            inDependencies = false;
          continue;
        }
        if (!inDependencies || hasSource)
          continue;
        lines[index] = lines[index].replace(/(["'])([^"']+) \d[^"']*\1/g, (full, quote, dependencyName) => {
          const dependencyTarget = findReleasedName(dependencyName, releasedVersions);
          if (!dependencyTarget || /\s\(/.test(full))
            return full;
          changed = true;
          return `${quote}${dependencyName} ${releasedVersions.get(dependencyTarget)}${quote}`;
        });
        if (/\]/.test(lines[index]))
          inDependencies = false;
      }
    }
    if (changed) {
      ftruncateSync(fd, 0);
      writeFileDescriptor(fd, lines.join(`
`));
      fsyncSync(fd);
    }
  } catch (error) {
    if (error.code === "ENOENT")
      return;
    throw error;
  } finally {
    if (fd !== undefined)
      closeSync(fd);
  }
}
function writeFileDescriptor(fd, content) {
  const data = Buffer.from(content);
  let offset = 0;
  while (offset < data.byteLength) {
    const written = writeSync(fd, data, offset, data.byteLength - offset, offset);
    if (written <= 0)
      throw new HooversionError("Failed to write Cargo.lock");
    offset += written;
  }
}
function escapeRegExp(value) {
  return value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}

// src/git.ts
import { relative, resolve, sep } from "path";

// src/process.ts
import { spawnSync } from "child_process";
function runCommand(command, args, cwd, env) {
  const result = spawnSync(command, args, {
    cwd,
    encoding: "utf8",
    env: env ?? process.env
  });
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? ""
  };
}
function runShell(command, cwd, env) {
  const result = spawnSync(command, {
    cwd,
    encoding: "utf8",
    env: env ?? process.env,
    shell: (env ?? process.env).SHELL ?? "/bin/sh"
  });
  return {
    code: result.status ?? 1,
    stdout: result.stdout ?? "",
    stderr: result.stderr ?? ""
  };
}

// src/git.ts
function assertValidGitRef(value, kind) {
  if (typeof value !== "string" || value.length === 0 || value === "@" || value.startsWith("-") || value.startsWith("refs/") || value.startsWith("/") || value.endsWith("/") || value.includes("//") || value.includes("..") || value.includes("@{") || /[\u0000-\u0020\u007f~^:?*\\]/u.test(value) || value.includes("[")) {
    throw new HooversionError(`Invalid Git ${kind} name: ${JSON.stringify(value)}`);
  }
  const components = value.split("/");
  if (components.some((component) => component.length === 0 || component === "." || component === ".." || component.startsWith(".") || component.endsWith(".") || component.toLowerCase().endsWith(".lock"))) {
    throw new HooversionError(`Invalid Git ${kind} name: ${JSON.stringify(value)}`);
  }
}
function commandEnv(auth) {
  return auth ? { ...process.env, ...auth } : undefined;
}
function git(cwd, args, allowFailure = false, auth) {
  const result = runCommand("git", args, cwd, commandEnv(auth));
  if (result.code !== 0 && !allowFailure) {
    throw new HooversionError(`git ${args.join(" ")} failed:
${result.stderr || result.stdout}`);
  }
  return result.stdout.trimEnd();
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
function ensureCleanWorkingTree(cwd, ignoredPaths = [], scopedOutputDir) {
  const ignored = new Set(ignoredPaths.map((path) => resolve(cwd, path)));
  const unexpected = git(cwd, ["status", "--porcelain", "--untracked-files=all"]).split(`
`).filter((line) => {
    if (!line.trim())
      return false;
    const path = line.slice(3).trim();
    return !ignored.has(resolve(cwd, path));
  });
  if (scopedOutputDir) {
    const resolvedOutputDir = resolve(cwd, scopedOutputDir);
    const relativeOutputDir = relative(cwd, resolvedOutputDir);
    if (relativeOutputDir && !relativeOutputDir.startsWith(`..${sep}`) && relativeOutputDir !== "..") {
      const ignoredOutputFiles = git(cwd, ["ls-files", "--others", "--ignored", "--exclude-standard", "--", relativeOutputDir], true).split(`
`).filter((path) => path.trim() && !ignored.has(resolve(cwd, path.trim())));
      unexpected.push(...ignoredOutputFiles.map((path) => `?? ${path}`));
    }
  }
  if (unexpected.length > 0) {
    throw new HooversionError(`Working tree must be clean before release:
${unexpected.join(`
`)}`);
  }
}
function getLatestTag(cwd, pattern) {
  const output = git(cwd, ["describe", "--tags", "--abbrev=0", "--match", pattern], true).trim();
  return output || undefined;
}
function tagExists(cwd, tag) {
  assertValidGitRef(tag, "tag");
  return runCommand("git", ["rev-parse", "--verify", "--quiet", "--", `refs/tags/${tag}`], cwd).code === 0;
}
function getHeadSha(cwd) {
  return git(cwd, ["rev-parse", "HEAD"]).trim();
}
function getRefSha(cwd, ref) {
  let commitRef;
  if (typeof ref !== "string") {
    throw new HooversionError(`Invalid Git revision: ${JSON.stringify(ref)}`);
  }
  if (ref === "HEAD" || ref === "HEAD^" || /^[0-9a-fA-F]{40}$/u.test(ref)) {
    commitRef = ref;
  } else if (ref.startsWith("refs/tags/")) {
    const tag = ref.slice("refs/tags/".length);
    assertValidGitRef(tag, "tag");
    commitRef = `${ref}^{commit}`;
  } else if (ref.startsWith("refs/heads/")) {
    assertValidGitRef(ref.slice("refs/heads/".length), "branch");
    commitRef = ref;
  } else {
    throw new HooversionError(`Invalid Git revision: ${JSON.stringify(ref)}`);
  }
  const result = runCommand("git", ["rev-parse", "--verify", "--quiet", "--end-of-options", commitRef], cwd);
  return result.code === 0 ? result.stdout.trim() : undefined;
}
function getRemoteBranchSha(cwd, branch, auth) {
  assertValidGitRef(branch, "branch");
  const remote = git(cwd, ["config", "--get", "remote.origin.url"], true).trim();
  if (!remote)
    return;
  const output = git(cwd, ["ls-remote", "--", "origin", `refs/heads/${branch}`], true, auth).trim();
  return output ? output.split(/\s+/, 1)[0] : "";
}
function getCommitMessage(cwd, ref = "HEAD") {
  return git(cwd, ["show", "-s", "--format=%B", ref]).trimEnd();
}
function pushRelease(cwd, branch, tags, auth) {
  assertValidGitRef(branch, "branch");
  for (const tag of tags)
    assertValidGitRef(tag, "tag");
  git(cwd, [
    "push",
    "--atomic",
    "--no-verify",
    "--",
    "origin",
    `HEAD:refs/heads/${branch}`,
    ...tags.map((tag) => `refs/tags/${tag}`)
  ], false, auth);
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
function createReleaseCommit(cwd, message) {
  git(cwd, ["add", "--all"]);
  const status = git(cwd, ["status", "--porcelain"]);
  if (!status.trim())
    return;
  git(cwd, ["commit", "-m", message]);
}
function createAnnotatedTag(cwd, tag, message) {
  assertValidGitRef(tag, "tag");
  git(cwd, ["tag", "-a", tag, "-m", message]);
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

// src/config.ts
function assertValidTagFormat(format, packages) {
  for (const pkg of packages) {
    const candidate = format.replaceAll("${name}", pkg.name).replaceAll("${version}", "0.0.0");
    assertValidGitRef(candidate, "tag");
  }
}
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
    raw = JSON.parse(readFileSync3(configPath, "utf8"));
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
  const packageNames = new Map;
  for (const pkg of packages) {
    const normalizedName = normalizeGraphName(pkg.name);
    const duplicate = packageNames.get(normalizedName);
    if (duplicate) {
      throw new HooversionError(`Duplicate package name after normalization: ${duplicate.name} and ${pkg.name}`);
    }
    packageNames.set(normalizedName, pkg);
  }
  const graph = new Map;
  for (const pkg of packages) {
    const dependencies = [];
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
  const branches = raw.branches ?? ["main"];
  const tagFormat = raw.tagFormat ?? "v${version}";
  const independentTagFormat = raw.independentTagFormat ?? "${name}@v${version}";
  for (const branch of branches)
    assertValidGitRef(branch, "branch");
  assertValidTagFormat(tagFormat, packages);
  assertValidTagFormat(independentTagFormat, packages);
  return {
    ...raw,
    branches,
    tagFormat,
    independentTagFormat,
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
  const name = (pkg.name || info.name).trim();
  return {
    ...pkg,
    name,
    path: packagePath,
    type: pkg.type,
    manifest,
    changelog: normalizeRelative(pkg.changelog ?? defaultChangelog(packagePath)),
    scopes: [...new Set([name, ...pkg.scopes ?? []])],
    dependencies: (pkg.dependencies ?? []).map((dependency) => dependency.trim()),
    assets: pkg.assets ?? []
  };
}
function normalizeGraphName(name) {
  return name.trim().toLowerCase();
}
function assertAcyclicPackageGraph(packages, graph) {
  const state = new Map;
  const stack = [];
  const visit = (name) => {
    if (state.get(name) === "visited")
      return;
    if (state.get(name) === "visiting") {
      const cycleStart = stack.indexOf(name);
      throw new HooversionError(`Package dependency cycle detected: ${[...stack.slice(cycleStart), name].join(" -> ")}`);
    }
    state.set(name, "visiting");
    stack.push(name);
    for (const dependency of graph.get(name) ?? [])
      visit(dependency);
    stack.pop();
    state.set(name, "visited");
  };
  for (const pkg of packages)
    visit(normalizeGraphName(pkg.name));
}
function defaultChangelog(packagePath) {
  return packagePath === "." ? "CHANGELOG.md" : join2(packagePath, "CHANGELOG.md");
}
function normalizeRelative(path) {
  const normalized = normalize(path);
  if (normalized.startsWith("..")) {
    throw new HooversionError(`Path must stay inside the repository: ${path}`);
  }
  return normalized === "" ? "." : normalized;
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

// src/changelog.ts
import {
  closeSync as closeSync2,
  constants as fsConstants2,
  fstatSync as fstatSync2,
  fsyncSync as fsyncSync2,
  mkdirSync,
  openSync as openSync2,
  readFileSync as readFileSync4,
  renameSync,
  unlinkSync,
  writeSync as writeSync2
} from "fs";
import { dirname as dirname2, join as join3 } from "path";

// src/commit.ts
var conventionalHeaderPattern = /^([a-z][a-z0-9-]*)(?:\(([^()\r\n]+)\))?(!)?: (.+)$/;
var breakingFooterLinePattern = /^BREAKING[ -]CHANGE:\s*\S.*$/;
var ignoredSubjectPatterns = [
  /^Merge /,
  /^Revert "/,
  /^revert: /i,
  /^chore\(release\)!?: /
];
var defaultReleaseRules = {
  feat: "minor",
  fix: "patch",
  perf: "patch"
};
function isIgnoredSubject(subject) {
  return ignoredSubjectPatterns.some((pattern) => pattern.test(subject));
}
function parseCommit(raw, policy = {}) {
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
  const releaseRules = { ...defaultReleaseRules, ...policy.releaseTypes };
  const breaking = Boolean(breakingChangeDescription(raw.body)) || Boolean(bang);
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
function parseCommits(rawCommits, policy = {}) {
  return rawCommits.map((raw) => parseCommit(raw, policy));
}
function breakingChangeDescription(body) {
  const lines = body.split(/\r?\n/);
  for (let index = 0;index < lines.length; index += 1) {
    const match = breakingFooterLinePattern.exec(lines[index].trim());
    if (!match)
      continue;
    if (index === 0 && lines.length === 1 || lines[index - 1]?.trim() === "") {
      return lines[index].trim().replace(/^BREAKING[ -]CHANGE:\s*/, "").trim();
    }
  }
  return;
}

// src/changelog.ts
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
        const breaking = breakingChangeDescription(commit.body);
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
  let existing = "";
  let sourceFd;
  try {
    sourceFd = openSync2(path, fsConstants2.O_RDONLY | fsConstants2.O_NOFOLLOW | fsConstants2.O_NONBLOCK);
    if (!fstatSync2(sourceFd).isFile())
      throw new HooversionError(`${path} must be a regular file`);
    existing = readFileSync4(sourceFd, "utf8");
  } catch (error) {
    if (error.code !== "ENOENT")
      throw error;
  } finally {
    if (sourceFd !== undefined)
      closeSync2(sourceFd);
  }
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
  const tempPath = `${path}.hooversion-${process.pid}-${Math.random().toString(16).slice(2)}.tmp`;
  let tempFd;
  let tempOwned = false;
  try {
    tempFd = openSync2(tempPath, fsConstants2.O_WRONLY | fsConstants2.O_CREAT | fsConstants2.O_EXCL | fsConstants2.O_NOFOLLOW, 384);
    tempOwned = true;
    writeChangelogFile(tempFd, next);
    fsyncSync2(tempFd);
    closeSync2(tempFd);
    tempFd = undefined;
    renameSync(tempPath, path);
    tempOwned = false;
  } finally {
    if (tempFd !== undefined) {
      try {
        closeSync2(tempFd);
      } catch {}
    }
    if (tempOwned) {
      try {
        unlinkSync(tempPath);
      } catch (error) {
        if (error.code !== "ENOENT")
          throw error;
      }
    }
  }
}
function writeChangelogFile(fd, content) {
  const data = Buffer.from(content);
  let offset = 0;
  while (offset < data.byteLength) {
    const written = writeSync2(fd, data, offset, data.byteLength - offset, offset);
    if (written <= 0)
      throw new HooversionError("Failed to write changelog");
    offset += written;
  }
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
  const orderedTitles = [...Object.values(groupTitles), "Other Changes"];
  return orderedTitles.map((title) => [title, buckets.get(title) ?? []]).filter(([, values]) => values.length > 0);
}
function pushBucket(map, key, commit) {
  const bucket = map.get(key) ?? [];
  bucket.push(commit);
  map.set(key, bucket);
}

// src/routing.ts
import { relative as relative3 } from "path";
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
  const rel = relative3(packagePath, file);
  return rel === "" || !rel.startsWith("..") && rel !== ".";
}

// src/plan.ts
function renderTag(format, pkg, version) {
  return format.replaceAll("${name}", pkg.name).replaceAll("${version}", version);
}
function tagPatternForPackage(config, pkg) {
  const format = config.packages.length === 1 ? config.tagFormat : config.independentTagFormat;
  return format.replaceAll("${name}", pkg.name).replaceAll("${version}", "[0-9]*");
}
function tagForPackage(config, pkg, version) {
  return renderTag(config.packages.length === 1 ? config.tagFormat : config.independentTagFormat, pkg, version);
}
function createReleasePlan(cwd, config) {
  const branch = getCurrentBranch(cwd);
  const sourceSha = git(cwd, ["rev-parse", "HEAD"]).trim();
  const independent = config.packages.length > 1;
  return independent ? createIndependentPlan(cwd, config, branch, sourceSha) : createSinglePackagePlan(cwd, config, branch, sourceSha);
}
function createSinglePackagePlan(cwd, config, branch, sourceSha) {
  const pkg = config.packages[0];
  const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
  const commits = parseCommits(getCommits(cwd, latestTag, sourceSha)).filter((commit) => !commit.ignored);
  const releaseType = highestReleaseType(commits.map((commit) => commit.releaseType));
  const releases = releaseType ? [buildRelease(cwd, config, pkg, commits, releaseType, latestTag, false)] : [];
  return { cwd, branch, sourceSha, independent: false, releases, unmatchedCommits: [] };
}
function createIndependentPlan(cwd, config, branch, sourceSha) {
  const latestTags = new Map;
  const candidateCommits = new Map;
  for (const pkg of config.packages) {
    const latestTag = getLatestTag(cwd, tagPatternForPackage(config, pkg));
    latestTags.set(pkg.name, latestTag);
    const commits = parseCommits(getCommits(cwd, latestTag, sourceSha)).filter((commit) => !commit.ignored);
    for (const commit of commits) {
      candidateCommits.set(commit.hash, commit);
    }
  }
  const directAffectedByCommit = new Map;
  const releaseTypes = new Map;
  const releaseCommits = new Map;
  for (const commit of candidateCommits.values()) {
    const direct = directAffectedPackages(commit, config.packages);
    directAffectedByCommit.set(commit.hash, direct);
    if (!commit.releaseType)
      continue;
    for (const packageName of direct) {
      releaseTypes.set(packageName, highestReleaseType([releaseTypes.get(packageName), commit.releaseType]));
      const commits = releaseCommits.get(packageName) ?? [];
      commits.push(commit);
      releaseCommits.set(packageName, commits);
    }
  }
  const unmatchedCommits = Array.from(candidateCommits.values()).filter((commit) => commit.releaseType && (directAffectedByCommit.get(commit.hash)?.size ?? 0) === 0);
  if (unmatchedCommits.length > 0) {
    return { cwd, branch, sourceSha, independent: true, releases: [], unmatchedCommits };
  }
  const dependencyTriggered = new Set;
  let changed = true;
  while (changed) {
    changed = false;
    for (const pkg of config.packages) {
      if (releaseTypes.has(pkg.name))
        continue;
      if (!pkg.dependencies.some((dependency) => releaseTypes.has(dependency)))
        continue;
      releaseTypes.set(pkg.name, "patch");
      dependencyTriggered.add(pkg.name);
      changed = true;
    }
  }
  const releases = [];
  for (const pkg of config.packages) {
    const releaseType = releaseTypes.get(pkg.name);
    if (!releaseType)
      continue;
    releases.push(buildRelease(cwd, config, pkg, uniqueCommits(releaseCommits.get(pkg.name) ?? []), releaseType, latestTags.get(pkg.name), dependencyTriggered.has(pkg.name)));
  }
  return { cwd, branch, sourceSha, independent: true, releases, unmatchedCommits: [] };
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
import { join as join5 } from "path";

// src/github.ts
import { constants as fsConstants3 } from "fs";
import * as fs from "fs/promises";
import { basename as basename2, isAbsolute, relative as relative4, resolve as resolve2, sep as sep2, win32 } from "path";
async function publishGitHubRelease(cwd, config, release, options = {}) {
  if (config.github === false || !config.github.releases)
    return;
  const token = options.token || process.env.GITHUB_TOKEN || process.env.GH_TOKEN;
  if (!token) {
    throw new HooversionError("GITHUB_TOKEN or GH_TOKEN is required to create GitHub releases.");
  }
  const repository = config.github.repository || getOriginRepository(cwd);
  if (!repository) {
    throw new HooversionError("Could not determine GitHub repository. Set github.repository in hooversion config.");
  }
  const apiUrl = config.github.apiUrl.replace(/\/$/, "");
  const releaseName = `${release.package.name} ${release.nextVersion}`;
  const existing = await githubFetch(`${apiUrl}/repos/${repository}/releases/tags/${encodeURIComponent(release.tag)}`, token, { method: "GET" }, true);
  let response;
  let existingAssetNames = new Set;
  if (existing) {
    const matches = existing.tag_name === release.tag && existing.name === releaseName && existing.body === release.notes && existing.draft === false && existing.prerelease === false;
    if (!matches) {
      throw new HooversionError(`GitHub release already exists for tag ${release.tag} with different metadata.`);
    }
    response = existing;
    existingAssetNames = releaseAssetNames(existing.assets);
  } else {
    response = await githubFetch(`${apiUrl}/repos/${repository}/releases`, token, {
      method: "POST",
      body: JSON.stringify({
        tag_name: release.tag,
        name: releaseName,
        body: release.notes,
        draft: false,
        prerelease: false
      }),
      headers: {
        "content-type": "application/json"
      }
    });
  }
  await uploadMissingAssets(response.upload_url, apiUrl, token, cwd, release.package.assets, existingAssetNames);
  return response.html_url;
}
async function uploadMissingAssets(uploadUrlTemplate, apiUrl, token, cwd, assets, existingAssetNames) {
  const missingAssets = new Map;
  for (const asset of assets) {
    assertSafeReleaseAssetPath(asset);
    const name = basename2(asset);
    if (!existingAssetNames.has(name) && !missingAssets.has(name)) {
      missingAssets.set(name, asset);
    }
  }
  if (missingAssets.size === 0)
    return;
  const uploadUrl = validateGitHubUploadUrl(uploadUrlTemplate, apiUrl);
  const preparedAssets = await readMissingReleaseAssets(cwd, missingAssets);
  try {
    for (const prepared of preparedAssets.values()) {
      await assertStableReleaseAssetDescriptor(prepared);
    }
    for (const prepared of preparedAssets.values()) {
      await assertStableReleaseAssetDescriptor(prepared);
      await uploadAsset(uploadUrl, token, prepared.data, prepared.name);
    }
  } finally {
    await Promise.all(preparedAssets.map(async ({ file }) => file.close()));
  }
}
var maxReleaseAssetSizeBytes = 100 * 1024 * 1024;
async function readMissingReleaseAssets(cwd, missingAssets) {
  let root;
  try {
    root = await fs.realpath(cwd);
    if (!(await fs.lstat(root)).isDirectory()) {
      throw new HooversionError(`Release asset root is not a directory: ${cwd}`);
    }
  } catch (error) {
    if (error instanceof HooversionError)
      throw error;
    throw new HooversionError(`Could not resolve release asset root: ${cwd}`);
  }
  const preparedAssets = [];
  try {
    for (const [name, asset] of missingAssets) {
      preparedAssets.push(await readValidatedReleaseAsset(root, asset, name));
    }
    return preparedAssets;
  } catch (error) {
    await Promise.all(preparedAssets.map(async ({ file }) => file.close()));
    throw error;
  }
}
function assertSafeReleaseAssetPath(asset) {
  if (asset.length === 0 || asset.includes("\x00") || isAbsolute(asset) || win32.isAbsolute(asset) || asset.split(/[\\/]+/u).includes("..")) {
    throw new HooversionError(`Release asset path must be relative without parent traversal: ${asset}`);
  }
}
function isContainedPath(root, path) {
  const pathFromRoot = relative4(root, path);
  return pathFromRoot === "" || pathFromRoot !== ".." && !pathFromRoot.startsWith(`..${sep2}`) && !isAbsolute(pathFromRoot);
}
function resolveReleaseAssetPath(root, asset) {
  const path = resolve2(root, asset);
  if (!isContainedPath(root, path)) {
    throw new HooversionError(`Release asset path escapes the repository: ${asset}`);
  }
  return path;
}
async function readValidatedReleaseAsset(root, asset, name) {
  const path = resolveReleaseAssetPath(root, asset);
  let file;
  try {
    const noFollow = fsConstants3.O_NOFOLLOW;
    if (typeof noFollow !== "number") {
      throw new HooversionError("Secure release asset uploads require O_NOFOLLOW support.");
    }
    file = await fs.open(path, fsConstants3.O_RDONLY | noFollow);
    const descriptorStats = await file.stat();
    assertRegularReleaseAsset(descriptorStats, asset);
    const descriptorMetadata = releaseAssetMetadata(descriptorStats);
    await assertStableReleaseAssetPath(root, path, asset, descriptorMetadata);
    const data = await readReleaseAssetDescriptor(file, descriptorMetadata.size, asset);
    const afterReadMetadata = releaseAssetMetadata(await file.stat());
    if (!sameReleaseAssetMetadata(descriptorMetadata, afterReadMetadata) || data.byteLength !== descriptorMetadata.size) {
      throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
    }
    await assertStableReleaseAssetPath(root, path, asset, afterReadMetadata);
    return { name, path, root, data, file, metadata: afterReadMetadata };
  } catch (error) {
    if (file)
      await file.close();
    if (error instanceof HooversionError)
      throw error;
    const reason = error instanceof Error ? error.message : "unknown error";
    throw new HooversionError(`Could not securely read release asset ${asset}: ${reason}`);
  }
}
async function assertStableReleaseAssetDescriptor(asset) {
  const descriptorMetadata = releaseAssetMetadata(await asset.file.stat());
  if (!sameReleaseAssetMetadata(asset.metadata, descriptorMetadata)) {
    throw new HooversionError(`Release asset changed while it was being read: ${asset.name}`);
  }
  await assertStableReleaseAssetPath(asset.root, asset.path, asset.name, descriptorMetadata);
}
function assertRegularReleaseAsset(stats, asset) {
  if (!stats.isFile() || stats.isSymbolicLink()) {
    throw new HooversionError(`Release asset must be a regular file, not a symbolic link: ${asset}`);
  }
  if (!Number.isSafeInteger(stats.size) || stats.size < 0 || stats.size > maxReleaseAssetSizeBytes) {
    throw new HooversionError(`Release asset exceeds the ${maxReleaseAssetSizeBytes} byte upload limit: ${asset}`);
  }
}
function releaseAssetMetadata(stats) {
  return {
    dev: stats.dev,
    ino: stats.ino,
    size: stats.size,
    mtimeMs: stats.mtimeMs,
    ctimeMs: stats.ctimeMs
  };
}
function sameReleaseAssetMetadata(left, right) {
  return left.dev === right.dev && left.ino === right.ino && left.size === right.size && left.mtimeMs === right.mtimeMs && left.ctimeMs === right.ctimeMs;
}
async function assertStableReleaseAssetPath(root, path, asset, expected) {
  const canonicalPath = await fs.realpath(path);
  if (!isContainedPath(root, canonicalPath)) {
    throw new HooversionError(`Release asset path escapes the repository: ${asset}`);
  }
  if (canonicalPath !== path) {
    throw new HooversionError(`Release asset path must not traverse a symbolic link: ${asset}`);
  }
  const pathStats = await fs.lstat(path);
  assertRegularReleaseAsset(pathStats, asset);
  if (!sameReleaseAssetMetadata(expected, releaseAssetMetadata(pathStats))) {
    throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
  }
}
async function readReleaseAssetDescriptor(file, size, asset) {
  const data = Buffer.allocUnsafe(size);
  let offset = 0;
  while (offset < data.byteLength) {
    const { bytesRead } = await file.read(data, offset, data.byteLength - offset, offset);
    if (bytesRead === 0) {
      throw new HooversionError(`Release asset changed while it was being read: ${asset}`);
    }
    offset += bytesRead;
  }
  return data;
}
function releaseAssetNames(assets) {
  if (assets === undefined)
    return new Set;
  if (!Array.isArray(assets)) {
    throw new HooversionError("GitHub release response has invalid assets.");
  }
  const assetNames = new Set;
  for (const asset of assets) {
    if (typeof asset !== "object" || asset === null || !("name" in asset) || typeof asset.name !== "string") {
      throw new HooversionError("GitHub release response has invalid assets.");
    }
    assetNames.add(basename2(asset.name));
  }
  return assetNames;
}
function validateGitHubUploadUrl(uploadUrlTemplate, apiUrl) {
  if (typeof uploadUrlTemplate !== "string") {
    throw new HooversionError("Invalid GitHub release upload URL.");
  }
  const uploadUrl = uploadUrlTemplate.replace(/\{\?[^}]*\}$/, "");
  if (uploadUrl.includes("{") || uploadUrl.includes("}")) {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }
  let parsed;
  try {
    parsed = new URL(uploadUrl);
  } catch {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }
  const authority = uploadUrl.slice(uploadUrl.indexOf("//") + 2).split(/[/?#]/, 1)[0] ?? "";
  const host = authority.slice(authority.lastIndexOf("@") + 1);
  const hasExplicitPort = host.startsWith("[") ? host.includes("]:") : host.includes(":");
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.search || parsed.hash || hasExplicitPort) {
    throw new HooversionError(`Invalid GitHub release upload URL: ${uploadUrlTemplate}`);
  }
  const apiOrigin = new URL(apiUrl).origin;
  const trusted = parsed.origin === apiOrigin || apiOrigin === "https://api.github.com" && parsed.origin === "https://uploads.github.com";
  if (!trusted) {
    throw new HooversionError(`Untrusted GitHub release upload URL: ${uploadUrlTemplate}`);
  }
  return parsed.toString();
}
async function uploadAsset(uploadUrl, token, data, name) {
  await githubFetch(`${uploadUrl}?name=${encodeURIComponent(name)}`, token, {
    method: "POST",
    body: data,
    headers: {
      "content-type": "application/octet-stream"
    }
  });
}
async function githubFetch(url, token, init, notFoundIsEmpty = false) {
  const response = await fetch(url, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "x-github-api-version": "2022-11-28",
      ...init.headers ?? {}
    }
  });
  if (response.status === 404 && notFoundIsEmpty)
    return;
  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }
  return await response.json();
}

// src/output.ts
import {
  appendFileSync,
  closeSync as closeSync3,
  constants as fsConstants4,
  fstatSync as fstatSync3,
  mkdirSync as mkdirSync2,
  openSync as openSync3,
  readFileSync as readFileSync5,
  unlinkSync as unlinkSync2,
  writeFileSync as writeFileSync3
} from "fs";
import { join as join4, relative as relative5, sep as sep3 } from "path";
function getReleaseOutputPaths(cwd, outputDir = ".hooversion") {
  const resolvedOutputDir = join4(cwd, outputDir);
  const outputsPath = join4(resolvedOutputDir, "outputs.json");
  const paths = new Set([outputsPath, join4(cwd, ".release-version")]);
  let fd;
  try {
    fd = openSync3(outputsPath, fsConstants4.O_RDONLY | fsConstants4.O_NOFOLLOW | fsConstants4.O_NONBLOCK);
    if (!fstatSync3(fd).isFile())
      return [...paths];
    const payload = JSON.parse(readFileSync5(fd, "utf8"));
    for (const release of payload.releases ?? []) {
      if (typeof release.tag !== "string")
        continue;
      const notePath = join4(resolvedOutputDir, `${sanitizeFileName(release.tag)}-notes.md`);
      const noteRelativePath = relative5(resolvedOutputDir, notePath);
      if (noteRelativePath && noteRelativePath !== ".." && !noteRelativePath.startsWith(`..${sep3}`)) {
        paths.add(notePath);
      }
    }
  } catch {} finally {
    if (fd !== undefined) {
      try {
        closeSync3(fd);
      } catch {}
    }
  }
  return [...paths];
}
function clearReleaseOutputs(cwd, outputDir = ".hooversion") {
  for (const outputPath of getReleaseOutputPaths(cwd, outputDir)) {
    try {
      unlinkSync2(outputPath);
    } catch (error) {
      if (error.code !== "ENOENT")
        throw error;
    }
  }
}
function writeReleaseOutputs(cwd, config, plan) {
  clearReleaseOutputs(cwd, config.outputDir);
  const outputDir = join4(cwd, config.outputDir);
  mkdirSync2(outputDir, { recursive: true });
  for (const release of plan.releases) {
    writeFileSync3(join4(outputDir, `${sanitizeFileName(release.tag)}-notes.md`), `${release.notes}
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
  writeFileSync3(join4(outputDir, "outputs.json"), `${JSON.stringify(payload, null, 2)}
`);
  if (plan.releases.length === 1) {
    writeFileSync3(join4(cwd, ".release-version"), `${plan.releases[0].nextVersion}
`);
  } else {
    try {
      unlinkSync2(join4(cwd, ".release-version"));
    } catch (error) {
      if (error.code !== "ENOENT")
        throw error;
    }
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
  const effectivePlan = deriveResumablePlan(cwd, config, plan) ?? plan;
  const resumable = isResumableRelease(cwd, effectivePlan);
  if (resumable) {
    verifyResumableRemote(cwd, effectivePlan, options.gitAuth);
  } else {
    verifySource(cwd, effectivePlan, options.gitAuth);
  }
  validatePlan(cwd, config, effectivePlan, resumable);
  if (options.dryRun)
    return { plan: effectivePlan, published: false };
  ensureCleanWorkingTree(cwd, getReleaseOutputPaths(cwd, config.outputDir), config.outputDir);
  clearReleaseOutputs(cwd, config.outputDir);
  if (effectivePlan.releases.length === 0) {
    writeReleaseOutputs(cwd, config, effectivePlan);
    return { plan: effectivePlan, published: false };
  }
  if (!resumable) {
    runHooks(cwd, config.hooks.beforeRelease);
    const releasedVersions = new Map(effectivePlan.releases.map((release) => [release.package.name, release.nextVersion]));
    for (const release of effectivePlan.releases) {
      updateManifestVersion(cwd, release.package, release.nextVersion);
    }
    updateLocalDependencyVersions(cwd, config.packages, releasedVersions);
    for (const release of effectivePlan.releases) {
      updateChangelog(cwd, release);
    }
    runHooks(cwd, config.hooks.afterVersion);
    createReleaseCommit(cwd, releaseCommitMessage(effectivePlan));
    for (const release of effectivePlan.releases) {
      createAnnotatedTag(cwd, release.tag, `${release.package.name} ${release.nextVersion}`);
    }
  }
  const shouldPush = options.push ?? config.push;
  if (shouldPush) {
    pushRelease(cwd, effectivePlan.branch, effectivePlan.releases.map((release) => release.tag), options.gitAuth);
  }
  mkdirSync3(join5(cwd, config.outputDir), { recursive: true });
  if (options.github ?? true) {
    for (const release of effectivePlan.releases) {
      await publishGitHubRelease(cwd, config, release, { token: options.githubToken });
    }
  }
  writeReleaseOutputs(cwd, config, effectivePlan);
  runHooks(cwd, config.hooks.afterRelease);
  return { plan: effectivePlan, published: true };
}
function validatePlan(cwd, config, plan, resumable = isResumableRelease(cwd, plan)) {
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
    if (resumable)
      continue;
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
function deriveResumablePlan(cwd, config, plan) {
  if (plan.releases.length > 0)
    return;
  const head = getHeadSha(cwd);
  const sourceSha = getRefSha(cwd, "HEAD^");
  if (!sourceSha)
    return;
  const taggedPackages = config.packages.map((pkg) => {
    const nextVersion = readManifest(cwd, pkg).version;
    const tag = tagForPackage(config, pkg, nextVersion);
    return getRefSha(cwd, `refs/tags/${tag}`) === head ? { pkg, nextVersion, tag } : undefined;
  }).filter((release) => Boolean(release));
  if (taggedPackages.length === 0)
    return;
  const message = getCommitMessage(cwd);
  const separator = message.indexOf(`
`);
  const subject = separator < 0 ? message : message.slice(0, separator);
  const body = separator < 0 ? "" : message.slice(separator).trim();
  const expectedSubject = taggedPackages.length === 1 ? `chore(release): ${taggedPackages[0].pkg.name} ${taggedPackages[0].nextVersion}` : `chore(release): ${taggedPackages.map(({ pkg, nextVersion }) => `${pkg.name}@${nextVersion}`).join(", ")}`;
  if (subject !== expectedSubject)
    return;
  const transitions = taggedPackages.map(({ pkg, nextVersion }) => inferReleaseTransition(cwd, config, pkg, nextVersion));
  if (transitions.some((transition) => !transition))
    return;
  const releases = taggedPackages.map(({ pkg, nextVersion, tag }, index) => {
    const transition = transitions[index];
    const marker = `# ${pkg.name} ${nextVersion}

`;
    const markerStart = body.indexOf(marker);
    const notes = taggedPackages.length === 1 ? body : markerStart < 0 ? "" : body.slice(markerStart + marker.length).split(/\n\n# /, 1)[0];
    return {
      package: pkg,
      currentVersion: transition.currentVersion,
      nextVersion,
      releaseType: transition.releaseType,
      tag,
      commits: [],
      notes,
      changelogPath: pkg.changelog,
      dependencyTriggered: false
    };
  });
  const reconstructed = {
    ...plan,
    sourceSha,
    releases,
    unmatchedCommits: []
  };
  return getCommitMessage(cwd) === releaseCommitMessage(reconstructed) ? reconstructed : undefined;
}
function inferReleaseTransition(cwd, config, pkg, nextVersion) {
  const parts = parseVersion(nextVersion);
  const candidates = [
    ["major", `${parts.major - 1}.0.0`],
    ["minor", `${parts.major}.${parts.minor - 1}.0`],
    ["patch", `${parts.major}.${parts.minor}.${parts.patch - 1}`]
  ];
  for (const [releaseType, currentVersion] of candidates) {
    if (parts.major < 1 && releaseType === "major")
      continue;
    if (parts.minor < 1 && releaseType === "minor")
      continue;
    if (parts.patch < 1 && releaseType === "patch")
      continue;
    const tag = tagForPackage(config, pkg, currentVersion);
    if (getRefSha(cwd, `refs/tags/${tag}`))
      return { currentVersion, releaseType };
  }
  return;
}
function isResumableRelease(cwd, plan) {
  if (plan.releases.length === 0)
    return false;
  const head = getHeadSha(cwd);
  if (head === plan.sourceSha)
    return false;
  if (getRefSha(cwd, "HEAD^") !== plan.sourceSha)
    return false;
  if (getCommitMessage(cwd) !== releaseCommitMessage(plan))
    return false;
  return plan.releases.every((release) => getRefSha(cwd, `refs/tags/${release.tag}`) === head);
}
function verifySource(cwd, plan, gitAuth) {
  const head = getHeadSha(cwd);
  if (head !== plan.sourceSha) {
    throw new HooversionError(`Release source changed locally: expected ${plan.sourceSha}, found ${head}.`);
  }
  const remote = getRemoteBranchSha(cwd, plan.branch, gitAuth);
  if (remote !== undefined && remote !== plan.sourceSha) {
    throw new HooversionError(`Release source changed remotely: expected ${plan.sourceSha}, found ${remote || "missing"}.`);
  }
}
function verifyResumableRemote(cwd, plan, gitAuth) {
  const head = getHeadSha(cwd);
  const remote = getRemoteBranchSha(cwd, plan.branch, gitAuth);
  if (remote !== undefined && remote !== head && remote !== plan.sourceSha) {
    throw new HooversionError(`Release resume found remote drift: expected ${head}, found ${remote || "missing"}.`);
  }
}

// src/app-runner.ts
var GIT_ASKPASS_FILENAME = ".git-askpass";
var GIT_TOKEN_ENV = "VERSIONHOO_GIT_TOKEN";
var GIT_ASKPASS_SCRIPT = `#!/bin/sh
case "$1" in
  *[Uu][Ss][Ee][Rr][Nn][Aa][Mm][Ee]*) printf "%s\\n" "x-access-token" ;;
  *) printf "%s\\n" "$VERSIONHOO_GIT_TOKEN" ;;
esac
`;
async function runVersionhooRelease(job) {
  const parent = job.workDir ?? join6(tmpdir(), "versionhoo");
  mkdirSync4(parent, { recursive: true });
  const workDir = mkdtempSync(join6(parent, "release-"));
  const repoDir = join6(workDir, "repo");
  const repositoryHome = mkdtempSync(join6(workDir, ".home-"));
  try {
    return await withRepositoryEnvironment(job, async () => {
      try {
        const cloneUrl = validateCloneUrl(job.cloneUrl, job.repositoryFullName, job.trustedCloneHosts);
        const gitAuth = createGitAuthArtifacts(workDir, job.token);
        try {
          checked("git", ["clone", "--branch", job.branch, "--no-single-branch", cloneUrl, repoDir], workDir, job.token, gitAuth.env);
          checked("git", ["config", "user.name", job.gitAuthorName ?? "versionhoo[bot]"], repoDir, job.token);
          checked("git", ["config", "user.email", job.gitAuthorEmail ?? "versionhoo[bot]@users.noreply.github.com"], repoDir, job.token);
          const branchHead = runCommand("git", ["rev-parse", "HEAD"], repoDir).stdout.trim();
          if (branchHead !== job.headSha) {
            return {
              repositoryFullName: job.repositoryFullName,
              branch: job.branch,
              headSha: job.headSha,
              workDir,
              outcome: "stale",
              published: false,
              message: `Skipped stale workflow run for ${job.repositoryFullName}@${job.branch}: branch is ${branchHead}, workflow passed on ${job.headSha}.`,
              releases: []
            };
          }
          installProjectDependencies(repoDir, job.installCommand, job.token);
          const config = await loadConfig(repoDir, job.configPath);
          const trustedApiUrl = validateGitHubApiUrl(job.apiUrl ?? "https://api.github.com", job.trustedApiUrls);
          if (config.github !== false) {
            config.github.repository = validateRepositoryFullName(job.repositoryFullName);
            config.github.apiUrl = trustedApiUrl;
          }
          const plan = createReleasePlan(repoDir, config);
          const execution = await executeRelease(repoDir, config, plan, {
            push: true,
            github: true,
            githubToken: job.token,
            gitAuth: gitAuth.env
          });
          return {
            repositoryFullName: job.repositoryFullName,
            branch: job.branch,
            headSha: job.headSha,
            workDir,
            outcome: execution.published ? "published" : "no_release",
            published: execution.published,
            releases: execution.plan.releases.map((release) => ({
              name: release.package.name,
              version: release.nextVersion,
              tag: release.tag
            }))
          };
        } finally {
          gitAuth.cleanup();
        }
      } finally {
        if (!job.keepWorkDir) {
          rmSync(workDir, { recursive: true, force: true });
        }
      }
    }, repositoryHome);
  } finally {
    rmSync(repositoryHome, { recursive: true, force: true });
  }
}
function createGitAuthArtifacts(workDir, token) {
  const askpassPath = join6(workDir, GIT_ASKPASS_FILENAME);
  let created = false;
  const cleanup = () => {
    if (!created)
      return;
    rmSync(askpassPath, { force: true });
    created = false;
  };
  try {
    writeFileSync4(askpassPath, GIT_ASKPASS_SCRIPT, { encoding: "utf8", flag: "wx", mode: 448 });
    created = true;
    chmodSync(askpassPath, 448);
    return {
      env: {
        GIT_ASKPASS: askpassPath,
        GIT_TERMINAL_PROMPT: "0",
        [GIT_TOKEN_ENV]: token
      },
      cleanup
    };
  } catch (error) {
    cleanup();
    throw error;
  }
}
function installProjectDependencies(repoDir, configuredCommand, secret) {
  const command = configuredCommand ?? (existsSync2(join6(repoDir, "bun.lock")) ? "bun install --frozen-lockfile" : "");
  if (!command)
    return;
  const result = runShell(command, repoDir);
  if (result.code !== 0) {
    throw new HooversionError(`Install command failed: ${redact(command, secret)}
${redact(result.stderr || result.stdout, secret)}`);
  }
}
function checked(command, args, cwd, secret, env) {
  const result = runCommand(command, args, cwd, env ? { ...process.env, ...env } : undefined);
  if (result.code !== 0) {
    const rendered = `${command} ${args.map((arg) => redact(arg, secret)).join(" ")}`;
    throw new HooversionError(`${rendered} failed:
${redact(result.stderr || result.stdout, secret)}`);
  }
}
function validateCloneUrl(cloneUrl, repositoryFullName, trustedCloneHosts = []) {
  const expected = validateRepositoryFullName(repositoryFullName);
  let parsed;
  try {
    parsed = new URL(cloneUrl);
  } catch {
    throw new HooversionError(`Invalid GitHub clone URL: ${cloneUrl}`);
  }
  if (parsed.protocol !== "https:" || parsed.username || parsed.password || parsed.port || parsed.search || parsed.hash) {
    throw new HooversionError(`Invalid GitHub clone URL: ${cloneUrl}`);
  }
  const allowedHosts = new Set(["github.com", ...trustedCloneHosts.map((host) => host.toLowerCase())]);
  if (!allowedHosts.has(parsed.hostname.toLowerCase())) {
    throw new HooversionError(`Untrusted GitHub clone host: ${parsed.hostname}`);
  }
  const path = decodeURIComponent(parsed.pathname).replace(/^\/+|\/+$/g, "").replace(/\.git$/i, "");
  if (path.toLowerCase() !== expected.toLowerCase()) {
    throw new HooversionError(`GitHub clone repository mismatch: expected ${expected}, got ${path}`);
  }
  return parsed.toString().replace(/\/+$/, "");
}
var repositoryEnvironmentTail = Promise.resolve();
async function withRepositoryEnvironment(job, operation, repositoryHome) {
  const previous = repositoryEnvironmentTail;
  let release;
  repositoryEnvironmentTail = new Promise((resolve3) => {
    release = resolve3;
  });
  await previous;
  const original = { ...process.env };
  const allowed = {
    PATH: original.PATH ?? "",
    HOME: repositoryHome,
    SHELL: original.SHELL ?? "/bin/sh",
    LANG: original.LANG ?? "C.UTF-8",
    GIT_CONFIG_NOSYSTEM: "1",
    GIT_CONFIG_SYSTEM: "/dev/null",
    GIT_CONFIG_GLOBAL: "/dev/null",
    GIT_TERMINAL_PROMPT: "0",
    GITHUB_REPOSITORY: job.repositoryFullName,
    GITHUB_REF_NAME: job.branch,
    GITHUB_SHA: job.headSha,
    VERSIONHOO_REPOSITORY: job.repositoryFullName,
    VERSIONHOO_BRANCH: job.branch,
    VERSIONHOO_SHA: job.headSha
  };
  for (const key of Object.keys(process.env))
    delete process.env[key];
  Object.assign(process.env, allowed);
  try {
    return await operation();
  } finally {
    for (const key of Object.keys(process.env))
      delete process.env[key];
    Object.assign(process.env, original);
    release();
  }
}
function redact(value, secret) {
  return value.replaceAll(secret, "[redacted]");
}

// src/app-github.ts
var checkName = "Versionhoo Release";
var githubApiVersion2 = "2022-11-28";
async function createReleaseCheckRun(apiUrl, repository, token, headSha, expectedRepository, trustedApiUrls = []) {
  const response = await githubFetch2(apiUrl, `/repos/${validatedRepository(repository, expectedRepository)}/check-runs`, token, {
    method: "POST",
    body: JSON.stringify({
      name: checkName,
      head_sha: headSha,
      status: "in_progress",
      started_at: new Date().toISOString(),
      output: {
        title: "Versionhoo release started",
        summary: "Versionhoo accepted this workflow run and started release processing."
      }
    })
  }, trustedApiUrls);
  return { id: response.id, htmlUrl: response.html_url };
}
async function completeReleaseCheckRun(apiUrl, repository, token, checkRunId, conclusion, title, summary, expectedRepository, trustedApiUrls = []) {
  await githubFetch2(apiUrl, `/repos/${validatedRepository(repository, expectedRepository)}/check-runs/${checkRunId}`, token, {
    method: "PATCH",
    body: JSON.stringify({
      status: "completed",
      conclusion,
      completed_at: new Date().toISOString(),
      output: { title, summary }
    })
  }, trustedApiUrls);
}
function releaseCheckResult(result) {
  if (result.outcome === "stale") {
    return {
      conclusion: "neutral",
      title: "Versionhoo skipped a stale workflow run",
      summary: result.message ?? "The release branch moved after this workflow run completed."
    };
  }
  if (!result.published) {
    return {
      conclusion: "neutral",
      title: "Versionhoo found no release",
      summary: "No release-worthy commits were found for this workflow run."
    };
  }
  const releases = result.releases.map((release) => `- ${release.name} ${release.version} (${release.tag})`).join(`
`);
  return {
    conclusion: "success",
    title: "Versionhoo published releases",
    summary: releases || "Versionhoo published releases."
  };
}
function releaseFailureCheckResult(error) {
  const message = error instanceof Error ? error.message : String(error);
  return {
    conclusion: "failure",
    title: "Versionhoo release failed",
    summary: truncate(message, 60000)
  };
}
async function githubFetch2(apiUrl, path, token, init, trustedApiUrls) {
  const trustedApiUrl = validateGitHubApiUrl(apiUrl, trustedApiUrls);
  const response = await fetch(`${trustedApiUrl}${path}`, {
    ...init,
    headers: {
      accept: "application/vnd.github+json",
      authorization: `Bearer ${token}`,
      "content-type": "application/json",
      "user-agent": "versionhoo-app",
      "x-github-api-version": githubApiVersion2,
      ...init.headers ?? {}
    }
  });
  if (!response.ok) {
    const body = await response.text();
    throw new HooversionError(`GitHub API request failed (${response.status} ${response.statusText}): ${body}`);
  }
  return await response.json();
}
function validatedRepository(repository, expectedRepository) {
  const validated = validateRepositoryFullName(repository);
  const expected = validateRepositoryFullName(expectedRepository);
  if (validated.toLowerCase() !== expected.toLowerCase()) {
    throw new HooversionError(`GitHub repository mismatch: expected ${expected}, got ${validated}`);
  }
  return validated;
}
function truncate(value, maxLength) {
  return value.length <= maxLength ? value : `${value.slice(0, maxLength - 3)}...`;
}

// src/app-server.ts
var DEFAULT_WEBHOOK_MAX_BODY_BYTES = 1024 * 1024;
function isPositiveInteger(value) {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}

class ReleaseTaskQueue {
  tails = new Map;
  lastFailure;
  onFailure;
  maxAttempts;
  retryDelayMs;
  constructor(onFailure = (error) => {
    console.error(error);
  }, options = {}) {
    this.onFailure = onFailure;
    this.maxAttempts = Math.max(1, Math.min(3, Math.floor(options.maxAttempts ?? 1)));
    this.retryDelayMs = Math.max(0, Math.min(30000, Math.floor(options.retryDelayMs ?? 0)));
  }
  enqueue(key, task, onFinalFailure) {
    const previous = this.tails.get(key) ?? Promise.resolve();
    const next = previous.then(() => this.runWithRetry(task), () => this.runWithRetry(task)).catch((error) => {
      this.lastFailure = error;
      try {
        onFinalFailure?.(error);
        this.onFailure(error);
      } catch (callbackError) {
        this.lastFailure = callbackError;
      }
    }).finally(() => {
      if (this.tails.get(key) === next)
        this.tails.delete(key);
    });
    this.tails.set(key, next);
    return next;
  }
  get failure() {
    return this.lastFailure;
  }
  async runWithRetry(task) {
    for (let attempt = 1;; attempt += 1) {
      try {
        await task();
        return;
      } catch (error) {
        if (attempt >= this.maxAttempts)
          throw error;
        if (this.retryDelayMs > 0) {
          const { promise, resolve: resolve3 } = Promise.withResolvers();
          setTimeout(resolve3, this.retryDelayMs);
          await promise;
        }
      }
    }
  }
}

class WebhookDeduper {
  ttlMs;
  now;
  seen = new Map;
  constructor(ttlMs = 24 * 60 * 60 * 1000, now = () => Date.now()) {
    this.ttlMs = ttlMs;
    this.now = now;
  }
  reserve(key) {
    if (!key)
      return true;
    this.prune();
    if (this.seen.has(key))
      return false;
    this.seen.set(key, { state: "in_flight", expiresAt: this.now() + this.ttlMs });
    return true;
  }
  succeed(key) {
    if (!key)
      return;
    const entry = this.seen.get(key);
    if (entry) {
      entry.state = "succeeded";
      entry.expiresAt = this.now() + this.ttlMs;
    }
  }
  release(key) {
    if (key)
      this.seen.delete(key);
  }
  remember(key) {
    const reserved = this.reserve(key);
    if (reserved)
      this.succeed(key);
    return reserved;
  }
  prune() {
    const now = this.now();
    for (const [key, entry] of this.seen) {
      if (entry.expiresAt <= now)
        this.seen.delete(key);
    }
  }
}
function loadVersionhooAppConfigFromEnv(env = process.env) {
  const appId = readRequiredEnv(env, ["VERSIONHOO_APP_ID", "HOOVERSION_APP_ID"]);
  const webhookSecret = readRequiredEnv(env, ["VERSIONHOO_WEBHOOK_SECRET", "HOOVERSION_WEBHOOK_SECRET"]);
  const port = Number(readEnv(env, ["VERSIONHOO_PORT", "HOOVERSION_PORT"]) ?? "3000");
  if (!Number.isInteger(port) || port <= 0) {
    throw new HooversionError("VERSIONHOO_PORT must be a positive integer.");
  }
  const webhookMaxBodyBytes = Number(readEnv(env, ["VERSIONHOO_WEBHOOK_MAX_BODY_BYTES", "HOOVERSION_WEBHOOK_MAX_BODY_BYTES"]) ?? String(DEFAULT_WEBHOOK_MAX_BODY_BYTES));
  if (!Number.isInteger(webhookMaxBodyBytes) || webhookMaxBodyBytes <= 0) {
    throw new HooversionError("VERSIONHOO_WEBHOOK_MAX_BODY_BYTES must be a positive integer.");
  }
  const apiUrl = readEnv(env, ["VERSIONHOO_GITHUB_API_URL", "HOOVERSION_GITHUB_API_URL"]) ?? "https://api.github.com";
  const trustedApiUrls = splitCsv(readEnv(env, [
    "VERSIONHOO_TRUSTED_GITHUB_API_URLS",
    "HOOVERSION_TRUSTED_GITHUB_API_URLS",
    "VERSIONHOO_TRUSTED_API_URLS",
    "HOOVERSION_TRUSTED_API_URLS"
  ]));
  const trustedCloneHosts = splitCsv(readEnv(env, [
    "VERSIONHOO_TRUSTED_GITHUB_CLONE_HOSTS",
    "HOOVERSION_TRUSTED_GITHUB_CLONE_HOSTS",
    "VERSIONHOO_TRUSTED_CLONE_HOSTS",
    "HOOVERSION_TRUSTED_CLONE_HOSTS"
  ]));
  return {
    appId,
    privateKey: readGitHubAppPrivateKey(env),
    webhookSecret,
    apiUrl,
    trustedApiUrls,
    trustedCloneHosts,
    host: readEnv(env, ["VERSIONHOO_HOST", "HOOVERSION_HOST"]) ?? "0.0.0.0",
    port,
    workDir: readEnv(env, ["VERSIONHOO_WORKDIR", "HOOVERSION_WORKDIR"]),
    configPath: readEnv(env, ["VERSIONHOO_CONFIG", "HOOVERSION_CONFIG"]),
    installCommand: readEnv(env, ["VERSIONHOO_INSTALL_COMMAND", "HOOVERSION_INSTALL_COMMAND"]),
    allowedRepositories: splitCsv(readEnv(env, ["VERSIONHOO_ALLOWED_REPOS", "HOOVERSION_ALLOWED_REPOS"])),
    releaseBranches: splitCsv(readEnv(env, ["VERSIONHOO_RELEASE_BRANCHES", "HOOVERSION_RELEASE_BRANCHES"]) ?? "main"),
    ciWorkflowNames: splitCsv(readEnv(env, ["VERSIONHOO_CI_WORKFLOWS", "HOOVERSION_CI_WORKFLOWS"]) ?? "CI"),
    gitAuthorName: readEnv(env, ["VERSIONHOO_GIT_AUTHOR_NAME", "HOOVERSION_GIT_AUTHOR_NAME"]),
    gitAuthorEmail: readEnv(env, ["VERSIONHOO_GIT_AUTHOR_EMAIL", "HOOVERSION_GIT_AUTHOR_EMAIL"]),
    keepWorkDir: readBoolean(readEnv(env, ["VERSIONHOO_KEEP_WORKDIR", "HOOVERSION_KEEP_WORKDIR"])),
    webhookMaxBodyBytes
  };
}
function startVersionhooApp(config) {
  const queue = new ReleaseTaskQueue;
  const deduper = new WebhookDeduper;
  const handler = createVersionhooWebhookHandler(config, runVersionhooRelease, queue, deduper);
  const server = Bun.serve({
    hostname: config.host,
    port: config.port,
    async fetch(request) {
      const url = new URL(request.url);
      if (request.method === "GET" && url.pathname === "/health") {
        return json({ ok: true });
      }
      if (request.method === "POST" && url.pathname === "/webhooks/github") {
        return handler(request);
      }
      return json({ error: "not found" }, 404);
    }
  });
  console.log(`versionhoo app listening on http://${server.hostname}:${server.port}`);
  return server;
}
function createVersionhooWebhookHandler(config, runner, queue = new ReleaseTaskQueue, deduper = new WebhookDeduper) {
  return async (request) => {
    const event = request.headers.get("x-github-event");
    const delivery = request.headers.get("x-github-delivery") ?? "unknown";
    const maxBodyBytes = resolveWebhookMaxBodyBytes(config.webhookMaxBodyBytes);
    const body = await readWebhookBody(request, maxBodyBytes);
    if (body instanceof Response)
      return body;
    if (!verifyGitHubWebhookSignature(config.webhookSecret, body, request.headers.get("x-hub-signature-256"))) {
      return json({ error: "invalid webhook signature" }, 401);
    }
    if (event === "ping") {
      return json({ ok: true, delivery });
    }
    if (event !== "workflow_run") {
      return json({ ok: true, status: "ignored", reason: `unsupported event: ${event ?? "unknown"}` }, 202);
    }
    let parsed;
    try {
      parsed = JSON.parse(body);
    } catch {
      return json({ error: "invalid JSON webhook body" }, 400);
    }
    const validationError = validateWorkflowRunPayload(parsed, config.trustedCloneHosts);
    if (validationError)
      return json({ error: validationError }, 400);
    assertWorkflowRunPayload(parsed, config.trustedCloneHosts);
    const payload = parsed;
    const deliveryKey = delivery === "unknown" ? undefined : `delivery:${delivery}`;
    const workflowKey = `workflow_run:${workflowRunKey(payload)}`;
    if (!deduper.reserve(deliveryKey)) {
      return json({ ok: true, status: "ignored", reason: "duplicate delivery", delivery }, 202);
    }
    if (!deduper.reserve(workflowKey)) {
      deduper.release(deliveryKey);
      return json({ ok: true, status: "ignored", reason: "duplicate workflow run", delivery }, 202);
    }
    queue.enqueue(releaseQueueKey(payload), async () => {
      await releaseFromWorkflowRun(payload, config, runner);
      deduper.succeed(deliveryKey);
      deduper.succeed(workflowKey);
    }, () => {
      deduper.release(deliveryKey);
      deduper.release(workflowKey);
    });
    return json({ ok: true, status: "accepted", delivery }, 202);
  };
}
function resolveWebhookMaxBodyBytes(value) {
  const resolvedValue = value ?? DEFAULT_WEBHOOK_MAX_BODY_BYTES;
  return isPositiveInteger(resolvedValue) ? resolvedValue : DEFAULT_WEBHOOK_MAX_BODY_BYTES;
}
async function readWebhookBody(request, maxBytes) {
  const declaredLength = request.headers.get("content-length");
  if (declaredLength !== null) {
    const length = Number(declaredLength);
    if (Number.isFinite(length) && length > maxBytes) {
      return json({ error: "webhook payload too large" }, 413);
    }
  }
  if (!request.body)
    return "";
  const reader = request.body.getReader();
  const chunks = [];
  let total = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done)
        break;
      const chunk = value instanceof Uint8Array ? value : new Uint8Array(value);
      if (total + chunk.byteLength > maxBytes) {
        await reader.cancel().catch(() => {
          return;
        });
        return json({ error: "webhook payload too large" }, 413);
      }
      chunks.push(chunk);
      total += chunk.byteLength;
    }
  } finally {
    reader.releaseLock();
  }
  const body = new Uint8Array(total);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return new TextDecoder().decode(body);
}
function validateWorkflowRunPayload(value, trustedCloneHosts = []) {
  if (!value || typeof value !== "object")
    return "invalid webhook payload: expected an object";
  const payload = value;
  if (typeof payload.action !== "string")
    return "invalid webhook payload: missing workflow_run action";
  if (!payload.repository || typeof payload.repository !== "object") {
    return "invalid webhook payload: missing repository";
  }
  const repository = payload.repository;
  const repositoryId = repository.id;
  if (!isPositiveInteger(repositoryId) || typeof repository.full_name !== "string" || !/^[^/\s]+\/[^/\s]+$/.test(repository.full_name) || typeof repository.clone_url !== "string" || typeof repository.default_branch !== "string" || repository.default_branch.length === 0) {
    return "invalid webhook payload: malformed repository metadata";
  }
  let cloneUrl;
  try {
    cloneUrl = new URL(repository.clone_url);
  } catch {
    return "invalid webhook payload: malformed clone metadata";
  }
  if (cloneUrl.protocol !== "https:" || !["github.com", ...trustedCloneHosts.map((host) => host.toLowerCase())].includes(cloneUrl.hostname.toLowerCase()) || cloneUrl.port || cloneUrl.username || cloneUrl.password || cloneUrl.search || cloneUrl.hash || cloneUrl.pathname !== `/${repository.full_name}.git`) {
    return "invalid webhook payload: malformed clone metadata";
  }
  if (!payload.installation || typeof payload.installation !== "object") {
    return "invalid webhook payload: missing installation";
  }
  const installation = payload.installation;
  const installationId = installation.id;
  if (!isPositiveInteger(installationId)) {
    return "invalid webhook payload: malformed installation";
  }
  if (!payload.workflow_run || typeof payload.workflow_run !== "object") {
    return "invalid webhook payload: missing workflow_run";
  }
  const workflowRun = payload.workflow_run;
  if (typeof workflowRun.name !== "string" || typeof workflowRun.event !== "string" || typeof workflowRun.conclusion !== "string" && workflowRun.conclusion !== null || typeof workflowRun.head_branch !== "string" && workflowRun.head_branch !== null || typeof workflowRun.head_sha !== "string" || workflowRun.head_sha.length === 0) {
    return "invalid webhook payload: malformed workflow_run metadata";
  }
  if (workflowRun.head_repository !== undefined && workflowRun.head_repository !== null && (typeof workflowRun.head_repository !== "object" || typeof workflowRun.head_repository.full_name !== "string")) {
    return "invalid webhook payload: malformed workflow_run repository metadata";
  }
  return;
}
function assertWorkflowRunPayload(value, trustedCloneHosts = []) {
  const validationError = validateWorkflowRunPayload(value, trustedCloneHosts);
  if (validationError)
    throw new HooversionError(validationError);
}
function shouldHandleWorkflowRun(payload, config) {
  const validationError = validateWorkflowRunPayload(payload, config.trustedCloneHosts);
  if (validationError)
    return ignored(validationError);
  if (payload.action !== "completed")
    return ignored(`workflow_run action is ${payload.action}`);
  if (payload.workflow_run.conclusion !== "success") {
    return ignored(`workflow_run conclusion is ${payload.workflow_run.conclusion ?? "missing"}`);
  }
  if (payload.workflow_run.event !== "push")
    return ignored(`workflow_run event is ${payload.workflow_run.event}`);
  if (!config.ciWorkflowNames.includes(payload.workflow_run.name)) {
    return ignored(`workflow ${payload.workflow_run.name} is not configured for releases`);
  }
  const branch = payload.workflow_run.head_branch;
  if (!branch || !config.releaseBranches.includes(branch)) {
    return ignored(`branch ${branch ?? "missing"} is not a release branch`);
  }
  if (payload.workflow_run.head_repository?.full_name && payload.workflow_run.head_repository.full_name !== payload.repository.full_name) {
    return ignored("workflow_run came from a fork");
  }
  if (config.allowedRepositories.length > 0 && !config.allowedRepositories.includes(payload.repository.full_name)) {
    return ignored(`repository ${payload.repository.full_name} is not allowed`);
  }
  if (isReleaseCommit(payload.workflow_run.head_commit?.message ?? ""))
    return ignored("release commit");
  if (!payload.installation?.id)
    return ignored("missing installation id");
  if (!payload.workflow_run.head_sha)
    return ignored("missing workflow head sha");
  return { status: "accepted" };
}
async function releaseFromWorkflowRun(payload, config, runner = runVersionhooRelease) {
  const decision = shouldHandleWorkflowRun(payload, config);
  if (decision.status === "ignored")
    return;
  assertWorkflowRunPayload(payload, config.trustedCloneHosts);
  const installation = payload.installation;
  if (!installation)
    throw new HooversionError("Workflow run payload is missing installation id or branch.");
  const installationId = installation.id;
  const branch = payload.workflow_run.head_branch;
  if (branch === null)
    throw new HooversionError("Workflow run payload is missing installation id or branch.");
  const headSha = payload.workflow_run.head_sha;
  const repositoryId = payload.repository.id;
  const repositoryFullName = payload.repository.full_name;
  const cloneUrl = payload.repository.clone_url;
  const access = await createInstallationAccessToken({ appId: config.appId, privateKey: config.privateKey, apiUrl: config.apiUrl, trustedApiUrls: config.trustedApiUrls }, installationId, { id: repositoryId, fullName: repositoryFullName });
  let checkRun;
  try {
    checkRun = await createReleaseCheckRun(config.apiUrl, repositoryFullName, access.token, headSha, repositoryFullName, config.trustedApiUrls).catch((error) => {
      console.warn(`Could not create Versionhoo Release check: ${error instanceof Error ? error.message : error}`);
      return;
    });
    const result = await runner({
      repositoryFullName,
      cloneUrl,
      branch,
      headSha,
      token: access.token,
      apiUrl: config.apiUrl,
      trustedApiUrls: config.trustedApiUrls,
      trustedCloneHosts: config.trustedCloneHosts,
      workDir: config.workDir,
      configPath: config.configPath,
      installCommand: config.installCommand,
      gitAuthorName: config.gitAuthorName,
      gitAuthorEmail: config.gitAuthorEmail,
      keepWorkDir: config.keepWorkDir
    });
    if (checkRun) {
      const check = releaseCheckResult(result);
      await completeReleaseCheckRun(config.apiUrl, repositoryFullName, access.token, checkRun.id, check.conclusion, check.title, check.summary, repositoryFullName, config.trustedApiUrls).catch((error) => {
        console.warn(`Could not complete Versionhoo Release check: ${error instanceof Error ? error.message : error}`);
      });
    }
  } catch (error) {
    if (checkRun) {
      const check = releaseFailureCheckResult(error);
      await completeReleaseCheckRun(config.apiUrl, repositoryFullName, access.token, checkRun.id, check.conclusion, check.title, check.summary, repositoryFullName, config.trustedApiUrls).catch((checkError) => {
        console.warn(`Could not mark Versionhoo Release check failed: ${checkError instanceof Error ? checkError.message : checkError}`);
      });
    }
    throw error;
  }
}
function isReleaseCommit(message) {
  return /^chore\(release\):/m.test(message) || /\[skip ci\]/i.test(message);
}
function ignored(reason) {
  return { status: "ignored", reason };
}
function workflowRunKey(payload) {
  return [
    payload.repository.full_name,
    payload.workflow_run.id ?? payload.workflow_run.head_sha,
    payload.workflow_run.name,
    payload.workflow_run.head_branch ?? ""
  ].join(":");
}
function releaseQueueKey(payload) {
  return `${payload.repository.full_name}:${payload.workflow_run.head_branch ?? ""}`;
}
function readRequiredEnv(env, names) {
  const value = readEnv(env, names);
  if (!value)
    throw new HooversionError(`${names.join(" or ")} is required.`);
  return value;
}
function readEnv(env, names) {
  for (const name of names) {
    const value = env[name];
    if (value)
      return value;
  }
  return;
}
function splitCsv(value) {
  return value ? value.split(",").map((item) => item.trim()).filter(Boolean) : [];
}
function readBoolean(value) {
  return value === "1" || value === "true" || value === "yes";
}
function json(value, status = 200) {
  return new Response(JSON.stringify(value, null, 2), {
    status,
    headers: { "content-type": "application/json; charset=utf-8" }
  });
}

// src/app.ts
process.on("uncaughtException", exit);
process.on("unhandledRejection", exit);
try {
  startVersionhooApp(loadVersionhooAppConfigFromEnv());
} catch (error) {
  exit(error);
}
function exit(error) {
  if (error instanceof HooversionError) {
    console.error(error.message);
    process.exit(error.code);
  }
  console.error(error);
  process.exit(1);
}
