import { describe, expect, it } from "bun:test";
import { bumpVersion, highestReleaseType } from "../src/semver";

describe("semver", () => {
  it("bumps versions by release type", () => {
    expect(bumpVersion("1.2.3", "patch")).toBe("1.2.4");
    expect(bumpVersion("1.2.3", "minor")).toBe("1.3.0");
    expect(bumpVersion("1.2.3", "major")).toBe("2.0.0");
  });

  it("selects the highest release type", () => {
    expect(highestReleaseType(["patch", "minor"])).toBe("minor");
    expect(highestReleaseType(["patch", "major", "minor"])).toBe("major");
    expect(highestReleaseType([undefined])).toBeUndefined();
  });
});
