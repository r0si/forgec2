#!/usr/bin/env node
// check-guarded-clients.mjs — the outbound-client guard has exactly ONE implementation.
//
// ssrfSafeClient and aiSSRFClient were the same four-line construct written twice, and the second copy
// missed the timeout guard the first had been given: with a nil base the client fell back to
// http.DefaultClient, whose Timeout is zero, so a hung provider or webhook pinned the caller forever.
// Fixing one copy and not the other is exactly what happened (rounds 232 and 233), so the shape is now
// checked instead of remembered.
//
// Rule: `Timeout: base.Timeout` and `base = http.DefaultClient` may only appear in
// internal/server/guarded_client.go. Everything else routes through guardedClient(base, fallback, valid).
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");

const OWNER = "guarded_client.go";
const PATTERNS = [
  { needle: "Timeout: base.Timeout", why: "copying the base timeout must happen in guardedClient" },
  { needle: "base = http.DefaultClient", why: "the nil fallback must happen in guardedClient" },
];

const problems = [];
for (const entry of readdirSync(serverDir)) {
  if (!entry.endsWith(".go") || entry.endsWith("_test.go") || entry === OWNER) continue;
  const lines = readFileSync(join(serverDir, entry), "utf8").split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    if (line.trim().startsWith("//")) continue;
    for (const { needle, why } of PATTERNS) {
      if (line.includes(needle)) {
        problems.push(`${entry}:${i + 1}: ${line.trim()}  (${why})`);
      }
    }
  }
}

if (problems.length > 0) {
  console.error(`check-guarded-clients: ${problems.length} outbound client(s) bypass guardedClient:`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: call guardedClient(base, fallbackTimeout, validate) instead of copying the construct.");
  process.exit(1);
}

console.log("check-guarded-clients: OK (one guardedClient implementation, no copies)");
