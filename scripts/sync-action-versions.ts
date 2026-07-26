import { existsSync, readFileSync, readdirSync, writeFileSync } from "node:fs";
import { join } from "node:path";

const packageJson = JSON.parse(readFileSync("package.json", "utf8")) as { version: string };
const version = packageJson.version;
const actionMetadataFiles = readdirSync("actions", { withFileTypes: true }).flatMap((entry) => {
  if (!entry.isDirectory()) return [];

  return ["action.yml", "action.yaml"]
    .map((name) => join("actions", entry.name, name))
    .filter(existsSync);
});
const files = [...actionMetadataFiles, "actions/README.md", "README.md"];

for (const file of files) {
  let text = readFileSync(file, "utf8");
  if (/action\.ya?ml$/.test(file)) {
    text = updateActionVersionDefault(text, version);
  }
  text = text.replace(/version: \d+\.\d+\.\d+/g, `version: ${version}`);
  text = text.replace(
    /openhoo\/hooversion\/actions\/([a-z0-9-]+)@v\d+\.\d+\.\d+/gi,
    `openhoo/hooversion/actions/$1@v${version}`,
  );
  writeFileSync(file, text);
}

function updateActionVersionDefault(text: string, version: string): string {
  const lines = text.split("\n");
  let inVersionInput = false;
  return lines
    .map((line) => {
      if (line === "  version:") {
        inVersionInput = true;
        return line;
      }
      if (inVersionInput && /^  [a-z-]+:/.test(line)) {
        inVersionInput = false;
      }
      if (inVersionInput && line.trim().startsWith("default:")) {
        inVersionInput = false;
        return `    default: "${version}"`;
      }
      return line;
    })
    .join("\n");
}
