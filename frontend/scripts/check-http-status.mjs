#!/usr/bin/env node
// check-http-status.mjs — an outbound call must look at the status it got back.
//
// The notification webhook round (227) found a send that closed the response body without ever reading
// StatusCode, so a receiver answering 500 looked exactly like a 200 and a rule pointing at it was
// silently dead. The GeoIP lookup had the same shape one layer down: ip-api answers 200 with
// {"status":"fail",...} and 429 once the free tier is exhausted, and the decoder turned both into
// empty fields with no explanation.
//
// The rule is mechanical: a call through an http.Client (or http.Get/Post/Head/PostForm) must be
// followed by a StatusCode check within the same function body (approximated by a 45-line window, which
// is generous - every real check in this tree sits within a few lines). A deliberate exception carries
// a reason in ALLOW.
import { readdirSync, readFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..", "..");
const serverDir = join(root, "internal", "server");

const CALL =
  /\b(?:s\.httpClient|httpClient|http\.DefaultClient|client|ssrfSafeClient|s\.client)\s*\.\s*Do\(|\bhttp\.(?:Get|Post|Head|PostForm)\(/;

// file -> reason. Keep this empty when possible; a reason must explain why the status cannot matter.
const ALLOW = new Map([]);

const problems = [];
let checked = 0;
for (const entry of readdirSync(serverDir)) {
  if (!entry.endsWith(".go") || entry.endsWith("_test.go")) continue;
  if (ALLOW.has(entry)) continue;
  const lines = readFileSync(join(serverDir, entry), "utf8").split("\n");
  for (let i = 0; i < lines.length; i++) {
    if (!CALL.test(lines[i]) || lines[i].trim().startsWith("//")) continue;
    const window = lines.slice(i, Math.min(i + 45, lines.length)).join("\n");
    if (/StatusCode/.test(window)) {
      checked++;
      continue;
    }
    problems.push(`${entry}:${i + 1}: ${lines[i].trim().slice(0, 110)}`);
  }
}

if (problems.length > 0) {
  console.error(`check-http-status: ${problems.length} outbound call(s) never inspect the response status:`);
  for (const p of problems) console.error(`  - ${p}`);
  console.error("Fix: check resp.StatusCode (and the body's own status where the API has one).");
  process.exit(1);
}

console.log(`check-http-status: OK (${checked} outbound call(s) inspect the response status)`);
