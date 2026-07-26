#!/usr/bin/env bun
import { existsSync, readFileSync, unlinkSync } from "node:fs";
import { basename, join } from "node:path";
import { HooversionError } from "./errors";
import { lintCommit, parseCommit } from "./commit";
import { detectPackages, findConfigPath, loadConfig, writeDefaultConfig } from "./config";
import { getCommits, getLastCommit, git } from "./git";
import { createReleasePlan } from "./plan";
import { executeRelease } from "./release";
import { runDoctor } from "./doctor";
import { writeGitHubWorkflows } from "./workflow";
import { loadVersionhooAppConfigFromEnv, startVersionhooApp } from "./app-server";
import type { ParsedCommit, ReleasePlan } from "./types";

interface CliFlags {
  values: Map<string, string>;
  booleans: Set<string>;
  positionals: string[];
}

async function main(argv = process.argv.slice(2)): Promise<void> {
  const [command = "help", ...rest] = argv;
  const flags = parseFlags(command, rest);
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
    case "app":
      startVersionhooApp(loadVersionhooAppConfigFromEnv());
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

interface CommandOptions {
  values: readonly string[];
  booleans: readonly string[];
}

const commandOptions: Readonly<Record<string, CommandOptions>> = {
  init: {
    values: ["action-owner-repo", "action-ref", "hooversion-version"],
    booleans: ["force", "no-workflow"],
  },
  lint: { values: ["edit", "from", "to"], booleans: ["last"] },
  plan: { values: ["config"], booleans: [] },
  release: { values: ["config"], booleans: ["dry-run", "no-push", "no-github"] },
  doctor: { values: ["config"], booleans: [] },
  app: { values: [], booleans: [] },
  help: { values: [], booleans: [] },
  "--help": { values: [], booleans: [] },
  "-h": { values: [], booleans: [] },
  version: { values: [], booleans: [] },
  "--version": { values: [], booleans: [] },
  "-v": { values: [], booleans: [] },
};

async function initCommand(cwd: string, flags: CliFlags): Promise<void> {
  const force = flags.booleans.has("force");
  const configPaths = [
    "hooversion.config.ts",
    "hooversion.config.mjs",
    "hooversion.config.js",
    "hooversion.config.cjs",
    "hooversion.config.json",
  ].map((name) => join(cwd, name));
  const existingConfigs = configPaths.filter((path) => existsSync(path));
  const selectedConfig = findConfigPath(cwd);
  if (existingConfigs.length > 0 && !force) {
    throw new HooversionError("Hooversion config already exists. Use --force to overwrite.");
  }
  if (force && existingConfigs.length > 1) {
    throw new HooversionError("Multiple Hooversion configs exist; remove duplicate config files before using --force.");
  }

  const packages = detectPackages(cwd);
  if (packages.length === 0) {
    throw new HooversionError("Could not detect package.json, Cargo.toml, pyproject.toml, or version.");
  }
  const workflowPaths = flags.booleans.has("no-workflow")
    ? undefined
    : writeGitHubWorkflows(cwd, {
        actionOwnerRepo: flags.values.get("action-owner-repo"),
        actionRef: flags.values.get("action-ref"),
        hooversionVersion: flags.values.get("hooversion-version"),
        force,
      });
  const configPath = writeDefaultConfig(cwd, packages);
  if (force && selectedConfig && selectedConfig !== join(cwd, "hooversion.config.ts")) {
    unlinkSync(selectedConfig);
  }
  console.log(`Wrote ${configPath}`);
  for (const workflowPath of workflowPaths ?? []) {
    console.log(`Wrote ${workflowPath}`);
  }
}

function lintCommand(cwd: string, flags: CliFlags): void {
  const commits = readLintCommits(cwd, flags);
  const issues = commits.flatMap((commit) => lintCommit(commit));
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

async function planCommand(cwd: string, flags: CliFlags): Promise<void> {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  printPlan(plan);
  if (plan.unmatchedCommits.length > 0) {
    throw new HooversionError("Plan contains unmatched release-worthy commits.");
  }
}

async function releaseCommand(cwd: string, flags: CliFlags): Promise<void> {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  const dryRun = flags.booleans.has("dry-run");
  const execution = await executeRelease(cwd, config, plan, {
    dryRun,
    push: flags.booleans.has("no-push") ? false : undefined,
    github: flags.booleans.has("no-github") ? false : undefined,
  });
  printPlan(execution.plan);
  if (dryRun) {
    console.log("Dry run complete; no files, commits, tags, or releases were created.");
  } else if (execution.published) {
    console.log("Release complete.");
  } else {
    console.log("No release needed.");
  }
}

async function doctorCommand(cwd: string, flags: CliFlags): Promise<void> {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const result = runDoctor(cwd, config);
  for (const line of result.info) console.log(`ok: ${line}`);
  for (const line of result.warnings) console.warn(`warning: ${line}`);
  for (const line of result.errors) console.error(`error: ${line}`);
  if (result.errors.length > 0) throw new HooversionError("Doctor found blocking errors.");
}

function readLintCommits(cwd: string, flags: CliFlags) {
  const selectors = [
    flags.values.has("edit"),
    flags.booleans.has("last"),
    flags.values.has("from") || flags.values.has("to"),
  ].filter(Boolean).length;
  if (selectors !== 1) {
    throw new HooversionError("lint requires exactly one selector: --last, --edit <file>, or --from <ref> [--to <ref>].");
  }

  const editPath = flags.values.get("edit");
  if (editPath) {
    const message = readFileSync(editPath, "utf8");
    const [subject = "", ...body] = message.split(/\r?\n/);
    return [{ hash: "", subject, body: body.join("\n"), files: [] }];
  }

  if (flags.booleans.has("last")) {
    return [getLastCommit(cwd)];
  }

  const from = flags.values.get("from");
  const to = flags.values.get("to") ?? "HEAD";
  if (!from) {
    throw new HooversionError("--to requires --from.");
  }
  validateGitRef(cwd, from, "from");
  validateGitRef(cwd, to, "to");
  return getCommits(cwd, from, to);
}

function validateGitRef(cwd: string, ref: string, name: string): void {
  if (!ref.trim()) throw new HooversionError(`--${name} requires a non-empty git ref.`);
  const resolved = git(cwd, ["rev-parse", "--verify", "--end-of-options", `${ref}^{commit}`], true);
  if (!resolved.trim()) throw new HooversionError(`Invalid git ref for --${name}: ${ref}`);
}

function printPlan(plan: ReleasePlan): void {
  console.log(`Branch: ${plan.branch}`);
  if (plan.unmatchedCommits.length > 0) {
    console.log("Unmatched release commits:");
    for (const commit of plan.unmatchedCommits) {
      console.log(`- ${formatCommit(commit)}`);
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
    console.log(
      `- ${release.package.name}: ${release.currentVersion} -> ${release.nextVersion} (${release.releaseType}${dependency}, ${source}) tag ${release.tag}`,
    );
    for (const commit of release.commits) {
      console.log(`  - ${formatCommit(commit)}`);
    }
  }
}
function formatCommit(commit: ParsedCommit): string {
  const parsed = parseCommit(commit);
  const scope = parsed.scope ? `(${parsed.scope})` : "";
  const breaking = parsed.breaking ? "!" : "";
  return `${parsed.hash.slice(0, 7)} ${parsed.type}${scope}${breaking}: ${parsed.description}`;
}
function parseFlags(command: string, args: string[]): CliFlags {
  const spec = commandOptions[command];
  if (!spec) throw new HooversionError(`Unknown command: ${command}`);

  const values = new Map<string, string>();
  const booleans = new Set<string>();
  const positionals: string[] = [];
  const valueOptions = new Set(spec.values);
  const booleanOptions = new Set(spec.booleans);

  for (let index = 0; index < args.length; index += 1) {
    const arg = args[index];
    if (!arg.startsWith("--")) {
      positionals.push(arg);
      continue;
    }

    const raw = arg.slice(2);
    const separator = raw.indexOf("=");
    const name = separator === -1 ? raw : raw.slice(0, separator);
    const inlineValue = separator === -1 ? undefined : raw.slice(separator + 1);
    if (!name || (!valueOptions.has(name) && !booleanOptions.has(name))) {
      throw new HooversionError(`Unknown option for ${command}: --${name || raw}`);
    }
    if (values.has(name) || booleans.has(name)) {
      throw new HooversionError(`Option may only be specified once: --${name}`);
    }

    if (valueOptions.has(name)) {
      const value = inlineValue ?? args[index + 1];
      if (value === undefined || value.startsWith("-") || !value.trim()) {
        throw new HooversionError(`Option requires a non-empty value: --${name}`);
      }
      if (inlineValue === undefined) index += 1;
      values.set(name, value);
      continue;

    }
    if (inlineValue !== undefined) {
      throw new HooversionError(`Boolean option does not accept a value: --${name}`);
    }
    booleans.add(name);
  }

  if (positionals.length > 0) {
    throw new HooversionError(`Unexpected positional argument: ${positionals[0]}`);
  }

  return { values, booleans, positionals };
}

function printHelp(): void {
  console.log(`hooversion

Usage:
  hooversion init [--force] [--no-workflow] [--action-owner-repo <owner/repo>] [--action-ref <ref>] [--hooversion-version <version>]
  hooversion lint --last
  hooversion lint --from <ref> [--to <ref>]
  hooversion lint --edit <commit-msg-file>
  hooversion plan [--config <path>]
  hooversion release [--dry-run] [--no-push] [--no-github] [--config <path>]
  hooversion doctor [--config <path>]
  hooversion app
`);
}

function printVersion(): void {
  const packagePath = new URL("../package.json", import.meta.url);
  if (existsSync(packagePath)) {
    const json = JSON.parse(readFileSync(packagePath, "utf8")) as { version?: string; name?: string };
    console.log(`${json.name ?? "hooversion"} ${json.version ?? "unknown"}`);
  } else {
    console.log(`${basename(process.argv[1] ?? "hooversion")} unknown`);
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
