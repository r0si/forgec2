#!/usr/bin/env node
// apply-openapi-envelope.mjs — keep api/openapi.yaml's success schemas in the
// envelope the server actually sends.
//
// Every JSON 200 from the server is {"success": true, "data": <payload>} (see
// respond() in internal/server/helpers.go). The spec used to describe the bare
// payload, so ~300 documented responses disagreed with the wire format and no
// gate noticed: check:openapi-types only compares the spec to the generated
// TypeScript types, not to what handlers emit.
//
// This rewrites each JSON 200 schema to:
//
//   schema:
//     allOf:
//       - $ref: '#/components/schemas/SuccessResponse'
//       - type: object
//         required: [data]
//         properties:
//           data:
//             <the original payload schema>
//
// File downloads (application/octet-stream) and YAML bodies are left alone, and
// schemas that already reference SuccessResponse are skipped, so the script is
// idempotent.
//
// Usage:
//   node scripts/apply-openapi-envelope.mjs          # rewrite in place
//   node scripts/apply-openapi-envelope.mjs --check  # exit 1 if anything is unwrapped
import { readFileSync, writeFileSync } from "node:fs";
import { dirname, join } from "node:path";
import { fileURLToPath } from "node:url";

const root = join(dirname(fileURLToPath(import.meta.url)), "..");
const specPath = join(root, "api", "openapi.yaml");
const checkOnly = process.argv.includes("--check");

const lines = readFileSync(specPath, "utf8").split("\n");
const indentOf = (s) => s.match(/^\s*/)[0].length;
const isBlank = (s) => s.trim() === "";

const out = [];
let wrapped = 0;
const unwrapped = [];

for (let i = 0; i < lines.length; i++) {
  const line = lines[i];
  const statusMatch = line.match(/^(\s*)'200':\s*$/);
  if (!statusMatch) {
    out.push(line);
    continue;
  }
  const statusIndent = statusMatch[1].length;
  out.push(line);

  // Copy the rest of the '200' block, transforming the JSON schema if present.
  let j = i + 1;
  while (j < lines.length) {
    const cur = lines[j];
    if (!isBlank(cur) && indentOf(cur) <= statusIndent) break;
    const jsonMatch = cur.match(/^(\s*)application\/json:\s*$/);
    if (!jsonMatch) {
      out.push(cur);
      j++;
      continue;
    }
    const mtIndent = jsonMatch[1].length;
    out.push(cur);
    j++;
    // Find the schema: line directly owned by this media type.
    let schemaIdx = -1;
    for (let k = j; k < lines.length; k++) {
      const s = lines[k];
      if (isBlank(s)) continue;
      if (indentOf(s) <= mtIndent) break;
      if (/^\s*schema:\s*$/.test(s)) {
        schemaIdx = k;
        break;
      }
    }
    if (schemaIdx < 0) continue;
    const schemaIndent = indentOf(lines[schemaIdx]);
    // Capture the schema payload block (deeper than schema:).
    let end = schemaIdx + 1;
    while (end < lines.length) {
      const s = lines[end];
      if (!isBlank(s) && indentOf(s) <= schemaIndent) break;
      end++;
    }
    const payload = lines.slice(schemaIdx + 1, end);
    const already = payload.some((l) => l.includes("#/components/schemas/SuccessResponse"));
    const rel = payload.filter((l) => !isBlank(l)).map((l) => l.slice(schemaIndent + 2));
    if (already || rel.length === 0) {
      // Copy the schema (and its payload) through untouched and resume after it.
      for (let k = schemaIdx; k < end; k++) out.push(lines[k]);
      j = end;
      continue;
    }
    unwrapped.push(`line ${i + 1}`);
    const pad = " ".repeat(schemaIndent);
    out.push(`${pad}schema:`);
    out.push(`${pad}  allOf:`);
    out.push(`${pad}    - $ref: '#/components/schemas/SuccessResponse'`);
    out.push(`${pad}    - type: object`);
    out.push(`${pad}      required: [data]`);
    out.push(`${pad}      properties:`);
    out.push(`${pad}        data:`);
    for (const l of rel) out.push(`${pad}          ${l}`);
    wrapped++;
    // Skip the original payload lines; the loop resumes after them.
    j = end;
    continue;
  }
  i = j - 1;
}

if (checkOnly) {
  if (unwrapped.length > 0) {
    console.error(
      `openapi-envelope: ${unwrapped.length} JSON 200 schema(s) are not wrapped in SuccessResponse:`,
    );
    for (const u of unwrapped.slice(0, 20)) console.error(`  ${u}`);
    process.exit(1);
  }
  console.log("openapi-envelope: all JSON 200 schemas match the success envelope");
  process.exit(0);
}

writeFileSync(specPath, out.join("\n"), "utf8");
console.log(`openapi-envelope: wrapped ${wrapped} JSON 200 schema(s)`);
