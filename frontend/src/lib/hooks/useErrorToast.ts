import { useCallback } from "react";
import { toastSoon } from "@/lib/toast";
import { normalizeError, resolveErrorText, type NormalizedError } from "@/lib/errors";
import { useI18n } from "@/lib/i18n";

/**
 * i18n-aware error toast: normalizes the error, resolves the locale message
 * and shows a sonner toast. Replaces scattered `toast.error(err.message)`
 * call sites. `fallbackKey` is used only for errors with no mapped key;
 * `fallbackParams` interpolates that key (e.g. `{ action }` templates).
 *
 * `warnError` is the warning-toned twin for deliberate "outcome, not failure"
 * results (e.g. the 409 command-layer approval conflict): same normalization
 * and message resolution, different toast tone. Both absorb aborted requests
 * silently and return the normalized error.
 */
export function useErrorToast() {
  const { t } = useI18n();

  const emit = useCallback(
    (
      tone: (msg: string) => void,
      err: unknown,
      fallbackKey: string,
      fallbackParams?: Record<string, number | string>,
    ): NormalizedError => {
      const normalized = normalizeError(err);
      if (normalized.kind === "aborted") return normalized;
      const msg = resolveErrorText(t, normalized, () =>
        fallbackParams ? t(fallbackKey, fallbackParams) : t(fallbackKey),
      );
      tone(msg);
      return normalized;
    },
    [t],
  );

  const toastError = useCallback(
    (err: unknown, fallbackKey = "common.error", fallbackParams?: Record<string, number | string>) =>
      emit((msg) => toastSoon((toast) => toast.error(msg)), err, fallbackKey, fallbackParams),
    [emit],
  );

  const warnError = useCallback(
    (err: unknown, fallbackKey = "common.error", fallbackParams?: Record<string, number | string>) =>
      emit((msg) => toastSoon((toast) => toast.warning(msg)), err, fallbackKey, fallbackParams),
    [emit],
  );

  return { toastError, warnError };
}
