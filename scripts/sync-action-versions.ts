import { readFileSync, writeFileSync } from "node:fs";

const packageJson = JSON.parse(readFileSync("package.json", "utf8")) as { version: string };
const version = packageJson.version;
const files = [
  "actions/setup/action.yml",
  "actions/lint/action.yml",
  "actions/release/action.yml",
  "actions/README.md",
  "README.md",
];

for (const file of files) {
  let text = readFileSync(file, "utf8");
  if (file.endsWith("action.yml")) {
    text = updateActionVersionDefault(text, version);
  }
  text = text.replace(/version: \d+\.\d+\.\d+/g, `version: ${version}`);
  text = text.replace(/hooversion\/actions\/([a-z-]+)@v\d+\.\d+\.\d+/g, `hooversion/actions/$1@v${version}`);
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
