interface ShortcutDef {
  key: string;
  ctrl?: boolean;
  shift?: boolean;
  alt?: boolean;
  descKey: string;
}

export const DEFAULT_SHORTCUTS: Record<string, ShortcutDef> = {
  new_item: { key: "n", ctrl: true, descKey: "shortcuts.new_item_desc" },
  save: { key: "s", ctrl: true, descKey: "shortcuts.save_desc" },
  show_shortcuts: { key: "/", ctrl: true, descKey: "shortcuts.show_shortcuts_desc" },
  close_modal: { key: "Escape", descKey: "shortcuts.close_modal_desc" },
  refresh: { key: "F5", descKey: "shortcuts.refresh_desc" },
  toggle_lock: { key: "l", ctrl: true, shift: true, descKey: "shortcuts.toggle_lock_desc" },
};

const STORAGE_KEY = "forgec2_shortcuts";

// cloneShortcuts copies every ENTRY, not just the map. The default branch used to return
// { ...DEFAULT_SHORTCUTS }, a shallow copy whose values are the shared module objects, while the
// custom branch did copy each entry - so the same function handed out shared references on one path
// and private copies on the other, and a caller mutating a returned shortcut silently edited the
// defaults for the rest of the session (the keybinding help then shows the wrong binding).
function cloneShortcuts(source: Record<string, ShortcutDef>): Record<string, ShortcutDef> {
  const copy: Record<string, ShortcutDef> = {};
  Object.keys(source).forEach((key) => {
    copy[key] = { ...source[key] };
  });
  return copy;
}

export function loadShortcuts(): Record<string, ShortcutDef> {
  try {
    const saved = localStorage.getItem(STORAGE_KEY);
    if (saved) {
      const custom = JSON.parse(saved) as Record<string, Partial<ShortcutDef>>;
      const result: Record<string, ShortcutDef> = {};
      Object.keys(DEFAULT_SHORTCUTS).forEach((key) => {
        result[key] = { ...DEFAULT_SHORTCUTS[key], ...(custom[key] || {}) };
      });
      return result;
    }
  } catch {
    // corrupt localStorage — fall back to defaults silently
  }
  return cloneShortcuts(DEFAULT_SHORTCUTS);
}

export function formatShortcut(s: ShortcutDef, isMac = false): string {
  const parts: string[] = [];
  if (s.ctrl) parts.push(isMac ? "⌘" : "Ctrl");
  if (s.shift) parts.push(isMac ? "⇧" : "Shift");
  if (s.alt) parts.push(isMac ? "⌥" : "Alt");
  parts.push(s.key.length === 1 ? s.key.toUpperCase() : s.key);
  return parts.join(isMac ? "" : "+");
}

export function matchShortcut(e: KeyboardEvent, s: ShortcutDef): boolean {
  const key = s.key.length === 1 ? s.key.toLowerCase() : s.key;
  const eventKey = e.key.length === 1 ? e.key.toLowerCase() : e.key;
  return (
    eventKey === key &&
    !!s.ctrl === (e.ctrlKey || e.metaKey) &&
    !!s.shift === e.shiftKey &&
    !!s.alt === e.altKey
  );
}

export const DASHBOARD_RANGES = ["1h", "24h", "7d", "30d"] as const;