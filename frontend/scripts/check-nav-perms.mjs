#!/usr/bin/env node
// check-nav-perms.mjs — a sidebar item must not require permissions the page's API does not.
//
// The sidebar hides entries with `perms` the operator lacks ("any-of"). Those keys are typed, so a
// typo is a compile error — but nothing checked that they MATCH the backend, and two real cases did
// not:
//   - /dns had no perms at all while every /api/dns/* route requires listeners.read, so a custom role
//     without that permission saw a page whose every request answered 403;
//   - /ai required settings.read while its page handler and /api/ai/runs require ai.use, so a role
//     holding ai.use could use the page but never see the link.
// Defaults hid this: the built-in `user` role happens to carry listeners.read, settings.read and
// agents.read, so only custom-role deployments were affected.
//
// Scope is deliberately narrow: only NAV items whose path has an EXACT `/api/<segment>` GET route **or** an
// exact bare dual-use GET route at the same path (/loot, /tokens, /users, ...) are checked. A prefix
// match would compare a page against unrelated sibling endpoints and report noise; the rest are
// counted, not guessed.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");
const navPath = join(root, "frontend", "src", "lib", "navigation.ts");

const permFromUse = (line) => {
  const m = line.match(/RequirePermission\(\s*(?:db\.)?Perm([A-Za-z0-9_]+)\s*\)/);
  return m ? m[1] : null;
};

// PermXxxYyy -> the string the frontend uses, taken from the Go constant itself rather than
// guessed: the first version derived "ai.use" from PermAIUse with a CamelCase rule and produced
// "AIUse", which reported a false mismatch on /ai. internal/db/models.go is the authority.
const permStrings = new Map();
for (const file of readdirSync(join(root, "internal", "db")).filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))) {
  for (const m of readFileSync(join(root, "internal", "db", file), "utf8").matchAll(/Perm([A-Za-z0-9_]+)\s*=\s*"([^"]+)"/g)) {
    permStrings.set(m[1], m[2]);
  }
}
const permKey = (camel) => permStrings.get(camel) ?? camel;

const groupPerms = new Map();
const routes = new Map(); // path -> Set(perm key or "(none)")

for (const file of readdirSync(serverDir).filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))) {
  const lines = readFileSync(join(serverDir, file), "utf8").split("\n");
  for (const line of lines) {
    const group = line.match(/(\w+)\s*:?=\s*\w+\.Group\(/);
    if (group) {
      groupPerms.set(group[1], null);
      continue;
    }
    const use = line.match(/(\w+)\.Use\(/);
    if (use) {
      const perm = permFromUse(line);
      if (perm) groupPerms.set(use[1], permKey(perm));
      continue;
    }
    const reg = line.match(/(\w+)\.(GET)\(\s*"([^"]+)"/);
    if (!reg) continue;
    const [, variable, , path] = reg;
    const inline = permFromUse(line);
    const perm = inline ? permKey(inline) : (groupPerms.get(variable) ?? "(none)");
    if (!routes.has(path)) routes.set(path, new Set());
    routes.get(path).add(perm);
  }
}

const nav = readFileSync(navPath, "utf8");
const items = [];
for (const line of nav.split("\n")) {
  const href = line.match(/href:\s*"([^"]+)"/);
  if (!href) continue;
  const perms = [...(line.match(/perms:\s*\[([^\]]*)\]/)?.[1] ?? "").matchAll(/"([^"]+)"/g)].map((m) => m[1]);
  items.push({ href: href[1], perms });
}

// Pages whose data does not share their URL segment. This is the complete
// documented map after manual verification against each page's fetches and
// the backend route's group permission; a page is only listed here when its
// primary read endpoint is one of these paths. Add an entry when you move a
// page's data endpoint — the alternative (a prefix heuristic) produced
// false matches before. A page must not list a WRITE endpoint it only calls
// for mutations: that is exactly what check:nav-perms exists to catch.
const PAGE_DATA_ROUTES = {
  "/beacons": ["/agents"], // GET agents list, agents.read
  "/events": ["/api/timeline/data"], // timeline stream, reconRead agents.read
  "/payload-generator": ["/api/listeners", "/api/generate/profiles"], // listeners.read + genRead agents.read
  "/privilege-escalation": ["/api/privesc/history/:id"], // privesc history, reconRead agents.read
  "/bloodhound": ["/bloodhound/list"], // intel.read
  "/attack": ["/mitre/phases"], // campaigns.read
  "/campaign": ["/campaigns"], // campaigns.read
  "/chat": ["/chat/history"], // extRead agents.read
  "/asset-management": ["/groups"], // groups.read
  "/phishing": ["/phishing/templates"], // campaigns.read
  "/cloud": ["/cloud/:agentId/results"], // intel.read
  "/ntlm": ["/ntlm/relay_status"], // extRead agents.read
  "/proxy-chains": ["/chain/graph"], // extRead agents.read
  "/opsec": ["/api/opsec/rules"], // opsec.read
  "/circuit-breaker": ["/api/circuit-breaker/status"], // opsec.read
  "/autotag": ["/api/autotag/rules"], // settings.read
  // /report's section fetches (agents/tasks/credentials/network/findings/
  // history) are all GETs in reconRead, so any of them pins the permission.
  "/report": ["/api/report/agents"], // agents.read
};

