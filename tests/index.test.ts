import { describe, expect, it } from "bun:test";
import { lintCommit, lintCommits, parseCommit } from "../src/index";
import type { RawCommit } from "../src/types";

describe("public commit policy API", () => {
  it("supports configured allowed and releasing types", () => {
    const raw = commit("docs: publish guide");
    const policy = { allowedTypes: ["feat", "docs"], releaseTypes: { docs: "patch" as const } };

    expect(lintCommit(raw, policy)).toEqual([]);
    expect(parseCommit(raw, policy).releaseType).toBe("patch");
  });

  it("shares parser/linter behavior for batch consumers", () => {
    const commits = [commit("fix: repair"), commit("not conventional")];
    const policy = { allowedTypes: ["fix"] };

    expect(lintCommits(commits, policy)).toEqual(commits.flatMap((raw) => lintCommit(raw, policy)));
  });

  it("matches CLI lint semantics for valid and invalid commit messages", () => {
    const valid = commit("feat: add parity check");
    const invalid = commit("Add parity check");

    expect(lintCommit(valid)).toEqual([]);
    expect(lintCommit(invalid)).toHaveLength(1);
  });
});

function commit(subject: string): RawCommit {
  return { hash: "abc1234", subject, body: "", files: [] };
}
