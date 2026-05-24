export default {
  branches: ["main"],
  packages: [
    {
      name: "hoot-plugin-sdk",
      path: "crates/hoot-plugin-sdk",
      type: "rust",
      manifest: "crates/hoot-plugin-sdk/Cargo.toml",
      changelog: "crates/hoot-plugin-sdk/CHANGELOG.md",
      scopes: ["hoot-plugin-sdk", "plugin-sdk"],
      dependencies: [],
    },
    {
      name: "hoot-core",
      path: "crates/hoot-core",
      type: "rust",
      manifest: "crates/hoot-core/Cargo.toml",
      changelog: "crates/hoot-core/CHANGELOG.md",
      scopes: ["hoot-core", "core"],
      dependencies: ["hoot-plugin-sdk"],
    },
    {
      name: "hoot",
      path: ".",
      type: "rust",
      manifest: "Cargo.toml",
      changelog: "CHANGELOG.md",
      scopes: ["hoot"],
      dependencies: ["hoot-core"],
    },
  ],
  github: {
    releases: true,
  },
};
