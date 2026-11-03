#!/usr/bin/env node
// check-docker-context.mjs — the Docker image must be buildable from exactly the files the
// Dockerfile copies into it.
//
// Why this exists: stage 2 of the Dockerfile COPYs cmd/, internal/ and pkg/ — and omitted
// api/, while internal/server/handlers_api_docs.go imports github.com/forgec2/forgec2/api
// (a package that //go:embed's openapi.yaml so the docs endpoint works in packaged
// releases). `docker build` failed with "no required module provides package
// github.com/forgec2/forgec2/api" while `go build ./...` stayed green on every real
// checkout, because on a checkout api/ EXISTS. Only the image context was missing it.
// The same archive-vs-embed mismatch is what check-runtime-paths.mjs documents; this gate
// closes the Docker half of that story.
//
// Rule: take the set of top-level directories the Dockerfile COPYs from the build context,
// walk the production Go files inside them (test files and //go:build ignore files are not
// part of the image build), and follow imports of IN-MODULE packages transitively. Every
// top-level directory the build graph needs must be in the COPY list, and none of them may
// be excluded by .dockerignore.
//
// Usage: node scripts/check-docker-context.mjs
import { readFileSync, readdirSync, existsSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");

function read(file) {
  return readFileSync(join(root, file), "utf8");
}

// 1. Module path, from go.mod.
const goMod = read("go.mod");
const moduleMatch = goMod.match(/^module\s+(\S+)/m);
if (!moduleMatch) {
  console.error("check-docker-context: no module line in go.mod");
  process.exit(1);
}
const modulePath = moduleMatch[1];

// 2. Top-level directories the Dockerfile COPYs from the context (COPY --from= lines are
// image-to-image copies and irrelevant to the build context; single-file COPYs like
// go.mod/go.sum are checked by the build itself).
const dockerfile = read("Dockerfile");
const copied = new Set();
for (const line of dockerfile.split(/\r?\n/)) {
  const m = line.match(/^COPY\s+(?:--\S+(?:=\S+)?\s+)*(\S+)\s+\S+/);
  if (!m || line.includes("--from=")) continue;
  const src = m[1].replace(/\/+$/, "");
  if (!src.includes("/") && !src.includes(".")) copied.add(src);
}
if (copied.size === 0) {
  console.error("check-docker-context: no context COPY directives found in Dockerfile");
  process.exit(1);
}

// 3. .dockerignore patterns that would exclude a directory wholesale (exact or dir/ lines;
// globs beyond that are out of scope and reported only for the wholesale case).
const dockerignoreExists = existsSync(join(root, ".dockerignore"));
const ignoredDirs = new Set();
if (dockerignoreExists) {
  for (const line of read(".dockerignore").split(/\r?\n/)) {
    const p = line.trim();
    if (!p || p.startsWith("#")) continue;
    if (/^[A-Za-z0-9_.-]+\/?$/.test(p) && !p.includes(".")) ignoredDirs.add(p.replace(/\/+$/, ""));
  }
}

// 4. Walk a directory for production Go files.
//
// node_modules/.git/data are pruned: "frontend" is in the COPY list (stage 1 builds the
// console), so without this the walk would descend into an installed dependency tree on
// any developer checkout — slow, and looking for Go files that cannot be part of the
// server build.
const PRUNE = new Set(["node_modules", ".git", "data", "dist", "out"]);
function goFiles(dir, out = []) {
  let entries;
  try {
    entries = readdirSync(join(root, dir), { withFileTypes: true });
  } catch {
    return out;
  }
  for (const e of entries) {
    if (e.isDirectory() && PRUNE.has(e.name)) continue;
    const full = `${dir}/${e.name}`;
    if (e.isDirectory()) goFiles(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

// 5. Imports of in-module packages, from one file. Files behind //go:build ignore are not
// compiled into the image (they are standalone helpers), so their imports must not widen
// the requirement set.
const importBlockRe = /import\s*\(([\s\S]*?)\)/g;
const singleImportRe = /^\s*import\s+(?:\w+\s+)?"([^"]+)"/gm;
function inModuleTops(file) {
  const src = read(file);
  const head = src.split("\n", 12).join("\n");
  if (/^\s*\/\/go:build.*\bignore\b/m.test(head) || /^\s*\/\/ \+build.*\bignore\b/m.test(head)) {
    return [];
  }
  const tops = new Set();
  const paths = [];
  for (const m of src.matchAll(importBlockRe)) paths.push(...[...m[1].matchAll(/"([^"]+)"/g)].map((x) => x[1]));
  for (const m of src.matchAll(singleImportRe)) paths.push(m[1]);
  for (const p of paths) {
    if (p === modulePath) continue;
    if (!p.startsWith(modulePath + "/")) continue;
    tops.add(p.slice(modulePath.length + 1).split("/")[0]);
  }
  return [...tops];
}

// 6. Transitive fixpoint from the copied set: any in-module top-level directory the build
// graph reaches must itself be copied.
const queue = [...copied];
const visitedDirs = new Set();
const missingCopy = new Set();
const blockedByIgnore = new Set();
while (queue.length > 0) {
  const top = queue.shift();
  if (visitedDirs.has(top)) continue;
  visitedDirs.add(top);
  if (!copied.has(top)) missingCopy.add(top);
  if (ignoredDirs.has(top)) blockedByIgnore.add(top);
  for (const file of goFiles(top)) {
    for (const dep of inModuleTops(file)) {
      if (!visitedDirs.has(dep)) queue.push(dep);
    }
  }
}

if (missingCopy.size > 0) {
  console.error(
    `check-docker-context: the image build graph imports top-level package(s) the Dockerfile never COPYs: ${[...missingCopy].sort().join(", ")}`,
  );
  console.error(
    "  go build ./... cannot see this: on any checkout the directory exists, and only the image omits it.",
  );
  process.exit(1);
}
if (blockedByIgnore.size > 0) {
  console.error(
    `check-docker-context: .dockerignore excludes top-level package(s) the image needs: ${[...blockedByIgnore].sort().join(", ")}`,
  );
  process.exit(1);
}
console.log(
  `check-docker-context OK — build graph reaches ${visitedDirs.size} top-level dir(s), all copied`,
);
