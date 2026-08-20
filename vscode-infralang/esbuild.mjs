import process from "node:process";

import * as esbuild from "esbuild";

const production = process.argv.includes("--production");
const watch = process.argv.includes("--watch");

const context = await esbuild.context({
  entryPoints: ["src/extension.ts"],
  outfile: "dist/extension.js",
  bundle: true,
  external: ["vscode"],
  format: "cjs",
  logLevel: "info",
  minify: production,
  platform: "node",
  sourcemap: !production,
  sourcesContent: false,
  target: "node20",
});

if (watch) {
  await context.watch();
  console.log("Watching InfraLang extension sources...");
} else {
  await context.rebuild();
  await context.dispose();
}
