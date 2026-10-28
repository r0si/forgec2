#!/usr/bin/env node
// check-discarded-results.mjs — a delivery that failed must not be discarded.
//
// Four rounds in a row found the same shape: a send/dispatch call whose error was thrown away, so a
// feature reported success while nothing happened.
//   - automation script:      engine.Execute(...)            result unused (no log, no audit);
//   - notification dispatch:  _ = s.sendNotificationRoute(…) the row existed, the channel never got it;
//   - automation webhook:     resp status never inspected     a 500 looked exactly like a 200.
// A discarded error is invisible at the call site and invisible in the UI, so it needs a gate rather
// than another review.
//
// Scope is deliberately narrow: `_ = <receiver>.<deliveryVerb>…(` where the verb says the call talks to
// something outside the process, plus a BARE call (no assignment) to a named delivery function that
// returns an error. `_ = json.Unmarshal(…)` and friends are not matched, and an intentional ignore
// carries a reason in ALLOW.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");

// Verbs that mean "this reaches something outside the process".
const DELIVERY_VERB = /\.(send|deliver|dispatch|notify|push|post|webhook|execute)[A-Z]\w*\(/;

// Named functions that return an error and are ALWAYS delivery: a bare call to one of these drops the
// result just as surely as "_ = " does.
// Only functions that RETURN AN ERROR belong here: a bare call to a void function discards nothing.
// executeWebhook was listed here first and produced a false positive the moment it was wrapped in a
// recovering goroutine - the gate has to be precise or it teaches people to ignore it.
const BARE_FORBIDDEN = ["sendNotificationRoute(", "deliverNotificationSleep("];

// Intentional discards, each with the reason it cannot fail in a way the operator needs to see.
// (Currently empty: every delivery site in the tree either logs or returns its failure. Note that
// allowlisting a whole FILE defeats the check for the very place the defect lived - do not.)
const ALLOW = new Map();

const problems = [];
for (const entry of readdirSync(serverDir)) {
  if (!entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
  const rel = `internal/server/${entry}`;
  if (ALLOW.has(rel)) continue;
  const lines = readFileSync(join(serverDir, entry), "utf8").split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    const trimmed = line.trim();
    if (trimmed.startsWith("//")) continue;

    // 1. Explicitly discarded result of a delivery-shaped call.
    if (/^_ = /.test(trimmed) && DELIVERY_VERB.test(trimmed)) {
      // Close/flush style methods are not delivery.
      if (/\.(close|Close|flush|Flush|stop|Stop)\(/.test(trimmed)) continue;
      problems.push(`${rel}:${i + 1}: result discarded: ${trimmed}`);
      continue;
    }

    // 2. A bare call statement to a known error-returning delivery function.
    if (/^[A-Za-z_][\w.]*\.\w+\(/.test(trimmed) && BARE_FORBIDDEN.some((needle) => trimmed.includes(needle))) {
      problems.push(`${rel}:${i + 1}: delivery result never read: ${trimmed}`);
    }
  }
}

if (problems.length > 0) {
  console.error(`check-discarded-results: ${problems.length} delivery result(s) discarded:`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: log or return the failure. A delivery that fails silently is the defect.");
  process.exit(1);
}

console.log("check-discarded-results: OK (no delivery result is discarded)");
