
import { useEffect, useRef, useState } from "react";
import { I18nProvider } from "@/lib/i18n";
import { ThemeProvider } from "@/lib/theme";
import ErrorBoundary from "./ErrorBoundary";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useAppStore } from "@/lib/store";
import type { PermissionKey } from "@/lib/permission-keys";

const CHUNK_ERROR_RE = /dynamically imported module|Loading chunk|Importing a module script|Failed to fetch dynamically/i;
const RELOAD_FLAG = "chunkErrorReloadAt";
const RELOAD_COUNT_FLAG = "chunkErrorReloadCount";
// A broken redeploy must not trap every tab in a reload loop: after this many
// auto-reloads the app stops and shows a static fallback with a manual escape.
const MAX_CHUNK_RELOADS = 2;

function chunkReloadsExhausted(): boolean {
  try {
    return Number(sessionStorage.getItem(RELOAD_COUNT_FLAG) || 0) >= MAX_CHUNK_RELOADS;
  } catch {
    return false;
  }
}

/** Pure chunk-error policy (testable): throttle bursts, cap total reloads. */
export function chunkReloadDecision(now = Date.now()): "reload" | "throttled" | "exhausted" {
  let last: number;
  let count: number;
  try {
    last = Number(sessionStorage.getItem(RELOAD_FLAG) || 0);
    count = Number(sessionStorage.getItem(RELOAD_COUNT_FLAG) || 0);
  } catch {
    return "reload";
  }
  if (now - last < 15000) return "throttled";
  if (count >= MAX_CHUNK_RELOADS) return "exhausted";
  return "reload";
}

/**
 * After a redeploy the old hashed JS chunks are removed server-side; a tab
 * still running the previous build then fails to lazily import route chunks
 * (404), which surfaces as blank views. Auto-reload once to pick up the new
 * build instead of leaving the operator with an empty screen.
 */
function useChunkErrorReload(onExhausted: () => void) {
  const exhaustedRef = useRef(onExhausted);
  exhaustedRef.current = onExhausted;
  useEffect(() => {
    const reloadOnChunkError = () => {
      switch (chunkReloadDecision()) {
        case "throttled":
          return;
        case "exhausted":
          exhaustedRef.current();
          return;
        case "reload":
          sessionStorage.setItem(RELOAD_FLAG, String(Date.now()));
          sessionStorage.setItem(RELOAD_COUNT_FLAG, String(Number(sessionStorage.getItem(RELOAD_COUNT_FLAG) || 0) + 1));
          window.location.reload();
          return;
      }
    };
    const onError = (e: ErrorEvent) => {
      if (CHUNK_ERROR_RE.test(String(e.message || ""))) reloadOnChunkError();
    };
    const onRejection = (e: PromiseRejectionEvent) => {
      if (CHUNK_ERROR_RE.test(String(e.reason?.message || e.reason || ""))) reloadOnChunkError();
    };
    window.addEventListener("error", onError);
    window.addEventListener("unhandledrejection", onRejection);
    return () => {
      window.removeEventListener("error", onError);
      window.removeEventListener("unhandledrejection", onRejection);
    };
  }, []);
}

type CurrentUser = {
  username?: string;
  role?: string;
  permissions?: string[];
};

function readCurrentUser(payload: unknown): CurrentUser {
  if (!payload || typeof payload !== "object") return {};
  const record = payload as Record<string, unknown>;
  const candidate = record.data && typeof record.data === "object"
    ? record.data as Record<string, unknown>
    : record;
  return {
    username: typeof candidate.username === "string" ? candidate.username : undefined,
    role: typeof candidate.role === "string" ? candidate.role : undefined,
    permissions: Array.isArray(candidate.permissions)
      ? candidate.permissions.filter((permission): permission is string => typeof permission === "string")
      : undefined,
  };
}

/** Load authorization independently of the sidebar so focus/mobile layouts
 * receive the same permission state. `unwrap:false` also tolerates legacy and
 * current `/api/me` response shapes during rolling upgrades. */
// MainLayout calls this for the authenticated shell. Signed-out screens
// (login, forbidden, 404) must not probe /api/me: they have no session, and
// the call only added a 401/404 to every visit's console.
export function useCurrentUserBootstrap() {
  const setCurrentUsername = useAppStore((state) => state.setCurrentUsername);
  const setCurrentUserRole = useAppStore((state) => state.setCurrentUserRole);
  const setCurrentPermissions = useAppStore((state) => state.setCurrentPermissions);

  useEffect(() => {
    let active = true;
    void api.get<unknown>(paths.auth.me, { unwrap: false }).then((payload) => {
      if (!active) return;
      const user = readCurrentUser(payload);
      if (user.username) setCurrentUsername(user.username);
      if (user.role) setCurrentUserRole(user.role);
      if (user.permissions) setCurrentPermissions(user.permissions as PermissionKey[]);
    }).catch(() => { /* the session and network banners surface auth failures */ });
    return () => { active = false; };
  }, [setCurrentPermissions, setCurrentUserRole, setCurrentUsername]);
}

export default function ClientProvider({ children }: { children: React.ReactNode }) {
  // Static bilingual fallback: this renders above I18nProvider (and possibly
  // with broken chunks), so no t() or design-system components here.
  const [chunkDead, setChunkDead] = useState(() => chunkReloadsExhausted());
  useChunkErrorReload(() => setChunkDead(true));
  if (chunkDead) {
    const retry = () => {
      try {
        sessionStorage.removeItem(RELOAD_COUNT_FLAG);
        sessionStorage.removeItem(RELOAD_FLAG);
      } catch { /* storage unavailable: reload anyway */ }
      window.location.reload();
    };
    return (
      <div className="flex min-h-screen items-center justify-center bg-background p-6 text-center text-foreground">
        <div>
          <div className="mb-2 text-lg font-bold">New version failed to load / 新版本加载失败</div>
          <div className="mb-4 text-sm text-muted-foreground">The updated app bundle looks broken. Ask your admin to check the deploy, or retry.<br />更新包可能已损坏。请联系管理员检查部署，或重试。</div>
          <button type="button" onClick={retry} className="rounded-lg border border-border bg-card px-5 py-2 text-foreground">
            Retry / 重试
          </button>
        </div>
      </div>
    );
  }
  return (
    <ErrorBoundary>
      <ThemeProvider>
        {/* Bootstrap-only: the login, 404, forbidden and router error screens
            render outside the authenticated layout and must not pull the full
            dictionary. MainLayout mounts a full provider inside.
            WebSocketProvider also lives in MainLayout: the public login page
            used to open /ws (404 behind the demo gateway), then retry/health-
            poll for as long as it stayed open. No signed-out screen uses the
            socket. */}
        <I18nProvider full={false}>{children}</I18nProvider>
      </ThemeProvider>
    </ErrorBoundary>
  );
}
