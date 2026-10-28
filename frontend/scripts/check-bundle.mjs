#!/usr/bin/env node
// check-bundle.mjs — fail the check when the static client bundle grows
// beyond a sanity ceiling, or when the first-paint critical path does.
//
// Two budgets, because they catch different regressions:
//
//   1. TOTAL (out/**.js + out/**.css) — catches accidental weight growth, e.g.
//      an un-split import of a large chart/graph lib.
//   2. CRITICAL PATH (index.html's entry chunk + everything it statically
//      imports, transitively) — catches the failure the total budget cannot
//      see: a route chunk that stops being lazily fetched.
//
// The second budget exists because this app shipped 928 kB of route JS inside
// the first-paint path (route-agents-list, route-agents-detail, route-ai,
// route-generate, route-credentials) while the TOTAL budget stayed comfortably
// green at 3.92 MB / 5.5 MB. Every operator downloaded those routes before
// painting anything, and nothing failed.
//
// Measured 2026-10-15, before and after dropping the page-directory
// manualChunks buckets:
//   before: critical path 1682 kB (13 chunks), of which 906 kB was route-* chunk JS
//           (route-agents-list/detail, route-ai, route-generate,
//           route-credentials) that no first-paint screen uses; total 3.92 MB
//   after:  critical path 1076 kB (8 chunks), zero route-* chunks; total 3.95 MB
// At the 1076 kB mark the remaining ~694 kB was vendor (baseui 201, react
// 194, router 89, zod 65, icons 57, misc 55, sonner 33) plus the ~383 kB
// entry, which carries the English dictionary. That is the floor this change
// is not about; lower it by trimming vendor deps, not by raising this number.
//
// Batch S then took the critical path to 968 kB (7 chunks): the lazy login
// route drops zod and the base-ui vendor chunk is split by measured
// reachability; batch T held it at 968 kB with total 3.98 MB / 190 files
// (measurements and commands in docs/FRONTEND_PERF.md).
import { readdirSync, readFileSync, statSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";

const TOTAL_LIMIT_BYTES = 5.5 * 1024 * 1024;
// Tightened from 1200 KiB after batches S/T held the critical path at 968 kB, which left ~122 kB.
// Re-measured 2026-10-28: the critical path is down to 798 kB, so that same 1090 kB limit now left
// 292 kB - enough for a 200 kB route chunk to start leaking again and still pass, which is exactly
// the regression this budget exists to catch. Set to 900 kB: ~100 kB of ordinary entry/vendor
// headroom, and a re-leak of a few hundred kB trips it.
const CRITICAL_PATH_LIMIT_BYTES = 900 * 1024;
const staticDir = join(fileURLToPath(new URL(".", import.meta.url)), "..", "out");

function walk(dir, out) {
  const stack = [dir];
  while (stack.length) {
    const cur = stack.pop();
    for (const entry of readdirSync(cur, { withFileTypes: true })) {
      const abs = join(cur, entry.name);
      if (entry.isDirectory()) stack.push(abs);
      else out.push(abs);
    }
  }
  return out;
}

let allFiles;
let indexHtml;
try {
  allFiles = walk(staticDir, []);
  indexHtml = readFileSync(join(staticDir, "index.html"), "utf8");
} catch (err) {
  console.error("check-bundle: cannot read out/:", err.message);
  console.error("Run `npm run build` first.");
  process.exit(1);
}

// ── 1. Total JS + CSS ────────────────────────────────────────────────────────
let total = 0;
let files = 0;
for (const f of allFiles) {
  if (f.endsWith(".js") || f.endsWith(".css")) {
    total += statSync(f).size;
    files += 1;
  }
}

// ── 2. Critical path: entry chunk + transitive static imports ───────────────
// Only `import ... from "./x.js"` counts. A dynamic `import("./x.js")` is the
// whole point of splitting and must NOT be charged to first paint.
const entryMatch = indexHtml.match(/assets\/(index-[\w.-]+\.js)/);
if (!entryMatch) {
  console.error("check-bundle: could not find the entry chunk in index.html.");
  process.exit(1);
}

const critical = new Map(); // filename -> bytes
const queue = [entryMatch[1]];
const unresolved = [];
// index.html's own <script src> and <link rel=modulepreload> are fetched for
// first paint regardless of whether the entry chunk imports them, so they are
// part of the critical path too. Counting only the entry's static imports
// understates it (vendor chunks preload-declared in HTML were being missed).
for (const m of indexHtml.matchAll(/(?:src|href)="\/assets\/([\w.-]+\.js)"/g)) {
  queue.push(m[1]);
}
while (queue.length) {
  const name = queue.pop();
  if (critical.has(name)) continue;
  const abs = join(staticDir, "assets", name);
  let src;
  try {
    src = readFileSync(abs, "utf8");
  } catch {
    unresolved.push(name);
    continue;
  }
  critical.set(name, statSync(abs).size);
  // Static imports only: `from"./x.js"` / `from "./x.js"`. Vite emits these
  // without the space; both forms are matched.
  for (const m of src.matchAll(/from\s*"\.\/([\w.-]+\.js)"/g)) {
    queue.push(m[1]);
  }
}
const criticalBytes = [...critical.values()].reduce((a, b) => a + b, 0);

const mb = (b) => `${(b / 1024 / 1024).toFixed(2)}MB`;
const kb = (b) => `${Math.round(b / 1024)}kB`;

let failed = false;
if (total > TOTAL_LIMIT_BYTES) {
  console.error(`FAIL: static bundle is ${mb(total)} (${files} files) — exceeds ${mb(TOTAL_LIMIT_BYTES)}.`);
  console.error("Split lazy imports or remove heavy deps before raising the threshold.");
  failed = true;
}
if (unresolved.length) {
  console.error(`FAIL: entry references ${unresolved.length} missing chunk(s): ${unresolved.join(", ")}.`);
  failed = true;
}
if (criticalBytes > CRITICAL_PATH_LIMIT_BYTES) {
  console.error(`FAIL: first-paint critical path is ${kb(criticalBytes)} — exceeds ${kb(CRITICAL_PATH_LIMIT_BYTES)}.`);
  console.error("Chunks fetched before first paint:");
  for (const [name, size] of [...critical.entries()].sort((a, b) => b[1] - a[1])) {
    console.error(`  ${kb(size).padStart(8)}  ${name}`);
  }
  console.error(
    "A page or shared component is being pulled in statically. Keep it behind `lazy(() => import(...))`,",
  );
  console.error("and do not reintroduce directory-level manualChunks buckets in vite.config.ts.");
  failed = true;
}
if (failed) process.exit(1);

const routeChunks = [...critical.keys()].filter((n) => /^route-/.test(n));
console.log(
  `OK: total ${mb(total)} (${files} files) under ${mb(TOTAL_LIMIT_BYTES)}; ` +
    `critical path ${kb(criticalBytes)} (${critical.size} chunks, ${routeChunks.length} route) under ${kb(CRITICAL_PATH_LIMIT_BYTES)}`,
);