// Fails when a query is tenant-scoped through a model that has no tenant column.
//
// Why: internal/server/handlers_agent_status.go applied tenantScope to
// db.AgentStatusEvent, whose columns are only (agent_id, status, timestamp). tenantScope
// appends "WHERE tenant_id = ?", so every tenant-scoped operator got
// "SQL logic error: no such column: tenant_id" - the feature was dead in any multi-tenant
// deployment, and the failure looked like a database problem rather than a scoping mistake.
//
// Resolution order for a scope call, within its enclosing function:
//   1. a model literal on the scope line itself;
//   2. the most recent assignment of the query variable passed to the scope helper;
//   3. a model literal in the same statement (the chain may wrap onto later lines);
//   4. the query's destination variable, typed by its "var x []db.Model" / "x := db.Model{}"
//      declaration, matched through First/Find/Delete(&x);
//   5. exactly one model referenced in the function at all.
// A call that cannot be resolved is COUNTED and reported, never guessed: guessing is how the
// original bug read as fine. A deliberate exception carries
// "// check:tenant-scope-ok <reason>" on the call or immediately preceding line.
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = process.argv[2] ? process.argv[2] : join(dirname(fileURLToPath(import.meta.url)), "..");
const DB_DIR = join(root, "internal/db");
const SERVER_DIR = join(root, "internal/server");

function fail(message, details = []) {
  console.error(`check-tenant-scope: ${message}`);
  for (const line of details) console.error(`  ${line}`);
  process.exit(1);
}

// ── models and their tenant column ────────────────────────────────────────────
const models = new Map(); // name -> hasTenantID
for (const file of readdirSync(DB_DIR).filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))) {
  const src = readFileSync(join(DB_DIR, file), "utf8");
  for (const m of src.matchAll(/type (\w+) struct \{([\s\S]*?)\n\}/g)) {
    models.set(m[1], /\bTenantID\b/.test(m[2]));
  }
}
if (models.size === 0) {
  fail(`parsed zero model structs under ${DB_DIR}; the pattern no longer matches the code`);
}

// A struct with a TableName method or an AutoMigrate registration is a real table; a bare DTO
// (db.TaskStats) is only a scan destination and must never be read as the model being scoped.
const tables = new Set();
const tableNameModels = new Map(); // SQL table name -> model name
for (const file of readdirSync(DB_DIR).filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))) {
  const src = readFileSync(join(DB_DIR, file), "utf8");
  for (const m of src.matchAll(/func \((\w+)\) TableName\(\) string\s*\{\s*return\s+"([^"]+)"\s*\}/g)) {
    tables.add(m[1]);
    tableNameModels.set(m[2], m[1]);
  }
  for (const line of src.split("\n").filter((candidate) => candidate.includes("AutoMigrate("))) {
    for (const m of line.matchAll(/&?(\w+)\{\}/g)) {
      if (models.has(m[1])) tables.add(m[1]);
    }
  }
}
if (tables.size === 0) {
  fail(`parsed zero persisted models under ${DB_DIR}; the patterns no longer match the code`);
}
const isTable = (name) => tables.has(name);

