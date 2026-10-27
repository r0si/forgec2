#!/usr/bin/env node
// check-webdist.mjs — verify internal/webdist/dist matches the latest frontend
// static export (frontend/out). Run after `npm run build`.
//
// Line endings: text files are compared CRLF-normalized (CRLF -> LF) on both
// sides; the size field was dropped for the same reason (it is implied by the\n// hash, and CRLF-normalized text is shorter than the original).\n// A Windows build emits CRLF inside generated .html/.css while Linux
// emits LF, and the committed dist is always LF (normalize-webdist.mjs runs in
// the sync pipeline). Comparing raw bytes made the gate unsatisfiable on
// Windows: either the diff was reported for 142 identical files, or the
// committed bundle had to be CRLF and CI's Linux build rejected it. Binary
// assets (woff2, images) are still compared byte for byte.
//
// Usage: node scripts/check-webdist.mjs
// Exit codes: 0 = fresh, 1 = stale/drift (an embed refresh is required)
import { createHash } from "node:crypto";
import { readdirSync, readFileSync } from "node:fs";
import { join, relative, sep, extname } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(fileURLToPath(new URL(".", import.meta.url)), "..");
const outDir = join(root, "frontend", "out");
const distDir = join(root, "internal", "webdist", "dist");

// Must stay in sync with TEXT_EXT in normalize-webdist.mjs.
const TEXT_EXT = new Set([
  ".js",
  ".mjs",
  ".css",
  ".html",
  ".json",
  ".svg",
  ".txt",
  ".xml",
  ".map",
  ".webmanifest",
]);

function sha256(file) {
  const buf = readFileSync(file);
  if (TEXT_EXT.has(extname(file).toLowerCase())) {
    return createHash("sha256")
      .update(Buffer.from(buf.toString("utf8").replaceAll("\r\n", "\n"), "utf8"))
      .digest("hex");
  }
  return createHash("sha256").update(buf).digest("hex");
}

// Hash with every CR dropped. CRLF-normalizing is not enough to make a Windows
// checkout comparable: vite's static export re-converts the template's newlines,
// so a CRLF template yields a doubled CR ("\r\r\n") that survives replaceAll of
// "\r\n". Dropping all CRs is the loosest question we can ask before calling a
// difference real.
function crHash(file) {
  const buf = readFileSync(file);
  return createHash("sha256")
    .update(Buffer.from(buf.toString("utf8").replaceAll("\r", ""), "utf8"))
    .digest("hex");
}

function walk(dir) {
  const map = new Map();
  const stack = [dir];
  while (stack.length) {
    const cur = stack.pop();
    for (const entry of readdirSync(cur, { withFileTypes: true })) {
      const abs = join(cur, entry.name);
      if (entry.isDirectory()) {
        stack.push(abs);
      } else {
        const rel = relative(dir, abs).split(sep).join("/");
        const isText = TEXT_EXT.has(extname(abs).toLowerCase());
        map.set(rel, {
          hash: sha256(abs),
          crHash: isText ? crHash(abs) : "",
        });
      }
    }
  }
  return map;
}

let out;
let dist;
try {
  out = walk(outDir);
  dist = walk(distDir);
} catch (err) {
  console.error("check-webdist: cannot walk export dirs:", err.message);
  process.exit(1);
}

const stale = [];
const eolOnly = [];
for (const [rel, meta] of out) {
  const d = dist.get(rel);
  if (!d) {
    stale.push(`missing: ${rel}`);
  } else if (d.hash !== meta.hash) {
    // A text file can differ only because the checkout is CRLF. That is a
    // checkout problem, not a stale bundle, and it needs a different fix —
    // saying "changed: 404.html" sends people hunting for a nonexistent
    // content drift.
    if (TEXT_EXT.has(extname(rel).toLowerCase()) && crHash(join(outDir, rel)) === d.crHash) {
      eolOnly.push(rel);
    } else {
      stale.push(`changed: ${rel}`);
    }
  }
}
for (const rel of dist.keys()) {
  if (!out.has(rel)) stale.push(`orphan: ${rel}`);
}

if (stale.length || eolOnly.length) {
  console.error(`FAIL: internal/webdist/dist is stale (${stale.length + eolOnly.length} diff(s)).`);
  if (eolOnly.length) {
    console.error("");
    console.error(
      `${eolOnly.length} file(s) differ ONLY in carriage returns — the bundle is current,`,
    );
    console.error(
      "but this checkout has CRLF sources. On Windows a core.autocrlf=true checkout makes",
    );
    console.error(
      "frontend/index.html CRLF, and vite's static export then emits a doubled CR (\\r\\r\\n).",
    );
    console.error("CI builds on Linux, so a CRLF-built bundle does not match it.");
    console.error("");
    console.error("Fix: check out with LF line endings, then rebuild:");
    console.error("  git config core.autocrlf false");
    console.error("  git rm -r --cached . && git reset --hard   # or re-clone");
    console.error("  powershell -File scripts/build-embedded.ps1");
    console.error("");
  }
  console.error("Run from repo root:  powershell -File scripts/build-embedded.ps1  (or refresh dist only)");
  console.error("");
  console.error("If you did NOT change frontend sources, this is probably a rebuild of a tree with");
  console.error("uncommitted frontend changes: `npm run build` regenerates frontend/out from the");
  console.error("working tree, so out/ no longer represents the committed bundle. Restore it");
  console.error("without touching anyone's work by copying the committed bundle back:");
  console.error("  Remove-Item -Recurse -Force frontend/out; New-Item -ItemType Directory frontend/out | Out-Null");
  console.error("  Copy-Item -Recurse -Force internal/webdist/dist/* frontend/out/");
  for (const line of stale.slice(0, 40)) console.error(`  - ${line}`);
  if (stale.length > 40) console.error(`  ... and ${stale.length - 40} more`);
  process.exit(1);
}
console.log(`OK: internal/webdist/dist matches frontend/out (${out.size} files)`);