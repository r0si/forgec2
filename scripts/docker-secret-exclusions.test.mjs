import test from "node:test";
import assert from "node:assert/strict";
import { requiredSecretExclusions, unsafeSecretExclusions } from "./docker-secret-exclusions.mjs";

const safe = requiredSecretExclusions.join("\n");

test("recursive exclusions protect an empty inclusion policy", () => {
  assert.deepEqual(unsafeSecretExclusions(safe), []);
});
test("every exclusion is mandatory", () => {
  for (const omitted of requiredSecretExclusions) {
    const input = requiredSecretExclusions.filter(pattern => pattern !== omitted).join("\n");
    assert.deepEqual(unsafeSecretExclusions(input), [omitted]);
  }
});
test("root-only environment patterns are insufficient", () => {
  assert.ok(unsafeSecretExclusions(".env\n.env.*").includes("**/.env.*"));
});
test("late wildcard inclusion invalidates existing exclusions", () => {
  assert.deepEqual(unsafeSecretExclusions(`${safe}\n!**/*`), [...requiredSecretExclusions]);
});
test("late specific inclusion is rejected conservatively", () => {
  assert.ok(unsafeSecretExclusions(`${safe}\n!frontend/.env.local`).length > 0);
});
test("reasserting all exclusions after includes restores protection", () => {
  assert.deepEqual(unsafeSecretExclusions(`${safe}\n!frontend/**\n${safe}`), []);
});
test("comments, blank lines and CRLF do not alter policy", () => {
  assert.deepEqual(unsafeSecretExclusions(`# comment\r\n\r\n${safe.replaceAll("\n", "\r\n")}\r\n# !not-an-inclusion`), []);
});
