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
// Scope is deliberately narrow: only NAV items whose path has an EXACT `/api/<segment>` GET route are
// checked. A prefix match would compare a page against unrelated sibling endpoints and report noise;
// the 44 nav items yield a stable set of exact matches, and the rest are counted, not guessed.
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
let exact = 0;
let ambiguous = 0;
for (const item of items) {
  const segment = item.href.split("/").filter(Boolean)[0];
  const direct = routes.get(`/api/${segment}`);
  const grouped = segmentPerms.get(segment);
  if (!direct && grouped === null) {
    ambiguous++;
    continue;
  }
  if (!direct && !grouped) continue;
  const backend = direct ?? new Set(grouped.perms);
  exact++;
  const backendPerms = [...backend].filter((p) => p !== "(none)");
  // A route group with no permission requirement is reachable by every authenticated operator, so a
  // nav entry may legitimately be ungated.
  if (backendPerms.length === 0) continue;
  if (item.perms.length === 0) {
    problems.push(`${item.href}: the sidebar shows it to everyone, but /api/${segment} requires ${backendPerms.join(" or ")}`);
    continue;
  }
  if (!item.perms.some((p) => backendPerms.includes(p))) {
    problems.push(`${item.href}: nav requires ${item.perms.join(" or ")}, but /api/${segment} requires ${backendPerms.join(" or ")}`);
  }
}

if (problems.length > 0) {
  console.error(`check-nav-perms: ${problems.length} sidebar/route permission mismatch(es):`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: give the nav item the permission its page's API actually checks (perms are any-of).");
  process.exit(1);
}

console.log(
  `check-nav-perms: OK (${exact} of ${items.length} nav item(s) checked against their backend routes; ` +
    `${ambiguous} skipped as ambiguous, ${items.length - exact - ambiguous} have no backend route)`,
);
