
import { Link } from "react-router-dom";
import { memo, useMemo } from "react";
import { useAppStore } from "@/lib/store";
import { useI18n } from "@/lib/i18n";
import { timeAgo } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { ListRow } from "@/components/ui/list-row";
import { SectionCard } from "@/components/ui/section-card";
import { StatusBadge, StatusIndicator } from "@/components/ui/status-indicator";
import { StatTile } from "@/components/ui/stat-tile";
import { MetricGrid } from "@/components/ui/metric-grid";
import { DataError } from "@/components/ui/data-state";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import { Archive, Bug, Radio, Clock, ListChecks, Terminal, Wand2, AlertTriangle } from "lucide-react";
import type { DashboardStats } from "@/types/agent";
import { useOpsHomeData } from "./useOpsHomeData";
import {
  flattenLoot,
  LOOT_TAB,
  mergeAttention,
  pickUnhealthyListeners,
  splitSessions,
} from "./ops-home";
import { healthIndicatorStatus, translateHealthStatus } from "@/lib/listener-health";
import ActiveMissions from "./ActiveMissions";
import { AuditRiskStrip } from "./AuditRiskStrip";

function Panel({
  title,
  href,
  linkLabel,
  count,
  children,
}: {
  title: string;
  href: string;
  linkLabel: string;
  count?: number;
  children: React.ReactNode;
}) {
  return (
    <SectionCard title={title} href={href} linkLabel={linkLabel} count={count}>
      {children}
    </SectionCard>
  );
}

function QuickLaunch() {
  const { t } = useI18n();
  const actions = [
    { href: "/payload-generator", label: t("dashboard.quick_generate"), icon: <Wand2 className="size-5" /> },
    { href: "/listeners", label: t("dashboard.quick_listeners"), icon: <Radio className="size-5" /> },
    { href: "/beacons", label: t("dashboard.quick_agents"), icon: <Terminal className="size-5" /> },
  ];
  return (
    <SectionCard title={t("dashboard.quick_actions")}>
      <div className="grid grid-cols-2 gap-3 p-(--card-spacing) sm:grid-cols-3">
        {actions.map((a) => (
          <Button key={a.href} variant="outline" render={<Link to={a.href} />}
            className="h-auto flex-col gap-2 py-4 hover:border-primary hover:bg-primary/5">
            <span className="flex size-10 items-center justify-center rounded-lg bg-primary/10 text-primary transition-transform group-hover:scale-105">{a.icon}</span>
            <span className="text-sm font-medium text-foreground">{a.label}</span>
          </Button>
        ))}
      </div>
    </SectionCard>
  );
}

