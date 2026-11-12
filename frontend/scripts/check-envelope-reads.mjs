// check-envelope-reads.mjs — a raw-body read must go through an envelope helper.
//
// Every JSON 200 from this server is `{"success":true,"data":{...}}`. The api
// client unwraps one level by default, so a caller that opts out with
// `unwrap:false` gets the ENVELOPE, not the payload. Reading a payload field
// off that value returns undefined, and a list endpoint answers "0 rows" while
// the rows are right there in the response — which is exactly how the fleet
// list shipped empty on every deployment (the API returned the agents, the
// Beacons page rendered none, and nothing failed loudly).
//
// The rule: for every `unwrap:false` call, the value it produces must reach one
// of the envelope helpers (`unwrapEnvelope`, `firstArray`, `firstField`,
// `asRecord`, `normalizeListEnvelope`, `unwrapBody`) before its fields are read.
// Reading the raw body at the top level is the defect; the helper is the fix.
//
// The allowlist is empty on purpose: a call shape this script cannot recognise
// is reported, not ignored, so a new shape gets a human decision.
//
// Usage: node scripts/check-envelope-reads.mjs

import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative } from "node:path";
import { fileURLToPath } from "node:url";

const HELPERS = [
  "unwrapEnvelope",
  "firstArray",
  "firstField",
  "asRecord",
  "normalizeListEnvelope",
  "unwrapBody",
];

/** How far after the call the value's first use is looked for. Far enough for
 *  the `Promise.all` shapes, short enough that a later function re-using the
 *  same variable name is not mistaken for this value. */
const USE_WINDOW = 60;

const root = join(fileURLToPath(new URL(".", import.meta.url)), "..");
const srcRoot = join(root, "src");

function walk(dir) {
  const out = [];
  for (const entry of readdirSync(dir)) {
    const full = join(dir, entry);
    const stat = statSync(full);
    if (stat.isDirectory()) {
      if (entry === "node_modules" || entry === "dist") continue;
      out.push(...walk(full));
    } else if (/\.(ts|tsx)$/.test(entry) && !/\.test\./.test(entry) && !/\.spec\./.test(entry)) {
      out.push(full);
    }
  }
  return out;
}

/** Blank out comments while keeping every line and column, so line numbers stay
 *  valid and a doc comment that merely mentions `unwrap:false` is not read as
 *  code. Strings are left alone (a URL in a string is not a comment). */
function stripComments(source) {
  let out = "";
  let i = 0;
  let state = "code"; // code | line | block | single | double | template
  while (i < source.length) {
    const ch = source[i];
    const next = source[i + 1];
    if (state === "code") {
      if (ch === "/" && next === "/") { out += "  "; i += 2; state = "line"; continue; }
      if (ch === "/" && next === "*") { out += "  "; i += 2; state = "block"; continue; }
      if (ch === "'") { state = "single"; out += ch; i++; continue; }
      if (ch === '"') { state = "double"; out += ch; i++; continue; }
      if (ch === "`") { state = "template"; out += ch; i++; continue; }
      out += ch; i++; continue;
    }
    if (state === "line") {
      if (ch === "\n") { state = "code"; out += ch; } else { out += " "; }
      i++; continue;
    }
    if (state === "block") {
      if (ch === "*" && next === "/") { out += "  "; i += 2; state = "code"; continue; }
      out += ch === "\n" ? "\n" : " "; i++; continue;
    }
    // strings: copy verbatim, honouring backslash escapes
    if (ch === "\\") { out += source.slice(i, i + 2); i += 2; continue; }
    if ((state === "single" && ch === "'") || (state === "double" && ch === '"') || (state === "template" && ch === "`")) {
      state = "code"; out += ch; i++; continue;
    }
    if (ch === "\n" && state !== "template") { state = "code"; out += ch; i++; continue; }
    out += ch; i++;
  }
  return out;
}

/**
 * The raw-body reads of one source file.
 *
 * Exported so the gate itself can be tested: a detector that is never
 * exercised against a broken example is a detector nobody can trust.
 */
export function analyzeSource(raw, rel = "fixture.ts") {
  const findings = [];
  const source = stripComments(raw);
  const lines = source.split("\n");

  for (let i = 0; i < lines.length; i++) {
    if (!/unwrap:\s*false/.test(lines[i])) continue;

    // Where the statement containing the call starts and ends.
    const start = statementStart(lines, i);
    const end = statementEnd(lines, i);
    const stmt = lines.slice(start, end + 1).join("\n");

    // A helper in the same statement already normalises the body.
    if (HELPERS.some((helper) => new RegExp(`\\b${helper}\\s*\\(`).test(stmt))) {
      continue;
    }

    const names = resultNames(lines, i, start);
    if (names.length === 0) {
      findings.push({
        file: rel,
        line: i + 1,
        why: "the value of this `unwrap:false` call is not tracked; bind it to a name and pass that name to an envelope helper",
      });
      continue;
    }

    for (const name of names) {
      const verdict = firstUseIsHelper(lines, i, name);
      if (verdict === false) {
        findings.push({
          file: rel,
          line: i + 1,
          why: `\`${name}\` is not normalised by an envelope helper before it is used; its fields would be read one level too high`,
        });
      } else if (verdict === null) {
        findings.push({
          file: rel,
          line: i + 1,
          why: `\`${name}\` is never used within ${USE_WINDOW} lines of this call; bind it to a name and pass that name to an envelope helper`,
        });
      }
    }
  }
  return findings;
}

