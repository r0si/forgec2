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
const LEGACY = new Set([
  // select()s on dst.done, so a racing unsubscribe drops instead of panicking on a closed channel.
  "handlers_ai_runs.go:154",
  "handlers_ai_runs.go:477",
  "handlers_ai_runs.go:601",
  "handlers_ai_runs.go:811",
  "handlers_auth.go:212",
  "handlers_auth.go:270",
  "handlers_cookie_proxy.go:120",
  "handlers_cookie_proxy.go:262",
  "handlers_domain_front_stager.go:85",
  "handlers_lportfwd.go:130",
  "handlers_socks_relay.go:951",
  "handlers_websocket.go:753",
  "handlers_websocket.go:789",
]);

const START = /^\s*go func\s*\(/;

const problems = [];
for (const entry of readdirSync(serverDir)) {
  if (!entry.startsWith("handlers_") || !entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
  const lines = readFileSync(join(serverDir, entry), "utf8").split("\n");
  for (let i = 0; i < lines.length; i++) {
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
    if (/recover\(\)/.test(body.join("\n"))) continue;
    const id = `${entry}:${i + 1}`;
    if (LEGACY.has(id)) continue;
    problems.push(`${id}: ${lines[i].trim().slice(0, 90)}`);
  }
}

if (problems.length > 0) {
  console.error(`check-goroutine-recovery: ${problems.length} new request-path goroutine(s) without recover:`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: defer a recover (log the panic) or make the body incapable of panicking.");
  process.exit(1);
}

console.log(`check-goroutine-recovery: OK (${LEGACY.size} legacy site(s) frozen, no new ones)`);