// Group the GET routes by first segment: /api/dns, /api/dns/status and /api/dns/start all answer for
// the DNS page. Only segments where every GET route agrees on the permission set are compared, so a
// page that touches unrelated endpoints is skipped instead of reported.
const segmentPerms = new Map(); // segment -> Set(perm) or null when the routes disagree
for (const [path, perms] of routes) {
  const m = path.match(/^\/api\/([^/]+)(\/|$)/);
  if (!m) continue;
  const segment = m[1];
  const key = [...perms].sort().join("|");
  const current = segmentPerms.get(segment);
  if (!current) segmentPerms.set(segment, { key, perms });
  else if (current.key !== key) segmentPerms.set(segment, null);
}

const problems = [];
const compare = (item, backend, where) => {
  const backendPerms = [...backend].filter((p) => p !== "(none)");
  // A route group with no permission requirement is reachable by every authenticated operator, so a
  // nav entry may legitimately be ungated.
  if (backendPerms.length === 0) return true;
  if (item.perms.length === 0) {
    problems.push(`${item.href}: the sidebar shows it to everyone, but ${where} requires ${backendPerms.join(" or ")}`);
    return false;
  }
  if (!item.perms.some((p) => backendPerms.includes(p))) {
    problems.push(`${item.href}: nav requires ${item.perms.join(" or ")}, but ${where} requires ${backendPerms.join(" or ")}`);
    return false;
  }
  return true;
};

let exact = 0;
let ambiguous = 0;
let bare = 0;
for (const item of items) {
  const segment = item.href.split("/").filter(Boolean)[0];
  const direct = routes.get(`/api/${segment}`);
  const grouped = segmentPerms.get(segment);
  if (!direct && grouped === null) {
    ambiguous++;
    continue;
  }
  if (!direct && !grouped) {
    // No /api/<segment> route. Many pages are dual-use GETs at their bare
    // path (/loot, /tokens, /users, ...): compare against that exact route
    // instead of skipping, since its permission is the one the page checks.
    const bareRoute = routes.get(item.href);
    if (bareRoute) {
      bare++;
      exact++;
      compare(item, new Set(bareRoute), `GET ${item.href}`);
      continue;
    }
    // No exact route either. Pages whose data lives at a documented
    // cross-segment endpoint (PAGE_DATA_ROUTES) are compared against the
    // union of those endpoints' permissions: nav perms are any-of, so the
    // page must share at least one permission with its data union.
    const dataPaths = PAGE_DATA_ROUTES[item.href];
    if (dataPaths) {
      const union = new Set();
      for (const p of dataPaths) {
        for (const perm of routes.get(p) ?? []) union.add(perm);
      }
      if (union.size > 0) {
        exact++;
        compare(item, union, `GET ${dataPaths.join(" | ")}`);
      }
    }
    continue;
  }
  const backend = direct ?? new Set(grouped.perms);
  exact++;
  compare(item, backend, `/api/${segment}`);
}

// The footer Settings shortcut in Sidebar.tsx is not in NAV_ITEMS, so the
// comparison above never saw it. A custom role without settings.read used to
// get the button and a 403 page. Pin its guard mechanically: the /settings
// link must be conditional on can(permissions, "settings.read").
const sidebar = readFileSync(join(root, "frontend", "src", "components", "Sidebar.tsx"), "utf8");
const settingsLink = sidebar.indexOf('to="/settings"');
if (settingsLink < 0) {
  problems.push("Sidebar.tsx: the footer /settings shortcut is missing");
} else if (!sidebar.slice(Math.max(0, settingsLink - 600), settingsLink + 200).includes('can(permissions, "settings.read")')) {
  problems.push('Sidebar.tsx: the footer /settings shortcut is not gated by settings.read (a role without it gets a 403 page)');
}

if (problems.length > 0) {
  console.error(`check-nav-perms: ${problems.length} sidebar/route permission mismatch(es):`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: give the nav item the permission its page's API actually checks (perms are any-of).");
  process.exit(1);
}

console.log(
  `check-nav-perms: OK (${exact} of ${items.length} nav item(s) checked against their backend routes ` +
    `(${bare} via bare dual-use GET); ` +
    `${ambiguous} skipped as ambiguous, ${items.length - exact - ambiguous} have no backend route)`,
);
