#!/usr/bin/env node
// check-tx-queries.mjs — fail when a transaction block issues queries through
// the pool handle instead of the transaction handle.
//
// SQLite is clamped to a single connection (db.SQLiteMaxOpenConns = 1, and the
// config sync refuses to raise it). A query on s.db inside
// s.db.Transaction(func(tx *gorm.DB) error { ... }) therefore waits forever for
// the connection the transaction itself is holding: the handler hangs instead
// of returning an error. That is exactly what happened to user creation while
// stamping the caller's tenant, and the test hung for three minutes.
//
// Rule: inside a `<receiver>.Transaction(func(tx *gorm.DB) ...)` block, calls
// through `<receiver>.` are flagged (queries must go through `tx`). The
// `.Transaction(` call itself is ignored.
//
// Usage:
//   node scripts/check-tx-queries.mjs            # gate
//   node scripts/check-tx-queries.mjs --list     # print every transaction block
import fs from "node:fs";
import path from "node:path";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const internalDir = join(root, "internal");
const listOnly = process.argv.includes("--list");

// Blocks that legitimately read through the pool, with the reason.
const ALLOWLIST = new Map([]);

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

// Extracts the body of a `.Transaction(` block starting at line i, using brace
// counting. Braces inside string literals are rare enough in this codebase
// that the simple scan is adequate; the gate errs toward reporting.
function transactionBody(lines, i) {
  let started = false;
  let depth = 0;
  const body = [];
  for (let j = i; j < lines.length; j++) {
    const line = lines[j];
    if (!started) {
      const open = line.indexOf("{");
      if (open < 0) continue;
      started = true;
      depth = 1;
      body.push({ text: line.slice(open + 1), line: j + 1 });
      continue;
    }
    for (const ch of line) {
      if (ch === "{") depth++;
      else if (ch === "}") depth--;
    }
    if (depth <= 0) {
      body.push({ text: line.slice(0, Math.max(0, line.indexOf("}"))), line: j + 1 });
      break;
    }
    body.push({ text: line, line: j + 1 });
  }
  return body;
}

const files = walk(internalDir);
const findings = [];
const blocks = [];

for (const file of files) {
  const src = fs.readFileSync(file, "utf8");
  const lines = src.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const m = lines[i].match(/([A-Za-z_]\w*(?:\.\w+)*)\.Transaction\(/);
    if (!m) continue;
    const receiver = m[1];
    const body = transactionBody(lines, i);
    blocks.push({ file: path.relative(root, file), line: i + 1, receiver, lines: body.length });
    const escaped = receiver.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    const callRe = new RegExp(`\\b${escaped}\\.([A-Za-z_]\\w*)`, "g");
    for (const entry of body) {
      for (const hit of entry.text.matchAll(callRe)) {
        if (hit[1] === "Transaction") continue;
        const key = `${path.relative(root, file)}:${entry.line}:${hit[1]}`;
        if (ALLOWLIST.has(key)) continue;
        findings.push({ file: path.relative(root, file), line: entry.line, call: `${receiver}.${hit[1]}` });
      }
    }
  }
}

if (listOnly) {
  for (const b of blocks) console.log(`${b.file}:${b.line}  ${b.receiver}.Transaction  (${b.lines} body lines)`);
  process.exit(0);
}

if (findings.length === 0) {
  console.log(`tx-queries OK — ${blocks.length} transaction block(s), none query through the pool handle.`);
  process.exit(0);
}

console.error(`tx-queries FAILED — ${findings.length} pool query/queries inside a transaction (SQLite has ONE connection):`);
for (const f of findings) console.error(`  - ${f.file}:${f.line}  ${f.call}`);
console.error("\nFix: use the tx handle for every query in the block (or move the read before the transaction opens).");
console.error(`A deliberate exception belongs in ALLOWLIST with a reason in ${path.relative(root, fileURLToPath(import.meta.url))}.`);
process.exit(1);