export default memo(function OpsHome() {
  const { t } = useI18n();
  const { agents, healthByTarget, failedTasks, pendingTasks, approvalTasks, loot, loading, error, partialFailures, refresh } = useOpsHomeData();

  const sessions = useMemo(() => splitSessions(agents), [agents]);
  const unhealthy = useMemo(() => pickUnhealthyListeners(healthByTarget), [healthByTarget]);
  const attention = useMemo(
    () => mergeAttention(failedTasks, pendingTasks, approvalTasks),
    [failedTasks, pendingTasks, approvalTasks],
  );
  const lootItems = useMemo(() => flattenLoot(loot), [loot]);
  const healthUnknown = partialFailures.includes(t("dashboard.source_listener_health"));

  return (
    <div className="flex flex-col gap-(--page-gap)">
      {error && <DataError message={error} onRetry={refresh} className="mb-2" />}

      {partialFailures.length > 0 && (
        <div role="alert" className="mb-2 flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("dashboard.partial_load_failed", { sources: partialFailures.join(", ") })}</span>
          <Button onClick={refresh} size="sm" variant="outline" className="min-h-11 px-4 sm:min-h-7 sm:px-2.5">{t("common.try_again")}</Button>
        </div>
      )}

      <DashboardStatTiles loading={loading} healthUnknown={healthUnknown} unhealthyCount={unhealthy.length} lootCount={lootItems.length} online={sessions.online.length} total={agents.length} />

      <AuditRiskStrip />

      <QuickLaunch />

      <div className="grid grid-cols-1 lg:grid-cols-2 gap-5">
        <Panel title={t("dashboard.sessions")} href="/beacons" linkLabel={t("dashboard.view_all")} count={agents.length}>
          {loading ? (
            <div className="space-y-2 p-(--card-spacing)"><Skeleton className="h-8" /><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : agents.length === 0 ? (
            <div className="p-(--card-spacing)"><EmptyState icon={Bug} title={t("dashboard.no_sessions")} /></div>
          ) : (
            <div className="grid grid-cols-1 sm:grid-cols-2 divide-y sm:divide-y-0 sm:divide-x divide-border">
              <div>
                <div className="px-4 py-1.5 text-micro-sm uppercase tracking-wider text-muted-foreground">{t("dashboard.sessions_online")}</div>
                {sessions.online.length === 0 ? (
                  <p className="px-4 py-3 text-xs text-muted-foreground">{t("dashboard.no_sessions")}</p>
                ) : sessions.online.map((a) => (
                  <ListRow
                    key={a.id}
                    to={`/beacons/${a.id}`}
                    leading={<StatusBadge status="online" variant="dotOnly" size="sm" />}
                    title={<span className="truncate font-mono text-sm">{a.hostname || a.id}</span>}
                    subtitle={`${a.username}${a.ip ? ` · ${a.ip}` : ""}`}
                    meta={timeAgo(a.last_seen, t)}
                  />
                ))}
              </div>
              <div>
                <div className="px-4 py-1.5 text-micro-sm uppercase tracking-wider text-muted-foreground">{t("dashboard.sessions_dropped")}</div>
                {sessions.dropped.length === 0 ? (
                  <p className="px-4 py-3 text-xs text-muted-foreground">{t("dashboard.no_sessions")}</p>
                ) : sessions.dropped.map((a) => (
                  <ListRow
                    key={a.id}
                    to={`/beacons/${a.id}`}
                    leading={<StatusBadge status={a.status} variant="dotOnly" size="sm" />}
                    title={<span className="truncate font-mono text-sm">{a.hostname || a.id}</span>}
                    subtitle={`${a.username}${a.ip ? ` · ${a.ip}` : ""}`}
                    meta={timeAgo(a.last_seen, t)}
                  />
                ))}
              </div>
            </div>
          )}
        </Panel>

        <Panel title={t("dashboard.listener_health")} href="/listeners" linkLabel={t("dashboard.view_all")} count={healthUnknown ? undefined : unhealthy.length}>
          {loading ? (
            <div className="space-y-2 p-(--card-spacing)"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : healthUnknown ? (
            <div className="p-(--card-spacing)"><EmptyState icon={AlertTriangle} title={t("dashboard.health_unknown")} message={t("dashboard.health_unknown_hint")} /></div>
          ) : unhealthy.length === 0 ? (
            <div className="p-(--card-spacing)"><EmptyState icon={Radio} title={t("dashboard.no_unhealthy_listeners")} /></div>
          ) : (
            <div className="divide-y divide-border">
              {unhealthy.map((h) => (
                <ListRow
                  key={h.target}
                  to={`/listeners/${h.target}`}
                  title={
                    <StatusIndicator
                      status={healthIndicatorStatus(h.status)}
                      variant="dot"
                      label={translateHealthStatus(t, h.status)}
                      pulse={h.status === "burned"}
                    />
                  }
                  subtitle={<span className="font-mono text-xs">{h.scheme || "http"}://{h.host}:{h.port}</span>}
                  meta={h.consecutive_fails ?? 0}
                  metaClassName="font-mono text-xs text-destructive-ink"
                />
              ))}
            </div>
          )}
        </Panel>

        <Panel title={t("dashboard.attention")} href="/events?tab=tasks" linkLabel={t("dashboard.view_all")} count={attention.length}>
          {loading ? (
            <div className="space-y-2 p-(--card-spacing)"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : attention.length === 0 ? (
            <div className="p-(--card-spacing)"><EmptyState icon={ListChecks} title={t("dashboard.no_attention")} /></div>
          ) : (
            <div className="divide-y divide-border">
              {attention.map((task) => (
                <ListRow
                  key={task.id}
                  to={`/events?tab=tasks&agent_id=${encodeURIComponent(task.agent_id || "")}`}
                  leading={
                    <StatusIndicator
                      status={task.status}
                      variant="dotOnly"
                      size="sm"
                      ariaLabel={task.status}
                    />
                  }
                  title={
                    <>
                      <span className="font-mono text-xs">{task.type}</span>
                      <span className="truncate text-xs text-muted-foreground">{task.command}</span>
                    </>
                  }
                  subtitle={task.agent_id}
                  meta={timeAgo(task.created_at, t)}
                />
              ))}
            </div>
          )}
        </Panel>

        <Panel title={t("dashboard.loot_inbox")} href="/loot" linkLabel={t("dashboard.view_all")} count={lootItems.length}>
          {loading ? (
            <div className="space-y-2 p-(--card-spacing)"><Skeleton className="h-8" /><Skeleton className="h-8" /></div>
          ) : lootItems.length === 0 ? (
            <div className="p-(--card-spacing)"><EmptyState icon={Archive} title={t("dashboard.no_loot")} /></div>
          ) : (
            <div className="divide-y divide-border">
              {lootItems.map((item) => (
                <ListRow
                  key={item.id}
                  to={`/loot?tab=${LOOT_TAB[item.kind]}`}
                  leading={
                    <Badge variant="secondary" className="text-micro-sm">
                      {item.kind === "screenshot" ? t("dashboard.loot_screenshot") : item.kind === "keylog" ? t("dashboard.loot_keylog") : t("dashboard.loot_download")}
                    </Badge>
                  }
                  title={<span className="truncate text-xs">{item.label}</span>}
                  meta={timeAgo(item.created_at, t)}
                />
              ))}
            </div>
          )}
        </Panel>
      </div>

      <ActiveMissions />
    </div>
  );
});

const DashboardStatTiles = memo(function DashboardStatTiles({ loading, healthUnknown, unhealthyCount, lootCount, online, total }: {
  loading: boolean;
  healthUnknown: boolean;
  unhealthyCount: number;
  lootCount: number;
  online: number;
  total: number;
}) {
  const { t } = useI18n();
  const stats = useAppStore((s) => s.stats);
  const s: Partial<DashboardStats> = stats ?? {};
  const pending = s.pending_tasks ?? 0;
  const failed = s.failed_tasks ?? 0;
  const totalTasks = s.total_tasks ?? 0;

  return (
    <MetricGrid count={5}>
      <Link to="/beacons">
        <Card interactive className="p-(--card-spacing) hover:ring-1 hover:ring-primary/20">
          <StatTile label={t("dashboard.beacons")} value={loading && !stats ? "…" : `${online}/${total}`} sub={online === total && total > 0 ? t("dashboard.all_online") : t("dashboard.online_suffix")} tone="success" icon={<Radio className="size-5" />} trend={total > 0 ? <span className="inline-flex items-center gap-1"><span className="size-1.5 rounded-full bg-success animate-pulse" />{t("dashboard.live_agents")}</span> : undefined} />
        </Card>
      </Link>
      <Link to="/events?tab=tasks">
        <Card interactive className="p-(--card-spacing) hover:ring-1 hover:ring-primary/20">
          <StatTile label={t("dashboard.pending_tasks_label")} value={loading && !stats ? "…" : pending} tone={pending > 0 ? "warning" : "muted"} icon={<Clock className="size-5" />} trend={totalTasks > 0 ? `${Math.round((pending/totalTasks)*100)}% ${t("dashboard.queue_share")}` : undefined} />
        </Card>
      </Link>
      <Link to="/events?tab=tasks">
        <Card interactive className="p-(--card-spacing) hover:ring-1 hover:ring-primary/20">
          <StatTile label={t("dashboard.failed_tasks")} value={loading && !stats ? "…" : failed} tone={failed > 0 ? "destructive" : "muted"} icon={<Bug className="size-5" />} trend={failed > 0 ? t("dashboard.needs_attention") : t("dashboard.all_clear")} />
        </Card>
      </Link>
      <Link to="/listeners">
        <Card interactive className="p-(--card-spacing) hover:ring-1 hover:ring-primary/20">
          <StatTile label={t("dashboard.unhealthy_count")} value={loading || healthUnknown ? "…" : unhealthyCount} tone={healthUnknown ? "muted" : unhealthyCount > 0 ? "destructive" : "success"} icon={<Radio className="size-5" />} trend={healthUnknown ? t("dashboard.health_unknown") : unhealthyCount === 0 ? t("dashboard.all_healthy") : undefined} />
        </Card>
      </Link>
      <Link to="/loot">
        <Card interactive className="p-(--card-spacing) hover:ring-1 hover:ring-primary/20">
          <StatTile label={t("dashboard.loot_inbox")} value={loading ? "…" : lootCount} sub={t("dashboard.loot_recent")} tone={lootCount > 0 ? "info" : "muted"} icon={<Archive className="size-5" />} />
        </Card>
      </Link>
    </MetricGrid>
  );
});
