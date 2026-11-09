
import { useState, useEffect, useCallback, useRef } from "react";
import { useTypedWS, isWSEvent } from "@/lib/typed-ws";
import { useI18n } from "@/lib/i18n";
import { toastSoon } from "@/lib/toast";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { formatTime, nowTime } from "@/lib/utils";
import { useAppStore } from "@/lib/store";
import { DropdownMenu, DropdownMenuContent, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ScrollArea } from "@/components/ui/scroll-area";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { AlertTriangle, Bell, BellOff } from "lucide-react";

interface Notification {
  // Render key: server rows use "db-<id>", client pushes use "ws-<seq>" —
  // raw numeric ids collided across the two sources (duplicate React keys).
  id: string;
  type: "info" | "warning" | "error" | "success";
  message: string;
  time: string;
  read: boolean;
}

let notifSeq = 1;

function formatNotifTime(raw: string): string {
  const d = new Date(raw);
  if (raw && !isNaN(d.getTime())) return formatTime(raw);
  return raw || nowTime();
}

export function NotificationDropdown() {
  const { t } = useI18n();
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loaded, setLoaded] = useState(false);
  const setUnreadNotifications = useAppStore((s) => s.setUnreadNotifications);
  // Open/close churn guard: skip the refetch when one already went out
  // recently (e.g. the user flicks the dropdown open and closed).
  const lastLoadAtRef = useRef(0);

  const loadNotifications = useCallback((opts?: { force?: boolean }) => {
    if (!opts?.force && Date.now() - lastLoadAtRef.current < 15_000) return;
    lastLoadAtRef.current = Date.now();
    setLoadError(null);
    api.get(paths.notifications.list("page=1&pageSize=20"))
      .then((data) => {
        const list = (data.notifications || data.data || []) as Array<Record<string, unknown>>;
        const mapped: Notification[] = list.slice(0, 20).map((n, i) => ({
          // Missing/unparseable ids fall back to an index-namespaced key
          // instead of all collapsing onto 0.
          id: `db-${n.id ?? i}-${i}`,
          type: (["info", "warning", "error", "success"].includes(String(n.severity || n.type || "info"))
            ? String(n.severity || n.type || "info")
            : "info") as Notification["type"],
          message: String(n.message || n.title || ""),
          time: formatNotifTime(String(n.created_at || "")),
          read: Boolean(n.read),
        }));
        setNotifications(mapped);
        setLoaded(true);
      })
      // A failed read must not leave the badge at 0: that is indistinguishable
      // from "no alerts", so only a successful response updates the count.
      .catch((err: unknown) => {
        setLoadError(err instanceof Error ? err.message : String(err));
        setLoaded(true);
      });
  }, []);

  useEffect(() => {
    loadNotifications();
  }, [loadNotifications]);

  const pushNotification = useCallback((type: Notification["type"], message: string) => {
    const id = `ws-${notifSeq++}`;
    setNotifications((prev) => [
      { id, type, message, time: nowTime(), read: false },
      ...prev.slice(0, 49),
    ]);
  }, []);

  // Coalesce bursty task_update WS events into a single notification per
  // status/task-type within a 4s window, so a large task batch does not flood
  // the dropdown with dozens of entries.
  const taskPendingRef = useRef<Map<string, { status: string; taskType: string; cmd: string; count: number }>>(new Map());
  const taskFlushRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const flushTaskNotifications = useCallback(() => {
    taskFlushRef.current = null;
    const pending = taskPendingRef.current;
    taskPendingRef.current = new Map();
    for (const item of pending.values()) {
      if (item.status === "completed") {
        pushNotification(
          "success",
          item.count === 1
            ? t("topbar.notif.task_done", { type: item.taskType, cmd: item.cmd })
            : t("topbar.notif.task_done_multi", { count: item.count }),
        );
      } else {
        pushNotification(
          "error",
          item.count === 1
            ? t("topbar.notif.task_failed", { type: item.taskType, cmd: item.cmd })
            : t("topbar.notif.task_failed_multi", { count: item.count }),
        );
      }
    }
  }, [pushNotification, t]);

  const queueTaskNotification = useCallback((status: string, taskType: string, cmd: string) => {
    const key = `${status}:${taskType}`;
    const existing = taskPendingRef.current.get(key);
    if (existing) {
      existing.count += 1;
    } else {
      taskPendingRef.current.set(key, { status, taskType, cmd, count: 1 });
    }
    if (!taskFlushRef.current) {
      taskFlushRef.current = setTimeout(flushTaskNotifications, 4000);
    }
  }, [flushTaskNotifications]);

  useEffect(() => () => {
    if (taskFlushRef.current) clearTimeout(taskFlushRef.current);
  }, []);

  useTypedWS(
    ["agent_online", "agent_offline", "task_update", "credential_found", "system_alert", "update_available"],
    (msg) => {
      const online = isWSEvent(msg, "agent_online") ? msg : null;
      if (online) {
        const name = String(online.hostname || online.agent_id || "").slice(0, 32);
        pushNotification("success", t("topbar.notif.agent_online", { name }));
        return;
      }
      const offline = isWSEvent(msg, "agent_offline") ? msg : null;
      if (offline) {
        const name = String(offline.hostname || offline.agent_id || "").slice(0, 32);
        pushNotification("warning", t("topbar.notif.agent_offline", { name }));
        return;
      }
      const task = isWSEvent(msg, "task_update") ? msg : null;
      if (task) {
        const status = String(task.status || "");
        const type = String(task.task_type || "");
        const cmd = String(task.command || "").slice(0, 40);
        if (status === "completed" || status === "failed") queueTaskNotification(status, type, cmd);
        return;
      }
      if (isWSEvent(msg, "credential_found")) {
        pushNotification("success", t("topbar.notif.credential_found"));
        return;
      }
      const alert = isWSEvent(msg, "system_alert") ? msg : null;
      if (alert) {
        pushNotification("warning", String(alert.message || alert.title || t("topbar.notif.system_alert")));
        return;
      }
      const update = isWSEvent(msg, "update_available") ? msg : null;
      if (update) {
        pushNotification("info", t("topbar.notif.update_available", { version: String(update.latest || "") }));
      }
    },
  );

  const unreadCount = notifications.filter((n) => !n.read).length;
  const unreadKnown = loaded && !loadError;
  useEffect(() => {
    if (unreadKnown) setUnreadNotifications(unreadCount);
  }, [unreadKnown, unreadCount, setUnreadNotifications]);
  const markAllRead = () => {
    // Optimistic with rollback: on failure the badge must not lie about
    // server-side unread state until the next reload. Rollback restores the
    // read flag ONLY on the items that were already present, so any WS
    // notification that arrives during the await is preserved rather than
    // being wiped by a full-state replace.
    const idsPresent = new Set<string>();
    setNotifications((prev) => {
      for (const n of prev) idsPresent.add(n.id);
      return prev.map((n) => ({ ...n, read: true }));
    });
    api.put(paths.notifications.readAll).catch(() => {
      setNotifications((prev) =>
        prev.map((n) =>
          idsPresent.has(n.id) && !n.read ? { ...n, read: false } : n,
        ),
      );
      toastSoon((toast) => toast.error(t("topbar.notif.mark_read_failed")));
    });
  };
  const typeColors: Record<string, string> = {
    success: "bg-success",
    warning: "bg-warning",
    error: "bg-destructive",
    info: "bg-info",
  };

  return (
    <DropdownMenu onOpenChange={(next) => { if (next) loadNotifications(); }}>
      <DropdownMenuTrigger render={
        <Tooltip>
          <TooltipTrigger render={<Button variant="ghost" size="icon" className="relative" aria-label={t("topbar.notifications")} />}>
            <Bell className="size-5 text-muted-foreground" />
            {!unreadKnown ? (
              <Badge variant="outline" className="absolute -top-0.5 -right-0.5 min-size-4 px-0.5 text-micro font-bold rounded-full flex items-center justify-center" title={loadError ? t("topbar.notif_count_unreadable") : undefined}>
                ?
              </Badge>
            ) : unreadCount > 0 && (
              <Badge variant="destructive" className="absolute -top-0.5 -right-0.5 min-size-4 px-0.5 text-micro font-bold rounded-full flex items-center justify-center animate-scale-in">
                {unreadCount > 99 ? "99+" : String(unreadCount)}
              </Badge>
            )}
          </TooltipTrigger>
          <TooltipContent>{t("topbar.notifications")}</TooltipContent>
        </Tooltip>
      }>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <div className="px-3 py-2 border-b border-border text-sm font-medium">
          <div className="flex items-center justify-between">
            <span>{t("topbar.notifications")}</span>
            {unreadKnown && unreadCount > 0 && (
              <Button variant="ghost" size="xs" onClick={markAllRead} className="text-micro-sm text-primary hover:text-primary/80">
                {t("topbar.mark_all_read")}
              </Button>
            )}
          </div>
        </div>
        <ScrollArea className="max-h-64">
          {loadError ? (
            <div className="p-6 text-center text-sm">
              <AlertTriangle className="size-6 mx-auto mb-2 text-warning-ink" />
              <p className="text-warning-ink">{t("topbar.notif_load_failed")}</p>
              {notifications.length > 0 && (
                <p className="mt-1 text-xs text-muted-foreground">{t("topbar.notif_showing_cached")}</p>
              )}
              <Button onClick={() => loadNotifications({ force: true })} size="sm" variant="outline" className="mt-3 min-h-11 px-4 sm:min-h-7 sm:px-2.5">
                {t("common.try_again")}
              </Button>
            </div>
          ) : notifications.length === 0 ? (
            <div className="p-6 text-center text-muted-foreground text-sm">
              <BellOff className="size-6 mx-auto mb-2" />
              {t("topbar.no_notifications")}
            </div>
          ) : (
            notifications.map((n) => (
              <div key={n.id} className={`px-4 py-3 border-b border-border last:border-0 ${!n.read ? "bg-primary/5" : ""}`}>
                <div className="flex items-start gap-2">
                  <span className={`size-2 rounded-full mt-1.5 shrink-0 ${typeColors[n.type] || typeColors.info}`} />
                  <div className="flex-1 min-w-0">
                    <p className="text-xs text-foreground truncate">{n.message}</p>
                    <p className="text-micro-sm text-muted-foreground mt-0.5">{n.time}</p>
                  </div>
                </div>
              </div>
            ))
          )}
        </ScrollArea>
      </DropdownMenuContent>
    </DropdownMenu>
  );
}