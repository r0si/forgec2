// Fails when an agent task handler is missing from docs/COMMAND_REFERENCE.md.
//
// Why one direction only: every entry registered in internal/payload/agent's
// taskHandlers map is a task type an operator can actually send, so an
// undocumented one is a feature nobody can find. The reverse is not a defect -
// docs/COMMAND_REFERENCE.md documents 53 types that are handled by the core
// dispatcher or by the C implant rather than by this registry (shell, ls,
// download, set_sleep, ...), so demanding a handler for every documented row would
// fail on legitimate entries.
//
// Measured when the gate was added: 169 registered handlers, 0 undocumented.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

// Resolved from this script rather than the caller's cwd: npm runs the gates from
// frontend/, and a cwd-relative path made this gate fail there with ENOENT.
const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const AGENT_DIR = join(root, "internal/payload/agent");
const DOCS = join(root, "docs/COMMAND_REFERENCE.md");

function fail(message) {
  console.error(`check-task-docs: ${message}`);
  process.exit(1);
}

let registryFiles;
try {
  registryFiles = readdirSync(AGENT_DIR).filter((f) => /^task_registry.*\.go$/.test(f));
} catch (err) {
  fail(`cannot read ${AGENT_DIR}: ${err.message}`);
}
if (registryFiles.length === 0) {
  fail(`no task_registry*.go files under ${AGENT_DIR}; the scan would silently pass`);
}

const handlers = new Set();
// Three registration idioms exist, and this gate only knew the first one:
//   taskHandlers["x"] = handleX          (most module registries)
//   "x": handleX,                        (the core map literal in task_registry.go: 49 entries)
//   RegisterTask(protocol.MustSpec("x"), handleX)
// Missing the literal form left the gate blind to 49 handlers - 22% of the registry - so an
// undocumented core command could never fail it.
for (const file of registryFiles) {
  const src = readFileSync(join(AGENT_DIR, file), "utf8");
  for (const m of src.matchAll(/taskHandlers\["([a-z0-9_]+)"\]\s*=/g)) {
    handlers.add(m[1]);
  }
  for (const m of src.matchAll(/^\s*"([a-z0-9_]+)":\s*[A-Za-z_]\w*,/gm)) {
    handlers.add(m[1]);
  }
  for (const m of src.matchAll(/RegisterTask\(\s*protocol\.(?:MustSpec|Spec)\("([a-z0-9_]+)"/g)) {
    handlers.add(m[1]);
  }
}
if (handlers.size === 0) {
  fail("parsed zero task handlers; the pattern in this script no longer matches the code");
}

let docs;
try {
  docs = readFileSync(DOCS, "utf8");
} catch (err) {
  fail(`cannot read ${DOCS}: ${err.message}`);
}
const documented = new Set();
for (const m of docs.matchAll(/^\|\s*`([a-z0-9_]+)`\s*\|/gm)) {
  documented.add(m[1]);
}
if (documented.size === 0) {
  fail(`parsed zero documented types from ${DOCS}; the table format changed`);
}

const missing = [...handlers].filter((h) => !documented.has(h)).sort();
if (missing.length > 0) {
  console.error("check-task-docs: these agent task handlers have no row in docs/COMMAND_REFERENCE.md:");
  for (const name of missing) console.error(`  - ${name}`);
  console.error("");
  console.error("Add a row (`| `type` | Name | approval | aliases | params | description |`) for each,");
  console.error("or remove the handler. An operator cannot discover an undocumented task type.");
  process.exit(1);
}

console.log(
  `check-task-docs: OK — ${handlers.size} registered handler(s) all documented (${documented.size} documented types)`
);
