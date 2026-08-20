import { mkdir, rm } from "node:fs/promises";
import path from "node:path";
import process from "node:process";
import { spawnSync } from "node:child_process";
import { fileURLToPath } from "node:url";

const extensionRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const targets = {
  "win32-x64": ["windows", "amd64"],
  "win32-arm64": ["windows", "arm64"],
  "linux-x64": ["linux", "amd64"],
  "linux-arm64": ["linux", "arm64"],
  "linux-armhf": ["linux", "arm"],
  "alpine-x64": ["linux", "amd64"],
  "alpine-arm64": ["linux", "arm64"],
  "darwin-x64": ["darwin", "amd64"],
  "darwin-arm64": ["darwin", "arm64"],
};

const targetIndex = process.argv.indexOf("--target");
const target = targetIndex >= 0 ? process.argv[targetIndex + 1] : undefined;
let goos = { win32: "windows", linux: "linux", darwin: "darwin" }[
  process.platform
];
let goarch = { x64: "amd64", arm64: "arm64", arm: "arm" }[process.arch];
if (target !== undefined) {
  if (targets[target] === undefined) {
    throw new Error(`Unsupported VS Code target: ${target}`);
  }
  [goos, goarch] = targets[target];
}
if (goos === undefined || goarch === undefined) {
  throw new Error(`Unsupported host: ${process.platform}-${process.arch}`);
}

const outputName = goos === "windows" ? "infralang-ls.exe" : "infralang-ls";
const outputPath = path.join(extensionRoot, "bin", outputName);

await mkdir(path.dirname(outputPath), { recursive: true });
await Promise.all([
  rm(path.join(extensionRoot, "bin", "infralang-ls"), { force: true }),
  rm(path.join(extensionRoot, "bin", "infralang-ls.exe"), { force: true }),
]);

const result = spawnSync("go", ["build", "-o", outputPath, "./server"], {
  cwd: extensionRoot,
  env: { ...process.env, CGO_ENABLED: "0", GOOS: goos, GOARCH: goarch },
  stdio: "inherit",
});

if (result.error !== undefined) {
  throw result.error;
}
process.exit(result.status ?? 1);
