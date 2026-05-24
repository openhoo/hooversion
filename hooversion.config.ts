export default {
  branches: ["main"],
  packages: [
    {
      name: "@openhoo/hooversion",
      path: ".",
      type: "node",
      manifest: "package.json",
      changelog: "CHANGELOG.md",
      scopes: ["hooversion"],
      dependencies: [],
    },
  ],
  hooks: {
    afterVersion: ["bun install --lockfile-only", "bun run sync:actions", "bun run build"],
  },
  github: {
    releases: true,
  },
};
