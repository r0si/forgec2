import { describe, expect, it, vi } from "vitest";
import {
  DOCK_HEIGHT_DEFAULT,
  DOCK_HEIGHT_MAX,
  DOCK_HEIGHT_MIN,
  clampDockHeight,
  defaultInteractPrefs,
  readInteractPrefs,
  writeInteractPrefs,
} from "./interact-storage";

// The dock preferences are small but they are rehydrated on every mount, so a wrong fallback shows
// up as a dock that jumps or forgets its agent. These pin the behaviour that the implementation
// already has but nothing checked.

describe("clampDockHeight", () => {
  it("keeps in-range heights and rounds fractions", () => {
    expect(clampDockHeight(400)).toBe(400);
    expect(clampDockHeight(400.6)).toBe(401);
  });

  it("clamps to the documented bounds", () => {
    expect(clampDockHeight(10)).toBe(DOCK_HEIGHT_MIN);
    expect(clampDockHeight(10_000)).toBe(DOCK_HEIGHT_MAX);
  });

  it("falls back to the default for values that are not finite", () => {
    // NaN/Infinity come from a hand-edited sessionStorage value, not from the UI.
    expect(clampDockHeight(Number.NaN)).toBe(DOCK_HEIGHT_DEFAULT);
    expect(clampDockHeight(Number.POSITIVE_INFINITY)).toBe(DOCK_HEIGHT_DEFAULT);
  });

  it("honours a custom maximum (a shorter viewport)", () => {
    expect(clampDockHeight(500, 300)).toBe(300);
  });
});

describe("readInteractPrefs", () => {
  const withRaw = (raw: string | null) => ({ getItem: () => raw });

  it("returns the defaults when nothing is stored", () => {
    expect(readInteractPrefs(withRaw(null))).toEqual(defaultInteractPrefs());
  });

  it("returns the defaults for corrupt JSON instead of throwing", () => {
    expect(readInteractPrefs(withRaw("{not json"))).toEqual(defaultInteractPrefs());
  });

  it("rejects an unknown tab and an empty agent id", () => {
    const prefs = readInteractPrefs(withRaw(JSON.stringify({ tab: "nope", agentId: "", height: "x" })));
    expect(prefs.tab).toBe("shell");
    expect(prefs.agentId).toBeNull();
    expect(prefs.height).toBe(DOCK_HEIGHT_DEFAULT);
  });

  it("clamps a stored height that is out of range", () => {
    expect(readInteractPrefs(withRaw(JSON.stringify({ height: 99_999 }))).height).toBe(DOCK_HEIGHT_MAX);
  });

  it("only treats a literal true as pinned", () => {
    expect(readInteractPrefs(withRaw(JSON.stringify({ pinned: "yes" }))).pinned).toBe(false);
    expect(readInteractPrefs(withRaw(JSON.stringify({ pinned: true }))).pinned).toBe(true);
  });

  it("survives a storage that throws on read (private mode)", () => {
    const throwing = {
      getItem: () => {
        throw new Error("SecurityError");
      },
    };
    expect(readInteractPrefs(throwing)).toEqual(defaultInteractPrefs());
  });
});

describe("writeInteractPrefs", () => {
  it("drops the agent id when the dock is not pinned", () => {
    const setItem = vi.fn();
    writeInteractPrefs({ agentId: "agent-1", height: 400, tab: "files", pinned: false }, { setItem });
    expect(setItem).toHaveBeenCalledTimes(1);
    const written = JSON.parse(setItem.mock.calls[0][1] as string);
    expect(written.agentId).toBeNull();
    expect(written.tab).toBe("files");
  });

  it("round-trips a pinned agent and clamps on write", () => {
    const store = new Map<string, string>();
    const storage = { setItem: (k: string, v: string) => void store.set(k, v) };
    writeInteractPrefs({ agentId: "agent-7", height: 9_999, tab: "tasks", pinned: true }, storage);
    // Reading back through the same code path proves the write is what read expects.
    const read = readInteractPrefs({ getItem: (k: string) => store.get(k) ?? null });
    expect(read.agentId).toBe("agent-7");
    expect(read.height).toBe(DOCK_HEIGHT_MAX);
    expect(read.tab).toBe("tasks");
    expect(read.pinned).toBe(true);
  });

  it("does not throw when the quota is exhausted", () => {
    const full = {
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
    };
    expect(() =>
      writeInteractPrefs({ agentId: null, height: 400, tab: "shell", pinned: false }, full),
    ).not.toThrow();
  });
});
