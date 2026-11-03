import { useCallback, useEffect, useRef, useState } from "react";

/**
 * Keyed "copied" feedback for copy buttons: `copy(text, key)` writes to the
 * clipboard, marks `key` as copied, and reverts to "" after `revertMs`.
 * The timer is cleaned up on unmount (no setState after unmount leaks).
 */
export function useCopiedField(revertMs = 1500): {
  copiedField: string;
  copy: (text: string, key: string) => void;
} {
  const [copiedField, setCopiedField] = useState("");
  const timerRef = useRef<number | null>(null);
  const lifecycleRef = useRef(0);

  const copy = useCallback(
    (text: string, key: string) => {
      const lifecycle = lifecycleRef.current;
      if (!navigator.clipboard?.writeText) return;
      try {
        navigator.clipboard.writeText(text)
        .then(() => {
          if (lifecycleRef.current !== lifecycle) return;
          if (timerRef.current !== null) window.clearTimeout(timerRef.current);
          setCopiedField(key);
          timerRef.current = window.setTimeout(() => {
            timerRef.current = null;
            setCopiedField("");
          }, revertMs);
        })
        .catch(() => {});
      } catch {
        // Some clipboard implementations throw before returning a Promise.
        // Do not mark the field copied when the browser refused the write.
      }
    },
    [revertMs]
  );

  useEffect(
    () => () => {
      lifecycleRef.current++;
      if (timerRef.current !== null) window.clearTimeout(timerRef.current);
    },
    []
  );

  return { copiedField, copy };
}
