// Fails when the ATT&CK coverage map advertises a task type the server cannot dispatch.
//
// Why: handlers_mitre.go carries its own warning - "Earlier versions advertised fantasy names
// (exec, schtasks_add, reg_add, steal_token, clear_logs, beacon, sleep, ...) which no agent
// handler implements, making techniques appear covered by operations nobody could run."
// A comment cannot stop that from happening again; the spec registry can. Every TaskTypes
// entry must resolve to a type declared in pkg/protocol's spec list, which is the single
// source the console and the implant both read.
//
// Measured when the gate was added: 82 referenced task types, 0 unknown, 223 dispatchable specs.
import { readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = process.argv[2] ? process.argv[2] : join(dirname(fileURLToPath(import.meta.url)), "..");
const MITRE = join(root, "internal/server/handlers_mitre.go");
const TASKS = join(root, "pkg/protocol/tasks.go");
const SPECS = join(root, "pkg/protocol/taskspec_data.go");

function fail(message, details = []) {
  console.error(`check-mitre-coverage: ${message}`);
  for (const line of details) console.error(`  ${line}`);
  process.exit(1);
}

let mitre;
let tasks;
let specs;
try {
  mitre = readFileSync(MITRE, "utf8");
  tasks = readFileSync(TASKS, "utf8");
  specs = readFileSync(SPECS, "utf8");
} catch (err) {
  fail(`cannot read the sources: ${err.message}`);
}

// Constant name -> wire value ("TaskTypeKillProc" -> "killproc").
const literal = new Map();
for (const m of tasks.matchAll(/(TaskType\w+)\s*=\s*"([a-z0-9_]+)"/g)) literal.set(m[1], m[2]);

// Dispatchable types: the constants that actually have a spec entry.
const dispatchable = new Set();
for (const m of specs.matchAll(/Type:\s*(TaskType\w+)/g)) {
  const value = literal.get(m[1]);
  if (value) dispatchable.add(value);
}
if (dispatchable.size === 0) {
  fail("resolved zero dispatchable task types; the spec pattern no longer matches the code");
}

// Techniques and the task types they claim.
const techniques = [];
for (const block of mitre.matchAll(/\{ID:\s*"([^"]+)"[^}]*?TaskTypes:\s*\[\]string\{([^}]*)\}/g)) {
  const types = [...block[2].matchAll(/"([a-z0-9_]+)"/g)].map((m) => m[1]);
  techniques.push({ id: block[1], types });
}
if (techniques.length === 0) {
  fail("parsed zero techniques; the map format changed and the gate would pass for nothing");
}

const unknown = [];
const badIDs = [];
for (const t of techniques) {
  if (!/^T\d{4}(\.\d{3})?$/.test(t.id)) badIDs.push(t.id);
  for (const type of t.types) {
    if (!dispatchable.has(type)) unknown.push(`${t.id}: ${type}`);
  }
}

if (badIDs.length > 0) fail(`technique ids that are not ATT&CK ids: ${badIDs.join(", ")}`);
if (unknown.length > 0) {
  fail(
    `${unknown.length} technique(s) point at a task type the server cannot dispatch`,
    [...new Set(unknown)].sort(),
  );
}

const covered = techniques.filter((t) => t.types.length > 0).length;
console.log(
  `check-mitre-coverage: OK (${techniques.length} techniques, ${covered} mapped, ` +
    `${techniques.length - covered} honestly uncovered; every mapped type is dispatchable)`,
);
