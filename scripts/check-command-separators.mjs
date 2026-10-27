#!/usr/bin/env node
// check-command-separators.mjs — fail when a task command is built by joining
// fields with "|" and the fields are not validated first.
//
// Several handlers send `field|field|field` as a task command and the implant
// splits it on "|". An unvalidated "|" inside one field shifts every later field,
// so the task silently means something else. Each of these was found by hand:
//   - password_spray: a password containing "|" made the implant spray the WRONG
//     domain (account lockouts in it);
//   - cred_check (manual + credential reuse): validated a different account, and
//     the lockout fuse kept counting the domain the SERVER parsed, so the fuse no
//     longer covered the account actually probed;
//   - persistence add/remove: deleted a different persistence entry than picked;
//   - uac_bypass: ran a different payload than picked.
//
// Rule: a function that concatenates a task command with "|" must call
// validateCommandArg(...) or credentialCommandHasSeparator(...) in the same
// function. Allowlisted functions are the ones that build a map key rather than a
// wire command (each with a reason).
//
// Usage:
//   node scripts/check-command-separators.mjs          # gate
//   node scripts/check-command-separators.mjs --list   # print every builder
import fs from "node:fs";
import path from "node:path";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const listOnly = process.argv.includes("--list");

// Functions that legitimately join fields with "|" and are NOT task commands.
const ALLOWLIST = new Map([
  ["credCheckFuseKey", "in-memory lockout-fuse key (agent|domain), never sent to an implant"],
  ["add", "iocAccumulator.add builds an in-memory key (kind|value)"],
  ["handleAutoTagApply", "in-memory assignment key (tag|agent)"],
  ["lportfwdKey", "in-memory tunnel key (agent|conn)"],
  ["handleMitreHeatmap", "in-memory heatmap bucket key"],
  ["phishingLandingAllowed", "in-memory token|ip rate-limit key"],
  ["handleAPINetworkTopology", "in-memory topology edge key"],
  ["networkFingerprint", "agent-side interface fingerprint (name|flags|addrs), compared to itself"],
]);

const GUARDS = ["validateCommandArg(", "credentialCommandHasSeparator("];

function walk(dir, out = []) {
  for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, e.name);
    if (e.isDirectory()) walk(full, out);
    else if (e.name.endsWith(".go") && !e.name.endsWith("_test.go")) out.push(full);
  }
  return out;
}

// Function boundaries: a top-level `func (recv) Name(` or `func Name(` line starts
// one; the next such line at column 0 ends it. Good enough for this codebase and
// independent of brace style.
const funcStart = /^(func\s+(?:\([^)]*\)\s*)?)([A-Za-z_][A-Za-z0-9_]*)\s*\(/;

function functionsOf(source) {
  const lines = source.split("\n");
  const out = [];
  let current = null;
  for (let i = 0; i < lines.length; i++) {
    const m = funcStart.exec(lines[i]);
    if (m) {
      if (current) out.push(current);
      current = { name: m[2], start: i, lines: [] };
    }
    if (current) current.lines.push({ text: lines[i], no: i + 1 });
  }
  if (current) out.push(current);
  return out;
}

// A command join looks like `<ident> + "|" + <ident>` (or the mirrored order).
const pipeJoin = /\+\s*"\|"\s*\+/;

const findings = [];
const builders = [];

for (const file of walk(join(root, "internal"))) {
  const source = fs.readFileSync(file, "utf8");
  for (const fn of functionsOf(source)) {
    const joinLines = fn.lines.filter((l) => pipeJoin.test(l.text));
    if (joinLines.length === 0) continue;
    const guarded = fn.lines.some((l) => GUARDS.some((g) => l.text.includes(g)));
    builders.push({ fn: fn.name, file, line: joinLines[0].no, guarded });
    const allowed = ALLOWLIST.has(fn.name);
    if (!guarded && !allowed) {
      findings.push({
        fn: fn.name,
        file: path.relative(root, file),
        line: joinLines[0].no,
        text: joinLines[0].text.trim(),
      });
    }
  }
}

if (listOnly) {
  for (const b of builders) {
    const rel = path.relative(root, b.file);
    const tag = b.guarded ? "guarded" : ALLOWLIST.has(b.fn) ? "allowlisted" : "UNGUARDED";
    console.log(`${tag.padEnd(12)} ${rel}:${b.line} ${b.fn}`);
  }
  console.log(`\n${builders.length} builder(s)`);
  process.exit(0);
}

if (findings.length > 0) {
  console.error("check-command-separators: task commands joined with \"|\" without field validation");
  for (const f of findings) {
    console.error(`  ${f.file}:${f.line}  ${f.fn}  ${f.text}`);
  }
  console.error(
    "\nThe implant splits the command on \"|\", so an unvalidated \"|\" in one field shifts every later\n" +
      "field and the task silently means something else. Validate the fields with validateCommandArg\n" +
      "(inject/spawn/persistence/uac) or credentialCommandHasSeparator (credential paths), or add the\n" +
      "function to ALLOWLIST with a reason if it builds an in-memory key rather than a task command.",
  );
  process.exit(1);
}

console.log(
  `check-command-separators: OK — ${builders.length} pipe-joined builder(s), ` +
    `${builders.filter((b) => b.guarded).length} validated, ${builders.filter((b) => !b.guarded && ALLOWLIST.has(b.fn)).length} allowlisted`,
);