const SCOPE_CALL = /\b(tenantScope|auditTenantScope)\s*\(/;
// Any db.X reference that names a known struct counts, not just composite literals: real
// code says "var agent db.Implant" far more often than "db.Implant{}", and matching only the
// literal form left half the call sites unresolvable.
const refsIn = (line) =>
  [...line.matchAll(/\bdb\.(\w+)\b/g)].map((m) => m[1]).filter((name) => models.has(name));
const literalTableNamesIn = (line) =>
  [...line.matchAll(/\.Table\(\s*"([^"]+)"\s*\)/g)].map((m) => m[1]);
const escapeRegExp = (value) => value.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

const violations = [];
const unresolved = [];
let checked = 0;

for (const file of readdirSync(SERVER_DIR).filter((f) => f.endsWith(".go") && !f.endsWith("_test.go"))) {
  const path = join(SERVER_DIR, file);
  const lines = readFileSync(path, "utf8").split("\n");

  const starts = [];
  lines.forEach((line, i) => {
    if (/^func /.test(line)) starts.push(i);
  });
  const functionStart = (i) => {
    let lo = 0;
    for (const s of starts) {
      if (s <= i) lo = s;
      else break;
    }
    return lo;
  };
  const functionEnd = (i) => {
    const next = starts.find((s) => s > i);
    return next === undefined ? lines.length - 1 : next - 1;
  };

  // Destination variables declared in this function: name -> model.
  const varModels = (from, to) => {
    const map = new Map();
    for (let j = from; j <= to; j++) {
      for (const m of lines[j].matchAll(/\bvar\s+(\w+)\s+\[\]?\*?db\.(\w+)/g)) map.set(m[1], m[2]);
      for (const m of lines[j].matchAll(/\b(\w+)\s*:=\s*(?:&)?db\.(\w+)\{/g)) map.set(m[1], m[2]);
      for (const m of lines[j].matchAll(/\bvar\s+(\w+)\s+\[\]?\*?db\.(\w+)/g)) map.set(m[1], m[2]);
    }
    return map;
  };

  lines.forEach((line, i) => {
    if (!SCOPE_CALL.test(line) || line.trimStart().startsWith("//")) return;
    if (/^func .*\b(?:tenantScope|auditTenantScope)\s*\(/.test(line)) return;
    if (line.includes("check:tenant-scope-ok") || lines[i - 1]?.includes("check:tenant-scope-ok")) return;
    const fStart = functionStart(i);
    const fEnd = functionEnd(i);

    // 1. The scope line itself.
    const onLineLiteralTables = literalTableNamesIn(line);
    if (onLineLiteralTables.length > 0) {
      checked++;
      recordTableName(onLineLiteralTables[onLineLiteralTables.length - 1], i, path);
      return;
    }
    const onLineTables = refsIn(line).filter(isTable);
    if (onLineTables.length > 0) {
      checked++;
      record(onLineTables[onLineTables.length - 1], i, path);
      return;
    }

    // 2. Resolve the query variable from its most recent assignment. This is more precise
    //    than treating the surrounding block as one statement: a scope line is often
    //    followed immediately by a second query against a different table.
    const scopeArg = line.match(/\b(?:tenantScope|auditTenantScope)\s*\(\s*(\w+)/)?.[1];
    if (scopeArg) {
      const assignment = new RegExp(`^\\s*${escapeRegExp(scopeArg)}\\s*:?=`);
      for (let j = i - 1; j >= fStart; j--) {
        if (!assignment.test(lines[j])) continue;
        const originLines = lines.slice(j, i);
        const originTableNames = originLines.flatMap(literalTableNamesIn);
        if (originTableNames.length > 0) {
          checked++;
          recordTableName(originTableNames[originTableNames.length - 1], i, path);
          return;
        }
        const originTables = originLines.flatMap(refsIn).filter(isTable);
        if (originTables.length > 0) {
          checked++;
          record(originTables[originTables.length - 1], i, path);
          return;
        }
        break;
      }
    }

    // 3. The statement the call belongs to: walk back to a blank line or a line ending in
    //    an explicit terminator, and forward to the end of the chain.
    let s = i;
    while (s > fStart && !/^\s*$/.test(lines[s - 1]) && !/;\s*$/.test(lines[s - 1])) s--;
    let e = i;
    while (e < fEnd && !/^\s*$/.test(lines[e + 1]) && !/;\s*$/.test(lines[e])) e++;
    let inStatement = [];
    for (let j = s; j <= e; j++) inStatement.push(...refsIn(lines[j]));
    const statementTableNames = [];
    for (let j = s; j <= e; j++) statementTableNames.push(...literalTableNamesIn(lines[j]));
    if (statementTableNames.length > 0) {
      checked++;
      recordTableName(statementTableNames[statementTableNames.length - 1], i, path);
      return;
    }
    const stmtTables = inStatement.filter(isTable);
    if (stmtTables.length > 0) {
      checked++;
      record(stmtTables[stmtTables.length - 1], i, path);
      return;
    }

    // 4. The destination variable of First/Find/Delete in this statement.
    const vars = varModels(fStart, fEnd);
    for (let j = s; j <= e; j++) {
      for (const m of lines[j].matchAll(/\b(?:First|Find|Delete|Count|Scan|Take|Last)\(&?(\w+)/g)) {
        if (vars.has(m[1]) && isTable(vars.get(m[1]))) {
          checked++;
          record(vars.get(m[1]), i, path);
          return;
        }
      }
    }

    // 5. Exactly one model in the whole function.
    const all = [];
    for (let j = fStart; j <= fEnd; j++) all.push(...refsIn(lines[j]));
    const unique = [...new Set(all)].filter(isTable);
    if (unique.length === 1) {
      checked++;
      record(unique[0], i, path);
      return;
    }

    // Fallback: if every table this function touches carries a tenant column, it does not
    // matter which one the scope applies to.
    const fnTables = [...new Set(all)].filter(isTable);
    if (fnTables.length > 0 && fnTables.every((name) => models.get(name))) {
      checked++;
      return;
    }
    unresolved.push(`${relative(root, path)}:${i + 1}: could not resolve the scoped model`);
  });

  function record(model, i, filePath) {
    if (!models.has(model)) {
      violations.push(`${relative(root, filePath)}:${i + 1}: unknown model db.${model}`);
      return;
    }
    if (!models.get(model)) {
      violations.push(
        `${relative(root, filePath)}:${i + 1}: db.${model} has no TenantID column, so tenantScope adds an unknown column`,
      );
    }
  }

  function recordTableName(tableName, i, filePath) {
    const model = tableNameModels.get(tableName);
    if (!model) {
      violations.push(
        `${relative(root, filePath)}:${i + 1}: Table(${JSON.stringify(tableName)}) has no matching TableName model`,
      );
      return;
    }
    record(model, i, filePath);
  }
}

if (checked === 0) {
  fail("resolved zero tenantScope call sites; the patterns in this script no longer match the code");
}
if (violations.length > 0) {
  fail(`${violations.length} scope call(s) target a model without a tenant column`, violations);
}
if (unresolved.length > 0) {
  fail(
    `${unresolved.length} scope call site(s) could not be resolved; add a model or a reasoned ` +
      `"// check:tenant-scope-ok" exception`,
    unresolved,
  );
}
console.log(
  `check-tenant-scope: OK (${checked} resolved scope calls, ${models.size} models; every scoped model carries TenantID)`,
);
