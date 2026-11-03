import { act, renderHook } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach, afterEach } from "vitest";
import { usePersistedState } from "./usePersistedState";
import { createElement, StrictMode, type ReactNode } from "react";

describe("usePersistedState", () => {
  beforeEach(() => {
    localStorage.clear();
  });
  afterEach(() => {
    vi.restoreAllMocks();
  });

  it("reads the stored JSON value on mount", () => {
    localStorage.setItem("k", JSON.stringify({ a: 1 }));
    const { result } = renderHook(() => usePersistedState("k", { a: 0 }));
    expect(result.current[0]).toEqual({ a: 1 });
  });

  it("falls back to the initial value when the key is missing", () => {
    const { result } = renderHook(() => usePersistedState("missing", 7));
    expect(result.current[0]).toBe(7);
  });

  it("falls back to the initial value when the stored JSON is corrupt", () => {
    localStorage.setItem("bad", "{not json");
    const { result } = renderHook(() => usePersistedState("bad", 7));
    expect(result.current[0]).toBe(7);
  });

  it("writes every change back to storage", () => {
    const { result } = renderHook(() => usePersistedState("w", 1));
    act(() => result.current[1](2));
    expect(result.current[0]).toBe(2);
    expect(localStorage.getItem("w")).toBe("2");
  });

  it("does not persist redundant same-value updates", () => {
    const { result } = renderHook(() => usePersistedState("unchanged", 3));
    const writes = vi.spyOn(Storage.prototype, "setItem");
    act(() => result.current[1](3));
    act(() => result.current[1](value => value));
    expect(writes).not.toHaveBeenCalled();
  });

  it("never writes the previous key's value under Strict Mode", () => {
    localStorage.setItem("a", "1");
    localStorage.setItem("b", "2");
    const { result, rerender } = renderHook(({ k }) => usePersistedState(k, 0), {
      initialProps: { k: "a" },
      wrapper: ({ children }: { children: ReactNode }) => createElement(StrictMode, null, children),
    });
    const writes = vi.spyOn(Storage.prototype, "setItem");
    rerender({ k: "b" });
    expect(result.current[0]).toBe(2);
    const destinationWrites = writes.mock.calls.filter(([key]) => key === "b");
    expect(destinationWrites.length).toBeGreaterThan(0);
    expect(destinationWrites.every(([, value]) => value === "2")).toBe(true);
  });

  it("resyncs when the key changes so route B never keeps route A's value", () => {
    localStorage.setItem("a", "1");
    localStorage.setItem("b", "2");
    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => usePersistedState(k, 0),
      { initialProps: { k: "a" } },
    );
    expect(result.current[0]).toBe(1);

    const writes = vi.spyOn(Storage.prototype, "setItem");
    rerender({ k: "b" });
    expect(result.current[0]).toBe(2);
    expect(writes.mock.calls.filter(([key]) => key === "b")).toEqual([["b", "2"]]);
  });

  it("keeps functional updates and a stable setter across key changes", () => {
    localStorage.setItem("a", "1");
    localStorage.setItem("b", "10");
    const { result, rerender } = renderHook(({ k }) => usePersistedState(k, 0), { initialProps: { k: "a" } });
    const setter = result.current[1];
    rerender({ k: "b" });
    expect(result.current[1]).toBe(setter);
    act(() => setter(value => value + 1));
    expect(result.current[0]).toBe(11);
    expect(localStorage.getItem("a")).toBe("1");
    expect(localStorage.getItem("b")).toBe("11");
  });

  it("retains memory-only behavior when reading storage is forbidden", () => {
    vi.spyOn(Storage.prototype, "getItem").mockImplementation(() => { throw new Error("SecurityError"); });
    const { result } = renderHook(() => usePersistedState("restricted", 4));
    act(() => result.current[1](value => value + 1));
    expect(result.current[0]).toBe(5);
  });

  it("resync falls back to initial when the new key holds corrupt JSON", () => {
    localStorage.setItem("a", "1");
    localStorage.setItem("b", "{oops");
    const { result, rerender } = renderHook(
      ({ k }: { k: string }) => usePersistedState(k, "init"),
      { initialProps: { k: "a" } },
    );

    rerender({ k: "b" });
    expect(result.current[0]).toBe("init");
  });

  it("keeps working in memory when the write throws (quota or disabled storage)", () => {
    const { result } = renderHook(() => usePersistedState("q", 1));
    vi.spyOn(Storage.prototype, "setItem").mockImplementation(() => {
      throw new Error("QuotaExceededError");
    });

    act(() => result.current[1](9));
    expect(result.current[0]).toBe(9);
  });
});
