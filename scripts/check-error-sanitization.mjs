#!/usr/bin/env node
// check-error-sanitization.mjs — fail when a 5xx response echoes a raw error
// value instead of a sanitized message.
//
// Internal errors name key material, absolute paths and SQL details. The
// codebase already routes them through sanitizeError (and logs the raw value),
// but six generation handlers returned err.Error() verbatim on a 500 before
// this gate existed.
//
// Rule: inside a respondError(...) call whose status is
// http.StatusInternalServerError, the message must be a string literal, a
// sanitizeError(...) call, or another explicit sanitizer. Raw `err.Error()`,
// `fmt.Sprintf("...%v", err)` and bare error-bearing variables fail.
//
// Usage:
//   node scripts/check-error-sanitization.mjs          # gate
//   node scripts/check-error-sanitization.mjs --list   # print every 5xx site
import fs from "node:fs";
import path from "node:path";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const internalDir = join(root, "internal");
const listOnly = process.argv.includes("--list");

// 5xx responses that deliberately echo an error, with the reason.
const DIRECT_5XX =
  /(?:c\.(?:JSON|String|AbortWithStatusJSON)|respondError\(\s*c)\s*[,(]\s*(?:http\.Status(?:InternalServerError|BadGateway|ServiceUnavailable|GatewayTimeout)|\b5\d\d\b)/;
const ALLOWLIST = new Map([]);

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

const files = walk(internalDir);
const findings = [];
const sites = [];

for (const f of files) {
  const src = fs.readFileSync(f, "utf8");
  const lines = src.split("\n");
  for (let i = 0; i < lines.length; i++) {
    const line = lines[i];
    // Two shapes write a 5xx: the respondError helper, and a direct write. The gate only
    // knew the first, so `c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})`
    // - or the same with StatusServiceUnavailable, a String body, AbortWithStatusJSON, or a
    // numeric 500 - could echo a raw error and never fail this check.
    const helper = line.includes("respondError(");
    const direct = DIRECT_5XX.test(line);
    if (!helper && !direct) continue;
    // Join the call's continuation lines until the parentheses balance.
    let call = line;
    let depth = (line.match(/\(/g) || []).length - (line.match(/\)/g) || []).length;
    let j = i;
    while (depth > 0 && j + 1 < lines.length) {
      j++;
      call += " " + lines[j].trim();
      depth += (lines[j].match(/\(/g) || []).length - (lines[j].match(/\)/g) || []).length;
    }
    sites.push({ file: path.relative(root, f), line: i + 1, call: call.trim() });

    if (direct) {
      const statusMatch = call.match(DIRECT_5XX);
      if (!statusMatch) continue;
      const afterStatus = call.slice(call.indexOf(statusMatch[0]) + statusMatch[0].length);
      const message = afterStatus.trim();
      const safe = message.includes("sanitizeError(") || message.includes("sanitizeErrorForUser(");
      const raw =
        (message.includes("err.Error()") || (/fmt\.Sprintf/.test(message) && /\berr\b/.test(message))) && !safe;
      if (raw) {
        const key = `${path.relative(root, f)}:${i + 1}`;
        if (!ALLOWLIST.has(key)) findings.push({ file: path.relative(root, f), line: i + 1, message });
      }
      continue;
    }

    if (!call.includes("StatusInternalServerError")) continue;
    // Everything after the status argument.
    const afterStatus = call.slice(call.indexOf("StatusInternalServerError") + "StatusInternalServerError".length);
    const message = afterStatus.replace(/^\s*,\s*/, "").replace(/\)\s*;?\s*$/, "").trim();
    const safe =
      message.startsWith('"') || // constant message
      message.startsWith("`") || // template literal
      message.includes("sanitizeError(") ||
      message.includes("sanitizeErrorForUser(") ||
      message.includes("formatSized") || // constant formatter, no error text
      message === "";
    const raw =
      message.includes("err.Error()") ||
      (/fmt\.Sprintf/.test(message) && /\berr\b/.test(message)) ||
      message === "err" ||
      /^[A-Za-z_]\w*$/.test(message); // bare variable: cannot prove it is sanitized
    if (!safe || raw) {
      const key = `${path.relative(root, f)}:${i + 1}`;
      if (!ALLOWLIST.has(key)) findings.push({ file: path.relative(root, f), line: i + 1, message });
    }
  }
}

if (listOnly) {
  for (const s of sites) console.log(`${s.file}:${s.line}  ${s.call}`);
  process.exit(0);
}

if (findings.length === 0) {
  console.log(`error-sanitization OK — ${sites.length} 5xx responses, none echo a raw error.`);
  process.exit(0);
}

console.error(`error-sanitization FAILED — ${findings.length} 5xx response(s) echo a raw error:`);
for (const f of findings) console.error(`  - ${f.file}:${f.line}  message: ${f.message}`);
console.error(`\nFix: log the raw error (slog.Error) and answer with sanitizeError(err, "<Context>").`);
console.error(`A deliberate exception belongs in ALLOWLIST with a reason in ${path.relative(root, fileURLToPath(import.meta.url))}.`);
process.exit(1);
