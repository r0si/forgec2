// Fails when a request handler touches a table that has NO tenant column without scoping the
// lookup.
//
// Why: fourteen rounds of this audit found the same shape over and over - a handler that takes an
// id (path parameter or request body) and reads/updates/deletes the row directly, where the table
// carries no tenant column so `tenantScope` cannot help. Scripts (read + overwrite, i.e. code
// injection into another tenant's events), BOF blobs (download + run), phishing templates and
// campaigns (retarget a credential-capturing landing page), workflows (EXECUTE against another
// tenant's implants), one-shot schedules, agent status history and agent tags were each found by
// hand. The fix each time was the same: scope through a relationship.
//
// Rule: for the tables listed below, a handler that performs a by-id lookup must also call the
// scoping helper that belongs to that table (or the generic `tenantScope`/`getAgentOrFail`), or
// carry "// check:owner-scope-ok <reason>". A handler that only lists rows must use a scope too -
// the list case is covered by the same check because listing a foreign table without a scope is
// exactly how several of these shipped.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = process.argv[2] ? process.argv[2] : join(dirname(fileURLToPath(import.meta.url)), "..");
const SERVER_DIR = join(root, "internal/server");

// table -> the scoping helper that MUST appear in a handler touching it.
const TABLE_RULES = new Map([
  ["Script", ["scriptScope", "creatorScope"]],
  ["BOFFile", ["bofScope", "creatorScope"]],
  ["PhishingTemplate", ["phishingScope", "creatorScope"]],
  ["PhishingCampaign", ["phishingScope", "creatorScope"]],
  ["Workflow", ["workflowScope", "creatorScope"]],
  ["OneShotTask", ["visibleAgentIDSubquery"]],
  ["AgentStatusEvent", ["getAgentOrFail", "visibleAgentIDSubquery"]],
  ["AgentTag", ["tenantScope"]],
  ["AgentGroup", ["tenantScope"]],
  ["AutoTagRule", ["tenantScope"]],
]);

// Handlers that legitimately work on the whole installation, with the reason.
const ALLOWLIST = new Map([
  ["handleBatchAgentTags", "narrowed with a tenant-scoped implants subquery before reading assignments"],
]);

const SCOPES = [
  "tenantScope",
  "getAgentOrFail",
  "creatorScope",
  "scriptScope",
  "bofScope",
  "phishingScope",
  "workflowScope",
  "visibleAgentIDSubquery",
  "findVisibleBOF",
  "findVisibleWorkflow",
  "auditTenantScope",
  "aiTenantScope",
  "findTenantOrFail",
];

function walk(dir, out = []) {
  for (const e of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

const findings = [];
let checked = 0;

for (const file of walk(SERVER_DIR)) {
  const rel = relative(root, file).replace(/\\/g, "/");
  const src = readFileSync(file, "utf8");
  // Split into functions so one handler's scope call cannot excuse another's.
  const funcs = [];
  const starts = [...src.matchAll(/^func \(s \*Server\) (\w+)\(/gm)].map((m) => ({ name: m[1], at: m.index }));
  for (let i = 0; i < starts.length; i++) {
    const end = i + 1 < starts.length ? starts[i + 1].at : src.length;
    funcs.push({ name: starts[i].name, body: src.slice(starts[i].at, end), line: src.slice(0, starts[i].at).split("\n").length });
  }

  for (const fn of funcs) {
    if (!fn.body.includes("gin.Context")) continue; // not a request handler
    if (fn.body.includes("check:owner-scope-ok")) continue;
    if (ALLOWLIST.has(fn.name)) continue;
    for (const [model, helpers] of TABLE_RULES) {
      // Match the TYPE reference, not just the composite literal: "var row db.Workflow" plus
      // Where("id = ?") is exactly the shape that shipped, and requiring "{" missed it.
      const touches = new RegExp(`\\bdb\\.${model}\\b`).test(fn.body);
      if (!touches) continue;
      const byID =
        fn.body.includes("Where(\"id = ?\"") ||
        fn.body.includes("First(&") ||
        fn.body.includes("Delete(&") ||
        fn.body.includes("Find(&");
      if (!byID) continue;
      checked++;
      // Accept ANY approved scoping helper, not just the table-specific one: a handler may
      // call a wrapper (findVisibleWorkflow) that scopes for it, and demanding the exact name
      // would flag correct code.
      if (helpers.some((h) => fn.body.includes(h)) || SCOPES.some((h) => fn.body.includes(h))) continue;
      // A CREATE cannot scope a row that does not exist yet: stamping the owner is the scoping.
      // Accept currentTenantID only when the handler also creates, so a read path cannot use it
      // as a free pass.
      if (fn.body.includes(".Create(") && fn.body.includes("currentTenantID(")) continue;
      findings.push({
        file: rel,
        line: fn.line,
        func: fn.name,
        model,
        hint: helpers.join(" or "),
      });
    }
  }
}

if (checked === 0) {
  console.error("check-owner-scope: matched no scoped-table handler; the patterns no longer fit the code");
  process.exit(1);
}
if (findings.length > 0) {
  console.error(
    `check-owner-scope: ${findings.length} handler/model pair(s) touch a tenant-less table without scoping:`,
  );
  for (const f of findings) {
    console.error(`  - ${f.file}:${f.line} ${f.func} -> db.${f.model} (needs ${f.hint})`);
  }
  console.error("\nFix: scope through the owning relationship (creator/agent), or add");
  console.error('     "// check:owner-scope-ok <reason>" for a genuinely global table.');
  process.exit(1);
}

console.log(`check-owner-scope: OK (${checked} handler/model pair(s), all scoped)`);
