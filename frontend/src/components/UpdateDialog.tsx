import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { useWS } from "@/lib/wsContext";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { formatBytes } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog";
import { Progress } from "@/components/ui/progress";
import { AlertTriangle, ArrowUpCircle, RotateCw } from "lucide-react";
import { toastSoon } from "@/lib/toast";

interface UpdateDialogInfo {
  latest: string;
  current?: string;
  downloadUrl?: string;
}

type Phase = "confirm" | "running" | "failed" | "restarting";

interface ProgressState {
  stage: string;
  percent: number;
  downloaded: number;
  total: number;
  error?: string;
}

/** Custom event to open the dialog from anywhere: `openUpdateDialog(info)`. */
export const OPEN_UPDATE_DIALOG = "forgec2:open-update-dialog";

/**
 * Session flag set right before the page reloads after a hot update, so the
 * reloaded console can tell the operator that the new build is the one they
 * are looking at now.
 */
const UPDATED_FLAG = "forgec2:updated-to";

export function openUpdateDialog(info: UpdateDialogInfo) {
  window.dispatchEvent(new CustomEvent<UpdateDialogInfo>(OPEN_UPDATE_DIALOG, { detail: info }));
}

export function UpdateDialog() {
  const { t } = useI18n();
  const { subscribe } = useWS();
  const { role } = usePermissions();
  const [open, setOpen] = useState(false);
  const [info, setInfo] = useState<UpdateDialogInfo | null>(null);
  const [phase, setPhase] = useState<Phase>("confirm");
  const [progress, setProgress] = useState<ProgressState>({ stage: "idle", percent: 0, downloaded: 0, total: 0 });
  const [polling, setPolling] = useState(false);
  // True once the health probe has seen the server go away: the operator
  // gets a "waiting for the server" hint instead of a plain "restarting".
  const [waiting, setWaiting] = useState(false);
  const pollInFlightRef = useRef(false);
  // The hot update endpoint is admin-only: offering the button to every
  // operator only produced a 403.
  const isAdmin = (role || "").toLowerCase() === "admin";

  const stopPolling = useCallback(() => {
    setPolling(false);
  }, []);

  useEffect(() => () => stopPolling(), [stopPolling]);

  // After the reload that follows a hot update, report the new version.
  useEffect(() => {
    let updated: string | null = null;
    try {
      updated = sessionStorage.getItem(UPDATED_FLAG);
      if (updated) sessionStorage.removeItem(UPDATED_FLAG);
    } catch {
      // Session storage is optional; without it the operator simply does not
      // get the confirmation toast.
    }
    if (updated) {
      toastSoon((toast) => toast.success(t("update_dialog.updated_toast", { version: updated })));
    }
  }, [t]);

  // Open requests from banner / settings / notifications.
  useEffect(() => {
    const onOpen = (e: Event) => {
      const detail = (e as CustomEvent<UpdateDialogInfo>).detail;
      if (!detail?.latest) return;
      setInfo(detail);
      setPhase("confirm");
      setProgress({ stage: "idle", percent: 0, downloaded: 0, total: 0 });
      setOpen(true);
    };
    window.addEventListener(OPEN_UPDATE_DIALOG, onOpen);
    return () => window.removeEventListener(OPEN_UPDATE_DIALOG, onOpen);
  }, []);

  const applyProgress = useCallback((p: Partial<ProgressState> & { stage: string }) => {
    setProgress((prev) => ({
      stage: p.stage,
      percent: typeof p.percent === "number" ? p.percent : prev.percent,
      downloaded: typeof p.downloaded === "number" ? p.downloaded : prev.downloaded,
      total: typeof p.total === "number" ? p.total : prev.total,
      error: p.error,
    }));
    if (p.stage === "failed") setPhase("failed");
    else if (p.stage === "restarting") setPhase("restarting");
  }, []);

  // Live progress via WS; polling fallback covers disconnects.
  useEffect(() => {
    if (!open) return;
    return subscribe((msg) => {
      if (msg.type === "update_progress") {
        applyProgress({
          stage: String(msg.stage || ""),
          percent: Number(msg.percent ?? 0),
          downloaded: Number(msg.downloaded ?? 0),
          total: Number(msg.total ?? 0),
          error: msg.error ? String(msg.error) : undefined,
        });
      } else if (msg.type === "server_restarting") {
        setPhase("restarting");
      }
    });
  }, [open, subscribe, applyProgress]);

  const pollProgress = useCallback(async () => {
    if (!open || pollInFlightRef.current) return;
    pollInFlightRef.current = true;
    try {
      const d = await api.get<{
        stage?: string; percent?: number; downloaded?: number;
        total?: number; error?: string;
      }>(paths.updateProgress);
      if (d.stage) {
        applyProgress({
          stage: String(d.stage),
          percent: Number(d.percent ?? 0),
          downloaded: Number(d.downloaded ?? 0),
          total: Number(d.total ?? 0),
          error: d.error ? String(d.error) : undefined,
        });
      }
    } catch {
      // WebSocket progress remains authoritative; a transient fallback poll
      // failure should not replace the active update state with an error.
    } finally {
      pollInFlightRef.current = false;
    }
  }, [applyProgress, open]);

  useVisibleInterval(() => { void pollProgress(); }, polling ? 3000 : 0);

  // The server replaces itself during a hot update: wait until it answers
  // again and reload the console, otherwise the operator is left staring at
  // a dialog that never finishes.
  useEffect(() => {
    if (phase !== "restarting") return;
    let cancelled = false;
    const probe = async () => {
      try {
        const res = await fetch("/health", { cache: "no-store" });
        if (!res.ok || cancelled) return;
        try {
          sessionStorage.setItem(UPDATED_FLAG, info?.latest || "");
        } catch {
          // The flag is a nicety; the reload happens either way.
        }
        window.location.reload();
      } catch {
        // The server is still down: keep waiting.
        if (!cancelled) setWaiting(true);
      }
    };
    const timer = window.setInterval(() => { void probe(); }, 2000);
    void probe();
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [phase, info?.latest]);

  const startPolling = useCallback(() => {
    setPolling(true);
  }, []);

  const startUpdate = useCallback(async () => {
    setPhase("running");
    setProgress({ stage: "downloading", percent: 0, downloaded: 0, total: 0 });
    startPolling();
    try {
      await api.postJson(paths.updateHotUpdate, {});
    } catch (e) {
      stopPolling();
      setPhase("failed");
      setProgress((prev) => ({
        ...prev,
        stage: "failed",
        error: e instanceof Error ? e.message : String(e),
      }));
    }
  }, [startPolling, stopPolling]);

  useEffect(() => {
    if (!open) stopPolling();
  }, [open, stopPolling]);

  const stageLabel = (() => {
    switch (progress.stage) {
      case "downloading":
        return t("update_dialog.downloading");
      case "verifying":
        return t("update_dialog.verifying");
      case "snapshotting":
        return t("update_dialog.snapshotting");
      case "restarting":
        return t("update_dialog.restarting");
      case "failed":
        return t("update_dialog.failed");
      case "done":
        return t("update_dialog.done");
      default:
        return t("update_dialog.preparing");
    }
  })();

  const indeterminate = progress.percent < 0 || (phase === "running" && progress.total <= 0 && progress.downloaded <= 0);

  return (
    <Dialog open={open} onOpenChange={setOpen}>
      <DialogContent size="md" aria-live="polite">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <ArrowUpCircle className="size-4 text-info-ink" />
            {t("update_dialog.title")}
          </DialogTitle>
          <DialogDescription>
            {info?.current && (
              <span className="font-mono">{info.current} → </span>
            )}
            <span className="font-mono font-semibold">{info?.latest}</span>
          </DialogDescription>
        </DialogHeader>

        {phase === "confirm" && (
          <div className="space-y-3 text-sm">
            <p className="text-muted-foreground">{t("update_dialog.confirm_hint")}</p>
            {!isAdmin && (
              <p className="text-warning-ink">{t("update_dialog.admin_only")}</p>
            )}
            {info?.downloadUrl && (
              <a href={info.downloadUrl} target="_blank" rel="noopener noreferrer"
                className="text-sm text-info-ink underline-offset-2 hover:underline">
                {t("update_dialog.view_changelog")}
              </a>
            )}
          </div>
        )}

        {phase !== "confirm" && (
          <div className="space-y-3">
            <div className="flex items-center justify-between text-sm">
              <span className="text-muted-foreground">{stageLabel}</span>
              {!indeterminate && (
                <span className="font-mono text-xs">{progress.percent}%</span>
              )}
            </div>
            {indeterminate ? (
              <div className="h-2 w-full overflow-hidden rounded-full bg-secondary">
                <div className="h-full w-1/3 animate-pulse rounded-full bg-primary" />
              </div>
            ) : (
              <Progress value={Math.max(0, Math.min(100, progress.percent))} />
            )}
            {progress.total > 0 && (
              <p className="font-mono text-xs text-muted-foreground">
                {formatBytes(progress.downloaded)} / {formatBytes(progress.total)}
              </p>
            )}
            {phase === "failed" && (
              <p className="flex items-start gap-1.5 text-sm text-destructive-ink">
                <AlertTriangle className="size-4 shrink-0" />
                <span>{progress.error || t("update_dialog.failed")}</span>
              </p>
            )}
            {phase === "restarting" && (
              <p className="text-sm text-muted-foreground">
                {waiting ? t("update_dialog.waiting_for_server") : t("update_dialog.restarting_hint")}
              </p>
            )}
          </div>
        )}

        <DialogFooter>
          {phase === "confirm" && (
            <>
              <Button variant="ghost" onClick={() => setOpen(false)}>
                {t("update_dialog.later")}
              </Button>
              {isAdmin && (
                <Button onClick={() => void startUpdate()}>
                  {t("update_dialog.update_now")}
                </Button>
              )}
            </>
          )}
          {phase === "running" && (
            <Button variant="ghost" onClick={() => setOpen(false)}>
              {t("update_dialog.hide")}
            </Button>
          )}
          {(phase === "failed" || phase === "restarting") && (
            <>
              {phase === "failed" && (
                <Button variant="outline" onClick={() => void startUpdate()}>
                  <RotateCw className="size-4" />{t("update_dialog.retry")}
                </Button>
              )}
              <Button variant="ghost" onClick={() => setOpen(false)}>
                {t("common.close")}
              </Button>
            </>
          )}
        </DialogFooter>
      </DialogContent>
    </Dialog>
  );
}
