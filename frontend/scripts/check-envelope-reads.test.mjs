// Tests for the envelope-reads gate.
//
// A detector that is never exercised against a broken example is a detector
// nobody can trust: it either passes everything or fails the tree for the
// wrong reason. These cases pin both directions - the shapes that must be
// reported and the shapes that must not.
//
// Run: node --test scripts/check-envelope-reads.test.mjs

import { test } from "node:test";
import assert from "node:assert/strict";
import { analyzeSource } from "./check-envelope-reads.mjs";

test("a raw body read at the top level is reported", () => {
  // The defect that shipped: the fleet list read `agents` off the envelope.
  const source = `
import { api } from "@/lib/api";
export async function load() {
  const d = await api.get<{ agents?: unknown[] }>("/api/agents", { unwrap: false });
  const list = (d.agents || []) as unknown[];
  return list;
}
`;
  const findings = analyzeSource(source);
  assert.equal(findings.length, 1, JSON.stringify(findings));
  assert.match(findings[0].why, /not normalised by an envelope helper/);
});

test("a raw body read through a helper is accepted", () => {
  const source = `
import { api } from "@/lib/api";
import { unwrapEnvelope } from "@/lib/envelope";
export async function load() {
  const d = await api.get<{ agents?: unknown[] }>("/api/agents", { unwrap: false });
  const body = unwrapEnvelope(d) as { agents?: unknown[] };
  return body.agents || [];
}
`;
  assert.deepEqual(analyzeSource(source), []);
});

test("a call spread over several lines is still tracked", () => {
  const source = `
import { api } from "@/lib/api";
import { unwrapEnvelope } from "@/lib/envelope";
export async function load() {
  const d = await api.get<{ agents?: unknown[] }>(
    "/api/agents",
    { unwrap: false },
  );
  const body = unwrapEnvelope(d) as { agents?: unknown[] };
  return body.agents || [];
}
`;
  assert.deepEqual(analyzeSource(source), []);
});

test("the same shape without the helper is reported", () => {
  const source = `
import { api } from "@/lib/api";
export async function load() {
  const d = await api.get<{ agents?: unknown[] }>(
    "/api/agents",
    { unwrap: false },
  );
  const body = d as { agents?: unknown[] };
  return body.agents || [];
}
`;
  assert.equal(analyzeSource(source).length, 1);
});

test("a Promise.all element is tracked to its destructured name", () => {
  const source = `
import { api } from "@/lib/api";
import { unwrapEnvelope } from "@/lib/envelope";
export async function load() {
  const [stats, listeners] = await Promise.all([
    api.get("/api/stats"),
    api
      .get<{ total?: number }>("/api/listeners?page=1", { unwrap: false })
      .catch(() => undefined),
  ]);
  const body = unwrapEnvelope(listeners) as { total?: number } | undefined;
  return { stats, total: body?.total };
}
`;
  assert.deepEqual(analyzeSource(source), []);
});

test("a comment that mentions unwrap:false is not code", () => {
  const source = `
import { api } from "@/lib/api";
/**
 * Reads the raw envelope (unwrap:false) so both shapes work.
 */
export async function load() {
  return api.get("/api/stats");
}
`;
  assert.deepEqual(analyzeSource(source), []);
});

test("a value that is never normalised is reported", () => {
  const source = `
import { api } from "@/lib/api";
export async function load() {
  const raw = await api.get<unknown>("/api/agents", { unwrap: false });
  return raw;
}
`;
  assert.equal(analyzeSource(source).length, 1);
});
