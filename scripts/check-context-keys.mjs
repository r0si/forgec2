#!/usr/bin/env node
// check-context-keys.mjs — fail when a handler reads a gin context key that the
// production code never sets.
//
// Four handlers once read c.GetString("username") while the auth middleware
// only sets "user": the value was silently empty, so BOF/script attribution and
// the scripting caller identity were always blank. No compiler or test caught
// it because both sides are just strings.
//
// This gate collects every key written with c.Set("literal") (or a key constant
// that resolves to a literal) across internal/**/*.go — test files excluded,
// since a test setting a key proves nothing about production — and every key
// read with c.Get / c.GetString / c.MustGet. A read with no writer fails.
//
// Usage:
//   node scripts/check-context-keys.mjs            # gate
//   node scripts/check-context-keys.mjs --list     # print the key table
import fs from "node:fs";
import path from "node:path";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const internalDir = join(root, "internal");
const listOnly = process.argv.includes("--list");

// Keys written by code outside internal/**/*.go (or by a mechanism this scan
// cannot see), with the reason.
const ALLOWLIST = new Map([
  // gin itself never sets a key; anything here would be a bug, so keep empty
  // on purpose and document the escape hatch.
]);

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

const files = walk(internalDir);

// Resolve `const someKey = "value"` / `const someKey string = "value"` so a key
// written through a constant still counts as a writer.
const constants = new Map();
for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  for (const m of src.matchAll(/const\s+([A-Za-z_]\w*)\s*(?:string\s*)?=\s*"([^"]+)"/g)) {
    constants.set(m[1], m[2]);
  }
}

const resolve = (token) => {
  const t = token.trim();
  if (t.startsWith('"')) return t.slice(1, t.indexOf('"', 1));
  return constants.get(t) ?? null;
};

const sets = new Map();
const gets = new Map();
const dynamicWriters = new Set();

for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  for (const m of src.matchAll(/\bc\.Set\(\s*([^,]+?)\s*,/g)) {
    const key = resolve(m[1]);
    if (key === null) dynamicWriters.add(m[1].trim());
    else sets.set(key, (sets.get(key) || 0) + 1);
  }
  for (const m of src.matchAll(/\bc\.(?:Get|GetString|MustGet)\(\s*([^)]+?)\s*\)/g)) {
    const key = resolve(m[1]);
    if (key !== null) gets.set(key, (gets.get(key) || 0) + 1);
  }
}

if (listOnly) {
  console.log("set keys:", [...sets.entries()].map(([k, n]) => `${k}(${n})`).join(", "));
  console.log("read keys:", [...gets.entries()].map(([k, n]) => `${k}(${n})`).join(", "));
  console.log("dynamic writers:", [...dynamicWriters].join(", ") || "(none)");
  process.exit(0);
}

const missing = [];
for (const [key, reads] of [...gets.entries()].sort((a, b) => b[1] - a[1])) {
  if (sets.has(key) || ALLOWLIST.has(key)) continue;
  missing.push({ key, reads });
}

if (missing.length === 0) {
  const dynamic = dynamicWriters.size ? ` (${dynamicWriters.size} dynamic writer(s) ignored)` : "";
  console.log(`context-keys OK — ${gets.size} keys read, all written by production code${dynamic}.`);
  process.exit(0);
}

console.error("context-keys FAILED — these gin context keys are read but never set outside tests:");
for (const m of missing) console.error(`  - "${m.key}" (${m.reads} read(s))`);
console.error("\nFix: have the middleware/handler set the key, or read the correct existing key.");
console.error("A genuinely external key belongs in ALLOWLIST with a reason in " + path.relative(root, fileURLToPath(import.meta.url)) + ".");
process.exit(1);
