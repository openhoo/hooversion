#!/usr/bin/env bun
import { existsSync, readFileSync } from "node:fs";
import { basename } from "node:path";
import { HooversionError } from "./errors";
import { lintCommit, parseCommit } from "./commit";
import { detectPackages, findConfigPath, loadConfig, writeDefaultConfig } from "./config";
import { getCommits, getLastCommit } from "./git";
import { createReleasePlan } from "./plan";
import { executeRelease, validatePlan } from "./release";
import { runDoctor } from "./doctor";
import { writeGitHubWorkflow } from "./workflow";
import type { ParsedCommit, ReleasePlan } from "./types";

interface CliFlags {
  values: Map<string, string>;
  booleans: Set<string>;
  positionals: string[];
}

async function main(argv = process.argv.slice(2)): Promise<void> {
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

async function initCommand(cwd: string, flags: CliFlags): Promise<void> {
  const force = flags.booleans.has("force");
  if (findConfigPath(cwd) && !force) {
    throw new HooversionError("Hooversion config already exists. Use --force to overwrite.");
  }

  const packages = detectPackages(cwd);
  const configPath = writeDefaultConfig(cwd, packages);
  const workflowPath = flags.booleans.has("no-workflow")
    ? undefined
    : writeGitHubWorkflow(cwd, {
        actionOwnerRepo: flags.values.get("action-owner-repo"),
        actionRef: flags.values.get("action-ref"),
        hooversionVersion: flags.values.get("hooversion-version"),
      });
  console.log(`Wrote ${configPath}`);
  if (workflowPath) console.log(`Wrote ${workflowPath}`);
}

function lintCommand(cwd: string, flags: CliFlags): void {
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

async function planCommand(cwd: string, flags: CliFlags): Promise<void> {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  printPlan(plan);
}

async function releaseCommand(cwd: string, flags: CliFlags): Promise<void> {
  const config = await loadConfig(cwd, flags.values.get("config"));
  const plan = createReleasePlan(cwd, config);
  const dryRun = flags.booleans.has("dry-run");
  validatePlan(cwd, config, plan);
  printPlan(plan);
  await executeRelease(cwd, config, plan, {
    dryRun,
    push: flags.booleans.has("no-push") ? false : undefined,
    github: flags.booleans.has("no-github") ? false : undefined,
  });
  if (dryRun) {
    console.log("Dry run complete; no files, commits, tags, or releases were created.");
  } else if (plan.releases.length > 0) {
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
  const editPath = flags.values.get("edit");
  if (editPath) {
    return [
      {
        hash: "",
        subject: readFileSync(editPath, "utf8").split(/\r?\n/)[0] ?? "",
        body: readFileSync(editPath, "utf8").split(/\r?\n/).slice(1).join("\n"),
        files: [],
      },
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

function printPlan(plan: ReleasePlan): void {
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
  const bang = parsed.breaking ? "!" : "";
  return `${commit.hash.slice(0, 7)} ${parsed.type}${scope}${bang}: ${parsed.description}`;
}

function parseFlags(args: string[]): CliFlags {
  const values = new Map<string, string>();
  const booleans = new Set<string>();
  const positionals: string[] = [];

  for (let index = 0; index < args.length; index += 1) {
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

function expectsValue(name: string): boolean {
  return ["action-owner-repo", "action-ref", "config", "edit", "from", "hooversion-version", "to"].includes(name);
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
