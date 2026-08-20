import assert from "node:assert/strict";
import path from "node:path";
import test from "node:test";

import { bundledServerName, resolveServerCommand } from "../serverPath";

test("uses the platform-specific bundled server when no path is configured", () => {
  assert.equal(bundledServerName("linux"), "infralang-ls");
  assert.equal(bundledServerName("win32"), "infralang-ls.exe");
  assert.equal(
    resolveServerCommand("/extension", "  ", "linux"),
    path.join("/extension", "bin", "infralang-ls"),
  );
});

test("preserves configured commands and expands home-relative paths", () => {
  assert.equal(
    resolveServerCommand("/extension", " infralang-ls ", "linux", "/home/test"),
    "infralang-ls",
  );
  assert.equal(
    resolveServerCommand(
      "/extension",
      "~/tools/infralang-ls",
      "linux",
      "/home/test",
    ),
    path.join("/home/test", "tools/infralang-ls"),
  );
});
