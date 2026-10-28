#!/usr/bin/env node
// check-test-stdio.mjs - a test must not close a handle it does not own.
//
// Round 254 found the flake that had been misread as a product bug for many rounds: the log-rotation
// test's cleanup closed every io.Closer sink of the fanout writer, and os.Stdout implements io.Closer.
// Closing it cost the test binary its final PASS line, so `go test` reported
//     FAIL  <pkg>   <n>s
// with NO failing test named and verbose output that simply stopped mid-run. It only bit when that test
// ran last, so it looked like an intermittent production failure - and it also masked a real regression
// (a rerun answering 400 instead of 429) for several rounds.
//
// The rule: in a _test.go file, a loop over `.sinks` (or `.writers`, `.outputs`) that calls `.Close()`
// must also mention os.Stdout - i.e. skip the process's own handles. Anything else that closes a
// process-wide handle must say why in ALLOW.
//
// This is deliberately narrow: it catches the construct that shipped, not every way to misuse a
// handle. A gate that guesses would be worse than one whose scope is stated.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");

// file -> reason. Empty on purpose: the one site that shipped is fixed, and an exception here would
// reintroduce a silent suite failure.
const ALLOW = new Map([]);

const SINK_LOOP = /\bfor\b[^{]*\brange\s+[\w.]*\.(sinks|writers|outputs)\b/;
const CLOSE_CALL = /\.\s*Close\s*\(\s*\)/;
const PROCESS_HANDLE = /\bos\.(Stdout|Stderr)\b/;
// A test may close a handle it created itself; those files name the handle explicitly.
const OWNED_HANDLE = /\border\s+(f|file|handle|w)\s*:?=/;

const problems = [];
const files = [];

function walk(dir) {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    if (entry.name === "node_modules" || entry.name === ".git" || entry.name === "dist" || entry.name === "out") {
      continue;
    }
    const full = join(dir, entry.name);
    if (entry.isDirectory()) {
      walk(full);
    } else if (entry.name.endsWith("_test.go")) {
      files.push(full);
    }
  }
}
walk(join(root, "internal"));
walk(join(root, "pkg"));

let checked = 0;
for (const file of files) {
  const rel = file.slice(root.length + 1).replace(/\\/g, "/");
  const source = readFileSync(file, "utf8");
  if (!SINK_LOOP.test(source)) continue;
  checked++;
  if (PROCESS_HANDLE.test(source)) continue;

  const lines = source.split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!SINK_LOOP.test(lines[i])) continue;
    // Look at the loop body: the next few lines, until the closing brace of the loop.
    let depth = 0;
    let closeAt = -1;
    for (let j = i; j < Math.min(lines.length, i + 25); j++) {
      depth += (lines[j].match(/\{/g) || []).length - (lines[j].match(/\}/g) || []).length;
      if (CLOSE_CALL.test(lines[j])) {
        closeAt = j;
        break;
      }
      if (j > i && depth <= 0) break;
    }
    if (closeAt === -1) continue;
    if (OWNED_HANDLE.test(lines.slice(i, closeAt + 1).join("\n"))) continue;
    problems.push(
      `${rel}:${closeAt + 1} closes objects from a sink loop without skipping os.Stdout/os.Stderr - ` +
        `closing the process handle costs go test its final PASS line, so a failure shows up with no test name`,
    );
  }
}

const allowed = [];
for (const [file, reason] of ALLOW) allowed.push(`${file} (${reason})`);

if (problems.length > 0) {
  console.error("test-stdio: a test closes a handle it does not own");
  for (const problem of problems) console.error(`  ${problem}`);
  console.error(
    "\nSkip os.Stdout/os.Stderr in that loop, or close only handles the test created itself.",
  );
  process.exit(1);
}

console.log(
  `test-stdio OK - ${checked} test file(s) close sink handles, every one skips os.Stdout/os.Stderr` +
    (allowed.length ? ` (${allowed.length} allowed)` : " (allowlist empty)"),
);
