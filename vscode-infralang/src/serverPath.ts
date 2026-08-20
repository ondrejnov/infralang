import os from "node:os";
import path from "node:path";

export function bundledServerName(
  platform: NodeJS.Platform = process.platform,
): string {
  return platform === "win32" ? "infralang-ls.exe" : "infralang-ls";
}

export function resolveServerCommand(
  extensionPath: string,
  configuredPath: string,
  platform: NodeJS.Platform = process.platform,
  homeDirectory: string = os.homedir(),
): string {
  const configured = configuredPath.trim();
  if (configured === "") {
    return path.join(extensionPath, "bin", bundledServerName(platform));
  }
  if (configured === "~") {
    return homeDirectory;
  }
  if (configured.startsWith("~/") || configured.startsWith("~\\")) {
    return path.join(homeDirectory, configured.slice(2));
  }
  return configured;
}