/** True when the FIRST use of `name` after line `from` is a call to one of the
 *  envelope helpers; false when the value is used some other way first; null
 *  when it is not used inside the window at all. */
function firstUseIsHelper(lines, from, name) {
  const use = new RegExp(`\\b${name}\\b`);
  const helper = new RegExp(`\\b(?:${HELPERS.join("|")})\\s*\\(\\s*${name}\\b`);
  const helperOpened = new RegExp(`\\b(?:${HELPERS.join("|")})\\s*\\(\\s*$`);
  for (let j = from + 1; j < lines.length && j <= from + USE_WINDOW; j++) {
    const match = use.exec(lines[j]);
    if (!match) continue;
    // A property of something else (`obj.name`) is not this value.
    if (lines[j][match.index - 1] === ".") continue;
    if (helper.test(lines[j])) return true;
    // A helper whose argument is on the next line: `unwrapEnvelope(\n  name\n)`
    if (helperOpened.test(lines[j - 1] || "") && new RegExp(`^\\s*${name}\\b`).test(lines[j])) return true;
    return false;
  }
  return null;
}

/** First line of the statement that contains line `i`. */
function statementStart(lines, i) {
  for (let j = i; j >= 0 && j > i - 12; j--) {
    if (j < i && /[;{}]\s*$/.test(lines[j])) return j + 1;
  }
  return Math.max(0, i - 11);
}

/** Last line of the statement that contains line `i` (brace/paren depth back to
 *  zero at a line end). */
function statementEnd(lines, i) {
  let depth = 0;
  for (let j = i; j < lines.length && j < i + 40; j++) {
    for (const ch of lines[j]) {
      if (ch === "(" || ch === "[" || ch === "{") depth++;
      else if (ch === ")" || ch === "]" || ch === "}") depth--;
    }
    if (depth <= 0 && /[;)\]]\s*$/.test(lines[j].trim())) return j;
    if (depth <= 0 && j > i && /^\s*\}\s*$/.test(lines[j])) return j;
  }
  return i;
}

/** The names the call's value lands in: a `const` binding, a `.then`/`.catch`
 *  callback parameter, or the destructured element of a `Promise.all` array. */
function resultNames(lines, i, start) {
  for (let j = i; j >= start; j--) {
    const line = lines[j];
    if (j < i && /;\s*$/.test(line)) return [];

    const destructuring = line.match(/^\s*(?:const|let|var)\s*\[([^\]]*)\]\s*=\s*await\s+Promise\.all\s*\(\s*\[?\s*$/);
    if (destructuring) {
      const names = destructuring[1]
        .split(",")
        .map((part) => part.trim().split(/[\s:=]/)[0].trim())
        .filter(Boolean);
      const index = elementIndex(lines, j, i);
      if (index != null && names[index]) return [names[index]];
      return [];
    }

    const decl = line.match(/\b(?:const|let|var)\s+([A-Za-z0-9_$]+)\s*=/);
    if (decl) {
      if (lines.slice(j + 1, i).some((l) => /;\s*$/.test(l))) return [];
      return [decl[1]];
    }

    const param = line.match(/\.\s*(?:then|catch)\s*\(\s*\(?\s*([A-Za-z0-9_$]+)\s*\)?\s*=>/);
    if (param) return [param[1]];
  }
  return [];
}

/** Which element of the Promise.all array that opens at `arrayLine` the call at
 *  `from` is (0-based). */
function elementIndex(lines, arrayLine, from) {
  let depth = 0;
  let index = 0;
  for (let j = arrayLine + 1; j <= from; j++) {
    const line = lines[j];
    if (depth === 0 && j > arrayLine + 1 && /^\s*\]/.test(line)) return null;
    for (let k = 0; k < line.length; k++) {
      const ch = line[k];
      if (ch === "(" || ch === "[") depth++;
      else if (ch === ")" || ch === "]") depth--;
      else if (ch === "," && depth === 0) index++;
    }
  }
  return index;
}

// The CLI part runs only when this file is the entry point, so the test can
// import the detector without scanning the tree.
if (process.argv[1] && fileURLToPath(import.meta.url) === process.argv[1]) {
  const files = walk(srcRoot);
  const findings = [];
  for (const file of files) {
    const rel = relative(root, file).replace(/\\/g, "/");
    findings.push(...analyzeSource(readFileSync(file, "utf8"), rel));
  }

  if (findings.length === 0) {
    console.log(`envelope-reads OK — ${files.length} frontend file(s) read every raw body through an envelope helper.`);
    process.exit(0);
  }

  console.error(`envelope-reads FAILED — ${findings.length} raw-body read(s) bypass the envelope helpers:`);
  for (const f of findings) {
    console.error(`  - ${f.file}:${f.line}  ${f.why}`);
  }
  console.error("");
  console.error("Fix: pass the value to unwrapEnvelope(...) (or firstArray/firstField/asRecord) before reading its fields,");
  console.error("or drop `unwrap:false` and read the payload the client already unwrapped for you.");
  process.exit(1);
}
