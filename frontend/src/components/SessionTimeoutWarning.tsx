
import { useEffect, useRef, useState, useCallback } from "react";
import { Button } from "@/components/ui/button";
import { toastSoon } from "@/lib/toast";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { bannerSurface } from "@/components/ui/banner";
import { cn } from "@/lib/utils";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";

const CHECK_INTERVAL_MS = 60_000;
const WARN_BEFORE_MS = 5 * 60 * 1000;

async function fetchSessionExpiry(): Promise<number | null> {
  try {
    // api.get routes 401 through the shared handleUnauthorized (debounced
    // redirect to /login), so an expired session logs the user out instead of
    // silently dropping the warning.
    const body = await api.get<{ session_exp?: number }>(paths.auth.me);
    const exp = body?.session_exp;
    return typeof exp === "number" && exp > 0 ? exp : null;
  } catch {
    return null;
  }
}

export default function SessionTimeoutWarning() {
  const { t } = useI18n();
  const [remaining, setRemaining] = useState<number | null>(null);
  const [visible, setVisible] = useState(false);
  const toastShownRef = useRef(false);
  const checkInFlightRef = useRef(false);

  const check = useCallback(async () => {
    // The warning is mounted globally, so a slow /api/auth/me request must
    // not overlap the next polling tick or apply stale expiry information.
    if (checkInFlightRef.current) return;
    checkInFlightRef.current = true;
    try {
      const exp = await fetchSessionExpiry();
      if (!exp) {
        setVisible(false);
        toastShownRef.current = false;
        return;
      }
      const left = exp - Date.now();
      if (left <= 0) {
        setVisible(false);
        toastShownRef.current = false;
        if (typeof window !== "undefined" && window.location.pathname !== "/login") {
          window.location.href = "/login";
        }
        return;
      }
      setRemaining(left);
      const shouldWarn = left < WARN_BEFORE_MS;
      setVisible(shouldWarn);

      if (shouldWarn && !toastShownRef.current) {
        toastShownRef.current = true;
        const minutes = Math.ceil(left / 60_000);
        toastSoon((toast) =>
          toast.warning(
            minutes === 1
              ? t("session.toast_minute")
              : t("session.toast_minutes", { minutes }),
            { duration: 10000 },
          ),
        );
      }
    } finally {
      checkInFlightRef.current = false;
    }
  }, [t]);

  useEffect(() => {
    void check();
  }, [check]);
  useVisibleInterval(() => { void check(); }, CHECK_INTERVAL_MS);

  // G5 fix: only redirect on 401 (actual session expiry); transient network
  // errors, 429 rate limits, or 5xx should not kick a valid session.
  const handleExtend = useCallback(async () => {
    try {
      await api.post(paths.auth.extend);
      toastShownRef.current = false;
      void check();
    } catch (err: unknown) {
      const status = (err as { status?: number })?.status;
      if (status === 401) {
        window.location.href = "/login";
      } else {
        toastSoon((toast) => toast.error(t("session.extend_failed")));
      }
    }
  }, [check, t]);

  const handleLogout = useCallback(() => {
    window.location.href = "/login";
  }, []);

  if (!visible || remaining === null) return null;

  const minutes = Math.max(0, Math.ceil(remaining / 60_000));
  const urgent = remaining < 60_000;

  return (
    <div className="fixed bottom-4 right-4 z-50 max-w-sm">
      <div
        role="alertdialog"
        aria-modal="false"
        aria-live="assertive"
        aria-label={t("session.expires_less_minute")}
        className={cn(bannerSurface(urgent ? "destructive" : "warning", "rounded-lg p-4 shadow-lg backdrop-blur-sm"))}
      >
        <p
          className={`text-sm font-medium ${
            urgent ? "text-destructive-ink" : "text-warning-ink"
          }`}
        >
          {urgent
            ? t("session.expires_less_minute")
            : minutes === 1
              ? t("session.expires_minute")
              : t("session.expires_minutes", { minutes })}
        </p>
        <div className="mt-3 flex gap-2">
          <Button
            size="sm"
            variant={urgent ? "destructive" : "default"}
            onClick={handleExtend}
            autoFocus
          >
            {t("session.extend")}
          </Button>
          <Button size="sm" variant="ghost" onClick={handleLogout}>
            {t("session.logout")}
          </Button>
        </div>
      </div>
    </div>
  );
}
