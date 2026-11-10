import { registerFragment } from "@/lib/i18n";
import { enDashboard } from "@/lib/i18n/fragments/en.dashboard";
import { enListeners } from "@/lib/i18n/fragments/en.listeners";
import { enTasks } from "@/lib/i18n/fragments/en.tasks";
registerFragment(enDashboard);
registerFragment(enListeners);
registerFragment(enTasks);

import { useEffect } from "react";
import { DASHBOARD_RANGES } from "@/lib/shortcuts";
import { useUrlState } from "@/lib/hooks/useUrlState";
import { useAppStore } from "@/lib/store";
import { PageContainer } from "@/components/ui/page-container";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { cn } from "@/lib/utils";
import { DataError } from "@/components/ui/data-state";
import { useI18n } from "@/lib/i18n";
import { DASHBOARD_VIEWS, type DashboardView } from "./components/ops-home";
import OpsHome from "./components/OpsHome";
import AnalyticsView from "./components/AnalyticsView";

export default function DashboardPage() {
  const [view, setView] = useUrlState<DashboardView>("view", "ops", DASHBOARD_VIEWS);
  const [timeRange, setTimeRange] = useUrlState("range", "24h", DASHBOARD_RANGES);
  const { t } = useI18n();
  const stats = useAppStore((s) => s.stats);
  const statsError = useAppStore((s) => s.statsError);
  const fetchStats = useAppStore((s) => s.fetchStats);

  useEffect(() => {
    fetchStats();
  }, [fetchStats]);

  return (
    <PageContainer
      title={t("dashboard.title")}
      subtitle={view === "ops" ? t("dashboard.subtitle_ops") : t("dashboard.subtitle")}
      toolbar={
        <PageToolbar>
          <div className="inline-flex items-center gap-0.5 rounded-lg bg-secondary/70 p-0.5 ring-1 ring-border/50">
            {DASHBOARD_VIEWS.map((v) => (
              <Button
                key={v}
                type="button"
                variant="ghost"
                size="xs"
                onClick={() => setView(v)}
                className={cn(
                  "rounded-md px-2.5 py-1",
                  view === v ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                )}
              >
                {v === "ops" ? t("dashboard.view_ops") : t("dashboard.view_analytics")}
              </Button>
            ))}
          </div>
          {view === "analytics" && (
            <div className="inline-flex items-center gap-0.5 rounded-lg bg-secondary/70 p-0.5 ring-1 ring-border/50">
              {DASHBOARD_RANGES.map((r) => (
                <Button
                  key={r}
                  type="button"
                  variant="ghost"
                  size="xs"
                  onClick={() => setTimeRange(r)}
                  className={cn(
                    "rounded-md px-2.5 py-1 font-mono",
                    timeRange === r ? "bg-background text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
                  )}
                >
                  {r}
                </Button>
              ))}
            </div>
          )}
          {stats?.server_version && (
            <Badge variant="secondary" className="px-2.5 py-1 text-xs font-mono">
              v{stats.server_version}
            </Badge>
          )}
          {(stats?.protocol_outdated_agents ?? 0) > 0 && (
            <Badge variant="warning" className="px-2.5 py-1 text-xs font-mono">
              {t("dashboard.protocol_outdated", { n: stats?.protocol_outdated_agents ?? 0 })}
            </Badge>
          )}
        </PageToolbar>
      }
    >

      {statsError !== undefined && (
        <DataError
          message={statsError || t("dashboard.load_failed")}
          onRetry={() => { fetchStats(); }}
          onDismiss={() => useAppStore.setState({ statsError: undefined })}
          className="mb-4"
        />
      )}

      {view === "ops" ? <OpsHome /> : <AnalyticsView range={timeRange} stats={stats} />}
    </PageContainer>
  );
}
