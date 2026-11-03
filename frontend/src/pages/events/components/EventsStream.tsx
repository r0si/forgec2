
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { firstArray } from "@/lib/envelope";
import { useI18n } from "@/lib/i18n";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { POLL } from "@/lib/polling";
import { useWS } from "@/lib/wsContext";
import { subscribeTyped } from "@/lib/typed-ws";
import { TIMELINE_EVENTS } from "@/lib/ws-events";
import { formatTime } from "@/lib/utils";
import { DataState } from "@/components/ui/data-state";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { SearchInput } from "@/components/SearchInput";
import { StatusBadge } from "@/components/ui/status-indicator";
import { RefreshCw, Activity, AlertTriangle, ListTodo, Zap, Radio } from "lucide-react";
import type { Task } from "@/types/task";
import type { TimelineEvent, UnifiedEvent } from "./types";
import { countEventSources, filterUnified, mergeEvents, type AlertLike } from "./merge-events";
import { mergePolledWithLive, unifiedFromWS, upsertLiveEvent } from "./ws-to-event";
import { useInteractStore } from "@/lib/interact-store";

const SOURCE_ICONS: Record<string, React.ReactNode> = {
  timeline: <Zap className="size-3.5" />,
  task: <ListTodo className="size-3.5" />,
  alert: <AlertTriangle className="size-3.5" />,
};

// How many event rows are rendered before the operator has to ask for more.
const PAGE_SIZE = 80;

const SOURCE_STYLES: Record<string, { border: string; iconBg: string; iconText: string; bg: string }> = {
  timeline: { border: "border-l-info", iconBg: "bg-info/15", iconText: "text-info-ink", bg: "" },
  task: { border: "border-l-chart-6", iconBg: "bg-chart-6/15", iconText: "text-chart-6", bg: "bg-chart-6/[0.03]" },
  alert: { border: "border-l-destructive", iconBg: "bg-destructive/15", iconText: "text-destructive-ink", bg: "bg-destructive/[0.03]" },
};

