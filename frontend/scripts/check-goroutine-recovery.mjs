#!/usr/bin/env node
// check-goroutine-recovery.mjs — a NEW goroutine in a request path must not be able to kill the process.
//
// A panic in any goroutine takes the whole server down; there is no process-wide handler in Go. A survey
// of internal/server found 63 `go …` sites with no reachable recover, so this gate does not pretend to
// fix that — it freezes the current set (LEGACY) and refuses new ones in handler files, which are the
// paths a malformed request or a misbehaving client reaches. The two sites that looked worst in the
// survey turned out to be guarded by design rather than by recover (wsReadPump/wsWritePump own their
// own handling; the AI broker select()s on a done channel so a racing unsubscribe cannot panic), which
// is why the rule is "no new ones" and not "add recover to all 63".
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");

// Existing sites, frozen at the state the gate was added. Each is reviewed when touched; the list must
// only ever shrink.
//
// A site is keyed by file + enclosing function + its zero-based occurrence in that function, NOT by line
// number. Line numbers move whenever an unrelated comment or handler above shifts, and every one of those
// moves reads as "3 new unguarded goroutines" — a false alarm that trains people to bypass the gate.
const LEGACY = new Set([
  // select()s on dst.done, so a racing unsubscribe drops instead of panicking on a closed channel.
  "handlers_ai_runs.go|publish|0",
  "handlers_ai_runs.go|startAIBackgroundRun|0",
  "handlers_ai_runs.go|executeAIBackgroundRun|0",
  "handlers_ai_runs.go|initializeAIRuns|0",
  "handlers_cookie_proxy.go|start|0",
  "handlers_cookie_proxy.go|handleConnect|0",
  "handlers_domain_front_stager.go|frontRefresh|0",
  "handlers_lportfwd.go|lportfwdConnect|0",
  "handlers_socks_relay.go|startUDPAssociate|0",
  "handlers_websocket.go|handleOperatorWS|0",
  "handlers_websocket.go|handleOperatorWS|1",
]);

const START = /^\s*go func\s*\(/;
const FUNC = /^func\s+(?:\([^)]*\)\s+)?(\w+)/;

const problems = [];
for (const entry of readdirSync(serverDir)) {
  if (!entry.startsWith("handlers_") || !entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
  const lines = readFileSync(join(serverDir, entry), "utf8").split("\n");
  let fn = "(none)";
  const seen = new Map();
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(FUNC);
    if (m) {
      fn = m[1];
      if (!seen.has(fn)) seen.set(fn, 0);
    }
    if (!START.test(lines[i])) continue;
    let depth = 0;
    const body = [];
    for (let j = i; j < Math.min(i + 80, lines.length); j++) {
      body.push(lines[j]);
      for (const ch of lines[j]) {
        if (ch === "{") depth++;
        if (ch === "}") depth--;
      }
      if (depth === 0 && j > i) break;
    }
    if (/recover\(\)/.test(body.join("\n"))) {
      // Still consumes the ordinal: occurrence numbering follows document
      // order, so a guarded goroutine added above an unguarded one cannot
      // hand that one its frozen slot.
      const used = seen.get(fn) ?? 0;
      seen.set(fn, used + 1);
      continue;
    }
    const occurrence = seen.get(fn) ?? 0;
    seen.set(fn, occurrence + 1);
    const id = `${entry}|${fn}|${occurrence}`;
    if (LEGACY.has(id)) continue;
    problems.push(`${id} (line ${i + 1}): ${lines[i].trim().slice(0, 90)}`);
  }
}

if (problems.length > 0) {
  console.error(`check-goroutine-recovery: ${problems.length} new request-path goroutine(s) without recover:`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: defer a recover (log the panic) or make the body incapable of panicking.");
  process.exit(1);
}

console.log(`check-goroutine-recovery: OK (${LEGACY.size} legacy site(s) frozen, no new ones)`);
