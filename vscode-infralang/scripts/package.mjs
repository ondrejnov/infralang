import process from "node:process";
import { spawnSync } from "node:child_process";
import { readFile, rm } from "node:fs/promises";
import path from "node:path";
import { fileURLToPath } from "node:url";

const extensionRoot = path.resolve(
  path.dirname(fileURLToPath(import.meta.url)),
  "..",
);
const supportedTargets = [
  "win32-x64",
  "win32-arm64",
  "linux-x64",
  "linux-arm64",
  "linux-armhf",
  "alpine-x64",
  "alpine-arm64",
  "darwin-x64",
  "darwin-arm64",
];
const hostTarget = {
  win32: { x64: "win32-x64", arm64: "win32-arm64" },
  linux: { x64: "linux-x64", arm64: "linux-arm64", arm: "linux-armhf" },
  darwin: { x64: "darwin-x64", arm64: "darwin-arm64" },
}[process.platform]?.[process.arch];

const targetIndex = process.argv.indexOf("--target");
const requestedTarget =
  targetIndex >= 0 ? process.argv[targetIndex + 1] : undefined;
const targets = process.argv.includes("--all")
  ? supportedTargets
  : [requestedTarget ?? hostTarget];
if (
  targets.some(
    (target) => target === undefined || !supportedTargets.includes(target),
  )
) {
  throw new Error(
    `Unsupported VS Code target. Use one of: ${supportedTargets.join(", ")}`,
  );
}

const manifest = JSON.parse(
  await readFile(path.join(extensionRoot, "package.json"), "utf8"),
);
await rm(
  path.join(extensionRoot, `vscode-infralang-${manifest.version}.vsix`),
  {
    force: true,
  },
);
for (const target of targets) {
  run(process.execPath, ["scripts/build-server.mjs", "--target", target]);
  run("vsce", [
    "package",
    "--target",
    target,
    "--out",
    `vscode-infralang-${manifest.version}-${target}.vsix`,
  ]);
}

function run(command, args) {
  const result = spawnSync(command, args, {
    cwd: extensionRoot,
    stdio: "inherit",
  });
  if (result.error !== undefined) {
    throw result.error;
  }
  if (result.status !== 0) {
    process.exit(result.status ?? 1);
  }
}
