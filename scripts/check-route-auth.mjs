#!/usr/bin/env node
// check-route-auth.mjs — fail when a mutating HTTP route is registered without
// any authorization gate.
//
// The server's route files group routes under `middleware.RequirePermission(...)`
// (or `RequireRole(...)`) blocks; handlers may additionally check
// requireOperator / roleGrantsOperator / tenant scoping themselves. A new
// mutating route registered straight on the auth group therefore inherits
// "any authenticated role" — which is how POST /api/update-check/refresh ended
// up letting a viewer trigger unlimited GitHub egress.
//
// This gate cross-checks every mutating registration against both places and
// fails on anything it cannot justify. Intentional exceptions (login endpoints,
// self-service session ops) live in ALLOWLIST with a reason.
//
// Usage:
//   node scripts/check-route-auth.mjs            # report + exit 1 on findings
//   node scripts/check-route-auth.mjs --list     # print the analysed routes
import fs from "node:fs";
import path from "node:path";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const serverDir = join(root, "internal", "server");
const listOnly = process.argv.includes("--list");

// Mutating routes that legitimately carry no permission middleware.
const ALLOWLIST = new Map([
  ["POST /login", "pre-auth login endpoint; operatorPlaneGuard limits the source network"],
  ["POST /api/login", "pre-auth login endpoint; operatorPlaneGuard limits the source network"],
  ["POST /api/auth/extend", "self-service session renewal for the caller's own session"],
  ["POST /logout", "self-service logout; AuthRequired protects it"],
  ["POST /lang/set", "public language preference; value must be in the supported-language list"],
  ["POST /phishing/l/:token", "phishing landing is deliberately public; per-token+IP rate limited"],
  // Implant-facing endpoints. They authenticate the BEACON (registration secret / per-agent
  // key / ExtC2 token inside the request), not an operator session, so a permission
  // middleware would make them unusable. Each one rate-limits and body-limits before the
  // decrypt hot path; the handlers reject unsigned envelopes.
  ["POST /beacon", "implant beacon endpoint; authenticated by the beacon key, rate limited"],
  ["POST /screen_frame", "implant screen frame; same beacon-key auth as /beacon"],
  ["POST /generate_204", "malleable-profile beacon URI; beacon-key auth, rate limited"],
  ["POST /th", "malleable-profile beacon URI; beacon-key auth, rate limited"],
  ["POST /collect", "malleable-profile beacon URI; beacon-key auth, rate limited"],
  ["POST /receive", "External C2 receive channel; ExtC2 token, not an operator session"],
  ["POST /send", "External C2 send channel; ExtC2 token, not an operator session"],
]);

const GATE_MARKERS = [
  "requireOperator",
  "requireAdmin",
  "roleGrantsOperator",
  "currentAIPrincipal",
  "hasPermission",
  "RoleHasPermission",
  // "currentUserID" was here for years: it RESOLVES the caller (and fails closed when there is
  // none) but it does not authorize, so a handler whose only "gate" was that call passed this
  // check - /emergency-stop armed the kill switch for ANY authenticated role that knew its own
  // password. Identity resolution is not a gate.
  "isAdmin",
  "tenantScope",
  "findTenantOrFail",
  "getAgentOrFail",
];

const files = fs.readdirSync(serverDir);
const sourceFiles = files.filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"));

// Handler bodies, used for the in-handler fallback gate check.
const bodies = new Map();
for (const f of sourceFiles) {
  const src = fs.readFileSync(join(serverDir, f), "utf8");
  const re = /func \(s \*Server\) (\w+)\(/g;
  let m;
  const starts = [];
  while ((m = re.exec(src)) !== null) starts.push({ name: m[1], at: m.index });
  for (let i = 0; i < starts.length; i++) {
    const end = i + 1 < starts.length ? starts[i + 1].at : src.length;
    bodies.set(starts[i].name, src.slice(starts[i].at, end));
  }
}

const routes = [];
// EVERY server file, not just routes*.go: mutating routes registered in server.go, api.go or a
// handler_*.go register* function were invisible to this gate. That is how POST
// /api/dns/start reached the global auth group (AuthRequired only) with no permission gate -
// any authenticated role could bind the DNS listener.
for (const f of files.filter((x) => x.endsWith(".go") && !x.endsWith("_test.go"))) {
  const src = fs.readFileSync(join(serverDir, f), "utf8");
  // Group receivers that apply an authorization middleware.
  const guarded = new Set();
  for (const re of [/(\w+)\.Use\(middleware\.RequirePermission\(/g, /(\w+)\.Use\(middleware\.RequireRole\(/g]) {
    let m;
    while ((m = re.exec(src)) !== null) guarded.add(m[1]);
  }
  const routeRe = /(\w+)\.(POST|PUT|PATCH|DELETE)\(\s*"([^"]+)"\s*,\s*(.*)$/gm;
  let m;
  while ((m = routeRe.exec(src)) !== null) {
    const [, recv, method, routePath, rest] = m;
    // A per-route middleware also counts (the call's argument list is on `rest`).
    const perRoute = rest.includes("middleware.RequirePermission(") || rest.includes("middleware.RequireRole(");
    const handlerName = (rest.match(/s\.(\w+)/) || [])[1] || "";
    routes.push({ file: f, recv, method, path: routePath, perRoute, guarded: guarded.has(recv), handler: handlerName });
  }
}

const findings = [];
for (const r of routes) {
  if (r.guarded || r.perRoute) continue;
  const body = bodies.get(r.handler) || "";
  // A registered middleware reference such as operatorPlaneGuard is enough.
  if (GATE_MARKERS.some((g) => body.includes(g))) continue;
  const key = `${r.method} ${r.path}`;
  if (ALLOWLIST.has(key)) continue;
  findings.push({ ...r, key });
}

if (listOnly) {
  for (const r of routes) console.log(`${r.method.padEnd(6)} ${r.path}  ->  s.${r.handler}  [${r.recv}] (${r.file})`);
  process.exit(0);
}

const total = routes.length;
if (findings.length === 0) {
  console.log(`route-auth OK — ${total} mutating routes, every one behind a permission/role gate or an allowed exception (${ALLOWLIST.size}).`);
  process.exit(0);
}

console.error(`route-auth FAILED — ${findings.length} of ${total} mutating routes have no authorization gate:`);
for (const f of findings) {
  console.error(`  - ${f.method} ${f.path} -> s.${f.handler} [${f.recv}] (${f.file})`);
}
console.error(`\nFix: register the route inside a middleware.RequirePermission/RequireRole group (or pass it per route),`);
console.error(`or add a handler-level gate. Intentional exceptions belong in ALLOWLIST in ${path.relative(root, fileURLToPath(import.meta.url))} with a reason.`);
process.exit(1);
