#!/usr/bin/env node
// check-etag-impl.mjs - the If-None-Match comparison has exactly one implementation.
//
// Rounds 3 and 4 added conditional requests to the audit export and the task CSV export, and each grew
// its own copy of the "parse the header and compare tags" loop. That is the same shape that produced
// check:guarded-clients: rounds 232/233 fixed a zero-timeout hole in ONE of two copies of an HTTP client
// wrapper, and the second copy kept the bug. A comparison looks small enough to duplicate safely, which
// is exactly why the duplicate happens.
//
// The rule is mechanical: outside etag.go, no server file may read the If-None-Match header. Revalidation
// goes through ifNoneMatch(), and a second reader is a second place to get the wildcard, the weak-tag
// form or the comma list wrong.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");

// file -> reason. Empty: the header belongs to one helper.
const ALLOW = new Map([]);

// The DUPLICATE MARKER is the comparison idiom itself: stripping the weak-validator prefix. A file may
// read the header (the static middleware does, then delegates) but it may not re-implement the compare.
const HEADER = /strings\.TrimPrefix\([^)]*"W\/"\)/;

const problems = [];
let readers = 0;

for (const entry of readdirSync(serverDir)) {
  if (!entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
  if (entry === "etag.go") continue;
  if (ALLOW.has(entry)) continue;
  const source = readFileSync(join(serverDir, entry), "utf8");
  if (!HEADER.test(source)) continue;
  readers++;
  const line = source.split("\n").findIndex((text) => HEADER.test(text)) + 1;
  problems.push(`${entry}:${line} re-implements the ETag comparison; use etagMatches()/ifNoneMatch() from etag.go`);
}

if (problems.length > 0) {
  console.error("etag-impl: the If-None-Match comparison must have one implementation");
  for (const problem of problems) console.error(`  ${problem}`);
  console.error("\nCall the shared ifNoneMatch() helper instead of parsing the header again.");
  process.exit(1);
}

console.log(
  `etag-impl OK - ${readers} duplicate reader(s); revalidation goes through ifNoneMatch()` +
    (ALLOW.size ? ` (${ALLOW.size} allowed)` : " (allowlist empty)"),
);
