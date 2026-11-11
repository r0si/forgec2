import { describe, it, expect } from "vitest";
import {
  asRecord,
  firstArray,
  firstField,
  firstNumber,
  normalizeListEnvelope,
  unwrapEnvelope,
} from "./envelope";

describe("envelope helpers", () => {
  it("asRecord rejects arrays and primitives", () => {
    expect(asRecord(null)).toBeNull();
    expect(asRecord([1])).toBeNull();
    expect(asRecord("x")).toBeNull();
    expect(asRecord({ a: 1 })).toEqual({ a: 1 });
  });

  it("firstArray prefers ordered keys and bare arrays", () => {
    expect(firstArray([1, 2], ["data"])).toEqual([1, 2]);
    expect(firstArray({ Agents: [{ id: "a" }], agents: [] }, ["agents", "Agents"])).toEqual([]);
    expect(firstArray({ Agents: [{ id: "a" }] }, ["agents", "Agents"])).toEqual([{ id: "a" }]);
    expect(firstArray({ total: 0 }, ["logs", "Logs"])).toEqual([]);
  });

  it("firstField / firstNumber handle dual-case dual-use payloads", () => {
    expect(firstField({ Total: 3, total: 1 }, ["total", "Total"])).toBe(1);
    expect(firstField({ Total: 3 }, ["total", "Total"])).toBe(3);
    expect(firstNumber({ SuccessCount: "5" }, ["success_count", "SuccessCount"])).toBe(5);
    expect(firstNumber({}, ["x"], 9)).toBe(9);
  });

  it("normalizeListEnvelope covers common C2 list shapes", () => {
    expect(normalizeListEnvelope([{ id: 1 }])).toEqual([{ id: 1 }]);
    expect(normalizeListEnvelope({ agents: [{ id: "a" }] })).toEqual([{ id: "a" }]);
    expect(normalizeListEnvelope({ Logs: [{ id: "b" }] })).toEqual([{ id: "b" }]);
    expect(normalizeListEnvelope({ users: [{ id: "u" }] })).toEqual([{ id: "u" }]);
    expect(normalizeListEnvelope({ data: [{ id: "d" }] })).toEqual([{ id: "d" }]);
    expect(normalizeListEnvelope({ foo: 1 })).toEqual([]);
  });

  it("unwrapEnvelope returns the payload of an envelope body and leaves others alone", () => {
    expect(unwrapEnvelope({ success: true, data: { total: 3 } })).toEqual({ total: 3 });
    // A body without the envelope markers is returned untouched, so a
    // legacy/raw endpoint keeps working.
    expect(unwrapEnvelope({ agents: [] })).toEqual({ agents: [] });
    expect(unwrapEnvelope([1, 2])).toEqual([1, 2]);
    expect(unwrapEnvelope(null)).toBeNull();
  });
});
