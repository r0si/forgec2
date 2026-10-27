// Fails when server-side code opens a file through a path relative to the process working
// directory.
//
// Why: this exact shape shipped five times, each with a different symptom.
//   - hosted payloads and uploads wrote a RELATIVE "payloads"/"uploads" directory when
//     server.data_dir was empty, so the files landed next to whatever directory the process
//     started in (and the cleanup sweep scanned the wrong tree);
//   - the ACME handler put a TLS PRIVATE KEY there;
//   - the build output directory resolved to <cwd>/agents;
//   - GET /api/docs/openapi.yaml read "api/openapi.yaml" relative to the cwd, so the docs
//     endpoint answered 500 in every packaged release, which ships no api/ directory.
//
// Rule: in server-side packages, a file operation whose path starts from a relative literal
// must be intentional. Absolute literals are fine (the implant legitimately reads /etc/hosts,
// /proc, ...), and so is a path built from a variable (the caller resolved it). A deliberate
// exception carries "// check:runtime-paths-ok <reason>".
import { readFileSync, readdirSync } from "node:fs";
import { dirname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const root = process.argv[2] ? process.argv[2] : join(dirname(fileURLToPath(import.meta.url)), "..");

// Relative paths that are deliberate, keyed "<file>:<line>".
const ALLOWLIST = new Map([
  ["internal/config/config.go:670", "a relative DEFAULT that is immediately rebased onto the config file's directory"],
  ["internal/payload/stager.go:44", "default only; cmd/server calls SetStagerKeyFile with the configured data dir before first use"],
  ["internal/payload/update_signing.go:28", "default only; cmd/server calls SetUpdateSigningKeyFile with the configured data dir before first use"],
  ["internal/server/handlers_file_thumb.go:288", "the fallback branch only fires when data_dir is empty, and matches the documented data/ convention"],
  ["internal/payload/donut.go:50", "build-tool SEARCH path list; a relative entry means 'look under the cwd', not 'store state here'"],
]);

// Package directories in scope. internal/payload/agent is excluded on purpose: an implant
// reading /etc/passwd or /proc/<pid>/status must use the HOST's absolute paths.
const SCOPES = ["internal/server", "internal/payload", "internal/plugin", "internal/config", "internal/db"];

const FILE_OPS = /\bos\.(?:ReadFile|WriteFile|Open|OpenFile|Stat|MkdirAll|Create|ReadDir)\(\s*/;
const JOIN_OP = /\bfilepath\.Join\(\s*/;

function walk(dir, out = []) {
  let entries;
  try {
    entries = readdirSync(dir, { withFileTypes: true });
  } catch {
    return out;
  }
  for (const e of entries) {
    const full = join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

// "..." -> the literal, if the expression begins with one.
function leadingLiteral(text) {
  const m = text.match(/^"([^"]*)"/);
  return m ? m[1] : null;
}

const findings = [];
let checked = 0;

for (const scope of SCOPES) {
  const scopeDir = join(root, scope);
  for (const file of walk(scopeDir)) {
    const rel = relative(root, file).replace(/\\/g, "/");
    const lines = readFileSync(file, "utf8").split("\n");
    for (let i = 0; i < lines.length; i++) {
      const line = lines[i];
      if (line.trimStart().startsWith("//") || line.includes("check:runtime-paths-ok")) continue;
      if (!FILE_OPS.test(line) && !JOIN_OP.test(line)) continue;
      const after = line.slice(line.search(FILE_OPS.test(line) ? FILE_OPS : JOIN_OP));
      const op = line.match(FILE_OPS) ? line.match(FILE_OPS)[0] : line.match(JOIN_OP)[0];
      const literal = leadingLiteral(after.slice(op.length));
      if (literal === null) continue; // built from a variable: the caller resolved it
      checked++;
      if (literal.startsWith("/") || /^[A-Za-z]:[\\/]/.test(literal) || literal.startsWith("\\\\")) continue; // absolute
      const key = `${rel}:${i + 1}`;
      if (ALLOWLIST.has(key)) continue;
      findings.push({ file: rel, line: i + 1, literal, text: line.trim() });
    }
  }
}

if (checked === 0) {
  console.error("check-runtime-paths: found zero literal path call sites; the patterns no longer match the code");
  process.exit(1);
}
if (findings.length > 0) {
  console.error(`check-runtime-paths: ${findings.length} file operation(s) start from a path relative to the cwd:`);
  for (const f of findings) console.error(`  - ${f.file}:${f.line}  "${f.literal}"  ${f.text.slice(0, 90)}`);
  console.error("\nFix: resolve the path against the configured data directory, or embed the file.");
  console.error(`A relative path is deliberate only with a reason: add "// check:runtime-paths-ok <why>" (and ALLOWLIST it in ${relative(root, fileURLToPath(import.meta.url))}).`);
  process.exit(1);
}

console.log(
  `check-runtime-paths: OK (${checked} literal path call sites, every relative one deliberate; absolute host paths exempt)`,
);
