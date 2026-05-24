import { describe, expect, it } from "bun:test";
import { lintCommit, parseCommit } from "../src/commit";

describe("commit parsing", () => {
  it("maps conventional commits to release types", () => {
    expect(parseCommit(raw("feat(ui): add theme switcher")).releaseType).toBe("minor");
    expect(parseCommit(raw("fix: handle hotplug failure")).releaseType).toBe("patch");
    expect(parseCommit(raw("perf(core): cache results")).releaseType).toBe("patch");
    expect(parseCommit(raw("docs: update readme")).releaseType).toBeUndefined();
  });

  it("detects breaking changes from bang and footer", () => {
    expect(parseCommit(raw("feat!: change config format")).releaseType).toBe("major");
    expect(parseCommit(raw("feat(api): change payload", "BREAKING CHANGE: payload is now nested")).releaseType).toBe(
      "major",
    );
  });

  it("lints invalid headers", () => {
    expect(lintCommit(raw("Add printer bridge"))).toHaveLength(1);
    expect(lintCommit(raw("feat: add printer bridge"))).toHaveLength(0);
  });
});

function raw(subject: string, body = "") {
  return { hash: "abc1234", subject, body, files: [] };
}