export default function EventsStream() {
  const { t } = useI18n();
  const { connected } = useWS();
  const [query, setQuery] = useState("");
  const [live, setLive] = useState<UnifiedEvent[]>([]);
  const dockAgentId = useInteractStore((s) => s.agentId);
  const [followSession, setFollowSession] = useState(true);

  const liveBuffer = useRef<UnifiedEvent[]>([]);
  const rafId = useRef<number | null>(null);
  const flushLive = useCallback(() => {
    rafId.current = null;
    const incoming = liveBuffer.current;
    liveBuffer.current = [];
    if (incoming.length === 0) return;
    setLive((prev) => incoming.reduce((next, ev) => upsertLiveEvent(next, ev), prev));
  }, []);

  const { data, loading, error, refresh } = useApiResource<{ timeline: TimelineEvent[]; tasks: Task[]; alerts: AlertLike[]; partialFailures: string[] }>({
    fetcher: async () => {
      const [tl, tk, nt] = await Promise.allSettled([
        api.get(paths.timeline.data("page=1")),
        api.get(paths.tasks.list("page=1&pageSize=50")),
        api.get(paths.notifications.list("page=1&pageSize=50")),
      ]);
      const failures = [tl, tk, nt].filter((r) => r.status === "rejected").length;
      if (failures === 3) throw new Error(t("events.load_failed"));
      // A source that failed contributes an empty list; name it so the counts
      // below read as "unknown" instead of "zero events".
      const partialFailures: string[] = [];
      if (tl.status === "rejected") partialFailures.push(t("events.failed_source_ops"));
      if (tk.status === "rejected") partialFailures.push(t("events.failed_source_tasks"));
      if (nt.status === "rejected") partialFailures.push(t("events.failed_source_notifications"));
      return {
        timeline: tl.status === "fulfilled" ? firstArray(tl.value, ["events", "data", "Events"]) as TimelineEvent[] : [],
        tasks: tk.status === "fulfilled" ? firstArray(tk.value, ["tasks", "data", "Tasks"]) as Task[] : [],
        alerts: nt.status === "fulfilled" ? firstArray(nt.value, ["notifications", "data"]) as AlertLike[] : [],
        partialFailures,
      };
    },
    pollMs: connected ? POLL.events : POLL.eventsFallback,
    errorMessage: t("events.load_failed"),
  });
  const partialFailures = data?.partialFailures ?? [];

  useEffect(() => {
    const unsub = subscribeTyped(TIMELINE_EVENTS, (msg) => {
      const ev = unifiedFromWS(msg);
      if (!ev) return;
      liveBuffer.current.push(ev);
      if (rafId.current === null) {
        rafId.current = requestAnimationFrame(flushLive);
      }
    });
    return () => {
      unsub();
      if (rafId.current !== null) {
        cancelAnimationFrame(rafId.current);
        rafId.current = null;
      }
    };
  }, [flushLive]);

  const allRows = useMemo(
    () => mergeEvents(data?.timeline ?? [], data?.tasks ?? [], data?.alerts ?? []),
    [data],
  );

  const mergedRows = useMemo(
    () => mergePolledWithLive(allRows, live),
    [allRows, live],
  );

  const rows = useMemo(
    () => filterUnified(
      mergedRows,
      "all",
      query,
      followSession ? dockAgentId : null,
    ),
    [mergedRows, query, followSession, dockAgentId],
  );

  // The feed is unbounded (three merged sources plus the live socket), so only
  // the first PAGE_SIZE rows are rendered. visibleCount is deliberately NOT
  // reset when rows changes: a 5s poll that replaced it would collapse a feed
  // the operator had expanded, which is the same silent-truncation problem in
  // a different disguise.
  const [visibleCount, setVisibleCount] = useState(PAGE_SIZE);
  const visible = useMemo(() => rows.slice(0, visibleCount), [rows, visibleCount]);
  const hidden = rows.length - visible.length;

  const counts = useMemo(() => countEventSources(mergedRows), [mergedRows]);

  return (
    <div className="flex flex-col gap-4">
      {/* Toolbar */}
      <div className="flex flex-wrap items-center gap-2">
        <SearchInput
          value={query}
          onChange={setQuery}
          placeholder={t("events.search_placeholder")}
          className="min-w-[200px] flex-1"
          label={t("events.search_placeholder")}
        />
        {dockAgentId && (
          <Button
            type="button"
            size="xs"
            variant={followSession ? "default" : "outline"}
            onClick={() => setFollowSession((v) => !v)}
          >
            {followSession ? t("events.follow_session") : t("events.follow_all")}
          </Button>
        )}
        <Badge variant={connected ? "success" : "secondary"} className="font-mono text-micro-sm">
          <Radio className="size-3 mr-1" />
          {connected ? t("events.live") : t("events.polling")}
        </Badge>
        <Button variant="outline" size="sm" onClick={() => void refresh()} className="gap-1.5">
          <RefreshCw className="size-3.5" />
          {t("events.refresh")}
        </Button>
      </div>

      {partialFailures.length > 0 && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-2.5 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("events.partial_load_failed", { sources: partialFailures.join(", ") })}</span>
        </div>
      )}

      {/* Source summary chips */}
      <div className="flex items-center gap-3">
        {(["timeline", "task", "alert"] as const).map((src) => {
          const s = SOURCE_STYLES[src];
          const labelKey = src === "timeline" ? "events.source_timeline" : src === "task" ? "events.source_task" : "events.source_alert";
          // A failed source shows "?", not 0: the count is unknown, not empty.
          const failed = partialFailures.includes(
            src === "timeline" ? t("events.failed_source_ops") : src === "task" ? t("events.failed_source_tasks") : t("events.failed_source_notifications"),
          );
          return (
            <div key={src} className={`flex items-center gap-1.5 rounded-md border border-border/50 bg-background px-2.5 py-1 text-xs${failed ? " border-warning/40" : ""}`}>
              <span className={`flex size-5 items-center justify-center rounded ${s.iconBg} ${s.iconText}`}>
                {SOURCE_ICONS[src]}
              </span>
              <span className="text-muted-foreground">{t(labelKey)}</span>
              <span className={`font-mono font-semibold ${failed ? "text-warning-ink" : "text-foreground"}`}>{failed ? "?" : counts[src]}</span>
            </div>
          );
        })}
        <div className="ml-auto text-xs text-muted-foreground font-mono">
          {visible.length === rows.length
            ? t("events.total_count", { count: rows.length })
            : t("events.shown_of_total", { shown: visible.length, total: rows.length })}
        </div>
      </div>

      {/* Event list */}
      <Card className="overflow-hidden divide-y divide-border/50">
        <DataState
          loading={loading}
          error={error}
          onRetry={() => void refresh()}
          empty={!loading && !error && rows.length === 0}
          emptyTitle={t("events.empty")}
          emptyMessage={t("events.empty_hint")}
        >
          <ul>
            {visible.map((ev) => {
              const s = SOURCE_STYLES[ev.source] ?? SOURCE_STYLES.timeline;
              return (
                <li
                  key={ev.id}
                  className={`group flex items-start gap-3 border-l-2 ${s.border} ${s.bg} px-4 py-3 text-sm transition-colors hover:bg-muted/30`}
                >
                  {/* Source icon */}
                  <span className={`mt-0.5 flex size-6 shrink-0 items-center justify-center rounded-md ${s.iconBg} ${s.iconText}`}>
                    {SOURCE_ICONS[ev.source] ?? <Activity className="size-3.5" />}
                  </span>

                  {/* Time */}
                  <span className="w-28 shrink-0 pt-0.5 font-mono text-xs text-muted-foreground">
                    {ev.at ? formatTime(ev.at) : "—"}
                  </span>

                  {/* Content */}
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 flex-wrap">
                      <span className="font-medium leading-snug">{ev.title}</span>
                      {ev.source === "task" && ev.kind && ev.kind !== ev.title && (
                        <Badge variant="outline" className="border-dashed text-[10px] px-1.5 py-0 font-mono">{ev.kind}</Badge>
                      )}
                      {ev.source === "alert" && ev.kind && (
                        <Badge variant={ev.kind === "critical" || ev.kind === "error" ? "destructive" : ev.kind === "warning" ? "warning" : "secondary"} className="text-[10px] px-1.5 py-0">{ev.kind}</Badge>
                      )}
                      {ev.status && <StatusBadge status={ev.status} />}
                    </div>
                    {ev.detail && (
                      <div className="mt-0.5 truncate font-mono text-xs text-muted-foreground/80">
                        {ev.detail}
                      </div>
                    )}
                  </div>

                  {/* Agent link */}
                  {ev.agentId && (
                    <Link
                      to={`/beacons/${ev.agentId}`}
                      className="shrink-0 rounded-md bg-primary/10 px-1.5 py-0.5 font-mono text-xs text-primary transition-colors hover:bg-primary/15"
                    >
                      {ev.agentId.slice(0, 8)}
                    </Link>
                  )}
                </li>
              );
            })}
          </ul>
          {hidden > 0 && (
            // Rendering used to stop at 80 rows while the header kept counting
            // every event, so a feed of 500 said "500 events" and showed 80 with
            // no indication that 420 were hidden. The count is now split into
            // shown/total and the remainder is one click away.
            <div className="flex items-center justify-center gap-3 border-t border-border/50 px-4 py-3">
              <span className="text-xs text-muted-foreground">
                {t("events.showing_of", { shown: visible.length, total: rows.length })}
              </span>
              <Button variant="outline" size="sm" onClick={() => setVisibleCount((n) => n + PAGE_SIZE)}>
                {t("events.show_more", { count: hidden })}
              </Button>
            </div>
          )}
        </DataState>
      </Card>
    </div>
  );
}
