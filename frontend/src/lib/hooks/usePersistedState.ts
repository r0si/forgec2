import { useCallback, useEffect, useState } from "react";

function readStoredValue<T>(key: string, initial: T): T {
  if (typeof window === "undefined") return initial;
  try {
    const raw = window.localStorage.getItem(key);
    return raw === null ? initial : (JSON.parse(raw) as T);
  } catch {
    return initial;
  }
}

/**
 * useState persisted to localStorage. Reads once on mount (client-side),
 * writes on every change. Safe when storage is unavailable or the tab
 * restored the value as invalid JSON — falls back to `initial`.
 */
export function usePersistedState<T>(key: string, initial: T) {
  const [snapshot, setSnapshot] = useState(() => ({ key, value: readStoredValue(key, initial) }));

  // Reset before committing a render for a different key. Separate read/write
  // effects briefly wrote A's value to B before the read effect's update landed.
  if (snapshot.key !== key) {
    setSnapshot({ key, value: readStoredValue(key, initial) });
  }

  const setValue = useCallback((next: T | ((previous: T) => T)) => {
    setSnapshot(previous => {
      const value = typeof next === "function" ? (next as (value: T) => T)(previous.value) : next;
      return Object.is(previous.value, value) ? previous : { key: previous.key, value };
    });
  }, []);

  useEffect(() => {
    try {
      if (snapshot.key === key) window.localStorage.setItem(key, JSON.stringify(snapshot.value));
    } catch {
      // Storage full / disabled — state still works in-memory.
    }
  }, [key, snapshot]);

  return [snapshot.value, setValue] as const;
}
