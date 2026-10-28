import { afterEach, describe, expect, it, vi } from "vitest";
import {
  DEFAULT_SHORTCUTS,
  formatShortcut,
  loadShortcuts,
  matchShortcut,
} from "./shortcuts";

// Keyboard handling is the one place where a wrong comparison silently disables a feature: nothing
// throws, the shortcut just never fires. These pin the matching rules and the persistence merge.

const press = (init: KeyboardEventInit): KeyboardEvent => new KeyboardEvent("keydown", init);

describe("matchShortcut", () => {
  const save = DEFAULT_SHORTCUTS.save; // ctrl+s

  it("matches the documented combination", () => {
    expect(matchShortcut(press({ key: "s", ctrlKey: true }), save)).toBe(true);
  });

  it("is case-insensitive for single characters", () => {
    expect(matchShortcut(press({ key: "S", ctrlKey: true }), save)).toBe(true);
  });

  it("treats Cmd as Ctrl, so the same binding works on macOS", () => {
    expect(matchShortcut(press({ key: "s", metaKey: true }), save)).toBe(true);
  });

  it("rejects a missing or extra modifier", () => {
    expect(matchShortcut(press({ key: "s" }), save)).toBe(false);
    expect(matchShortcut(press({ key: "s", ctrlKey: true, shiftKey: true }), save)).toBe(false);
    expect(matchShortcut(press({ key: "s", ctrlKey: true, altKey: true }), save)).toBe(false);
  });

  it("rejects a different key with the right modifiers", () => {
    expect(matchShortcut(press({ key: "x", ctrlKey: true }), save)).toBe(false);
  });

  it("matches named keys exactly and case-sensitively", () => {
    const close = DEFAULT_SHORTCUTS.close_modal; // Escape
    expect(matchShortcut(press({ key: "Escape" }), close)).toBe(true);
    // "escape" is not a key name a browser sends; only the exact spelling counts for named keys.
    expect(matchShortcut(press({ key: "escape" }), close)).toBe(false);
  });

  it("requires both modifiers for ctrl+shift+l", () => {
    const lock = DEFAULT_SHORTCUTS.toggle_lock;
    expect(matchShortcut(press({ key: "l", ctrlKey: true, shiftKey: true }), lock)).toBe(true);
    expect(matchShortcut(press({ key: "l", ctrlKey: true }), lock)).toBe(false);
  });
});

describe("formatShortcut", () => {
  it("renders the platform spelling", () => {
    expect(formatShortcut(DEFAULT_SHORTCUTS.save)).toBe("Ctrl+S");
    expect(formatShortcut(DEFAULT_SHORTCUTS.save, true)).toBe("⌘S");
  });

  it("keeps the order ctrl, shift, alt, key", () => {
    expect(formatShortcut(DEFAULT_SHORTCUTS.toggle_lock)).toBe("Ctrl+Shift+L");
  });

  it("leaves named keys alone", () => {
    expect(formatShortcut(DEFAULT_SHORTCUTS.refresh)).toBe("F5");
  });
});

describe("loadShortcuts", () => {
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("returns the defaults when nothing is stored", () => {
    vi.stubGlobal("localStorage", { getItem: () => null });
    expect(loadShortcuts()).toEqual(DEFAULT_SHORTCUTS);
  });

  it("merges a stored override onto the default for the same action", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => JSON.stringify({ save: { key: "k" } }),
    });
    const loaded = loadShortcuts();
    expect(loaded.save.key).toBe("k");
    // The untouched fields survive the merge.
    expect(loaded.save.ctrl).toBe(true);
    expect(loaded.save.descKey).toBe(DEFAULT_SHORTCUTS.save.descKey);
  });

  it("ignores stored actions that are not in the defaults", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => JSON.stringify({ not_an_action: { key: "z" } }),
    });
    expect(Object.keys(loadShortcuts()).sort()).toEqual(Object.keys(DEFAULT_SHORTCUTS).sort());
  });

  it("falls back to the defaults for corrupt JSON", () => {
    vi.stubGlobal("localStorage", { getItem: () => "{" });
    expect(loadShortcuts()).toEqual(DEFAULT_SHORTCUTS);
  });

  it("falls back when storage itself throws", () => {
    vi.stubGlobal("localStorage", {
      getItem: () => {
        throw new Error("SecurityError");
      },
    });
    expect(loadShortcuts()).toEqual(DEFAULT_SHORTCUTS);
  });

  it("does not hand out the shared default objects (mutation cannot leak)", () => {
    vi.stubGlobal("localStorage", { getItem: () => null });
    const first = loadShortcuts();
    first.save.key = "mutated";
    expect(DEFAULT_SHORTCUTS.save.key).toBe("s");
  });
});
