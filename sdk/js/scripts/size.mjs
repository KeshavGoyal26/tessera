// Bundle-size budget: bundles dist/index.js (SDK plus @bufbuild/protobuf runtime) as a
// minified ESM file with every export kept, gzips it and fails above the budget.
// Run `npm run build` first. Uses rolldown, which ships with vitest.
import { execFileSync } from "node:child_process";
import { mkdtempSync, readFileSync } from "node:fs";
import { tmpdir } from "node:os";
import { join } from "node:path";
import { gzipSync } from "node:zlib";

const BUDGET_KB = 60;
const dir = mkdtempSync(join(tmpdir(), "tessera-size-"));
const out = join(dir, "bundle.js");
execFileSync("npx", ["rolldown", "dist/index.js", "--format", "esm", "--minify", "--no-treeshake", "--file", out], { stdio: "inherit" });
const raw = readFileSync(out);
const gz = gzipSync(raw, { level: 9 }).length;
console.log(`@tessera/client (worst case, nothing tree-shaken): ${(raw.length / 1024).toFixed(1)} kB min, ${(gz / 1024).toFixed(1)} kB gzip (budget ${BUDGET_KB} kB)`);
if (gz / 1024 > BUDGET_KB) {
  console.error("bundle size budget exceeded");
  process.exit(1);
}
