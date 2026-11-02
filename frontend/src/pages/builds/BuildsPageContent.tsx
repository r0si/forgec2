import { PageContainer } from "@/components/ui/page-container";
import { ErrorState } from "@/components/ui/error-state";

import { useEffect, useState, useCallback, useRef } from "react";
import { Link } from "react-router-dom";
import { downloadBlob } from "@/lib/download";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { firstNumber, normalizeListEnvelope } from "@/lib/envelope";
import { toast } from "sonner";
import { useWS } from "@/lib/wsContext";
import { useUrlState } from "@/lib/hooks/useUrlState";
import { useRefreshGate } from "@/lib/hooks/useRefreshGate";
import { formatTime } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { Spinner } from "@/components/ui/spinner";
import { Pagination } from "@/components/ui/pagination";
import { StatCard } from "@/components/ui/animated-stat-card";
import { DataState } from "@/components/ui/data-state";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Skeleton } from "@/components/ui/skeleton";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Progress } from "@/components/ui/progress";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Activity, AlertCircle, AlertTriangle, Apple, Calendar, CheckCircle, Clock, Cpu, Download, File, Filter, Hammer, Monitor, Plus, RefreshCw, Terminal, Timer, User, X, XCircle } from "lucide-react";
import EffectivenessCard from "./components/EffectivenessCard";
import BuildCacheCard from "./components/BuildCacheCard";

interface BuildLog {
  id?: string;
  ID?: string;
  created_at?: string;
  CreatedAt?: string;
  platform?: string;
  Platform?: string;
  format?: string;
  Format?: string;
  c2_url?: string;
  C2URL?: string;
  filename?: string;
  Filename?: string;
  user?: string;
  User?: string;
  status?: string;
  Status?: string;
  error?: string;
  Error?: string;
  stdout?: string;
  Stdout?: string;
  stderr?: string;
  Stderr?: string;
  started_at?: string;
  StartedAt?: string;
  completed_at?: string;
  CompletedAt?: string;
  duration?: string;
  Duration?: string;
  artifact_path?: string;
  ArtifactPath?: string;
}

const PLATFORMS = ["all", "windows", "linux", "macos"];

const PAGE_SIZE = 10;

// dedupeBuilds keeps the first occurrence of each build id (order-stable) so
// builds arriving both via the list API and via WS-driven refreshes never
// render duplicates. Entries without an id are preserved as-is.
function dedupeBuilds(logs: BuildLog[]): BuildLog[] {
  const seen = new Set<string>();
  const out: BuildLog[] = [];
  for (const l of logs) {
    const id = l.id ?? l.ID;
    if (id) {
      if (seen.has(id)) continue;
      seen.add(id);
    }
    out.push(l);
  }
  return out;
}

function getDuration(start?: string, end?: string): string {
  if (!start) return "-";
  const s = new Date(start).getTime();
  const e = end ? new Date(end).getTime() : Date.now();
  // new Date(garbage) yields NaN without throwing — the old try/catch never
  // fired and the render produced "NaNs"/"NaNm NaNs".
  if (!Number.isFinite(s) || !Number.isFinite(e)) return "-";
  const diff = Math.max(0, e - s);
  const min = Math.floor(diff / 60000);
  const sec = Math.floor((diff % 60000) / 1000);
  if (min > 0) return `${min}m ${sec}s`;
  return `${sec}s`;
}

function getStatusInfo(status: string, t: (key: string, vars?: Record<string, string | number>) => string) {
  switch (status) {
    case "success": return { icon: <CheckCircle className="size-4 text-primary" />, label: t("builds.success"), bg: "bg-success" };
    case "failed": return { icon: <XCircle className="size-4 text-destructive-ink" />, label: t("builds.failed"), bg: "bg-destructive" };
    case "building": return { icon: <Spinner size="sm" />, label: t("builds.building"), bg: "bg-primary" };
    default: return { icon: <Clock className="size-4 text-muted-foreground" />, label: t("builds.pending"), bg: "bg-muted-foreground" };
  }
}

export default function BuildsPage({ embedded = false }: { embedded?: boolean }) {
  const [builds, setBuilds] = useState<BuildLog[]>([]);
  const [total, setTotal] = useState(0);
  const [successCount, setSuccessCount] = useState(0);
  const [failedCount, setFailedCount] = useState(0);
  const [avgDuration, setAvgDuration] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [filterPlatform, setFilterPlatform] = useUrlState("platform", "all", PLATFORMS as readonly string[]);
  const [filterStatus, setFilterStatus] = useUrlState("status", "", ["", "success", "failed", "building"] as const);
  const [expandedBuild, setExpandedBuild] = useState<string | null>(null);
  const [versionDist, setVersionDist] = useState<{ version: string; count: number }[]>([]);
  const [page, setPage] = useState(1);
  const { t } = useI18n();

  // Generation counter: rapid filter changes must not let a stale slower
  // response overwrite the fresh one. Also throttles error toasts so WS
  // reconnect bursts don't spam one toast per failed refresh.
  const loadGenRef = useRef(0);
  const lastErrorToastRef = useRef(0);
  // WebSocket refreshes must not replace the rendered build cards with
  // skeletons, and a failed refresh must not reset the success-rate KPI to a
  // fake 0%. Only the initial load and filter changes show progress.
  const { beginRefresh, markLoaded } = useRefreshGate();

  const loadBuilds = useCallback(async (opts?: { showLoading?: boolean }) => {
    const gen = ++loadGenRef.current;
    if (beginRefresh(opts?.showLoading)) setLoading(true);
    setError(null);
    try {
      const params = new URLSearchParams();
      if (filterPlatform !== "all") params.set("platform", filterPlatform);
      if (filterStatus) params.set("status", filterStatus);
      const [data, agents] = await Promise.all([
        api.get(paths.builds.list(params.toString())),
        fetchAgentListCached().catch(() => []),
      ]);
      if (gen !== loadGenRef.current) return;
      const logs = dedupeBuilds(normalizeListEnvelope(data, ["logs", "Logs", "builds", "data"]) as BuildLog[]);
      setBuilds(logs);
      setTotal(firstNumber(data, ["total", "Total"], logs.length));
      const success = firstNumber(
        data,
        ["success_count", "SuccessCount"],
        logs.filter((l: BuildLog) => (l.status || l.Status) === "success").length,
      );
      const failed = firstNumber(
        data,
        ["failed_count", "FailedCount"],
        logs.filter((l: BuildLog) => (l.status || l.Status) === "failed").length,
      );
      setSuccessCount(success);
      setFailedCount(failed);
      const durations = logs.filter((l: BuildLog) => l.duration || l.Duration).map((l: BuildLog) => {
        const d = (l.duration || l.Duration || "0").replace("s", "");
        return parseFloat(d) || 0;
      });
      setAvgDuration(durations.length > 0 ? Math.round(durations.reduce((a: number, b: number) => a + b, 0) / durations.length) : 0);
      if (agents.length > 0) {
        const counts: Record<string, number> = {};
        agents.forEach(a => { const v = a.version || "unknown"; counts[v] = (counts[v] || 0) + 1; });
        setVersionDist(Object.entries(counts).map(([version, count]) => ({ version, count })).sort((a, b) => b.count - a.count));
      }
      markLoaded();
    } catch (e) {
      if (gen !== loadGenRef.current) return;
      // Keep the last good rows and counters: zeroing them turned a transient
      // refresh failure into a fabricated "0 builds, 0% success" reading.
      const msg = e instanceof Error ? e.message : t("builds.toast.load_failed");
      setError(msg);
      if (Date.now() - lastErrorToastRef.current > 30_000) {
        lastErrorToastRef.current = Date.now();
        toast.error(msg);
      }
    } finally {
      if (gen === loadGenRef.current) setLoading(false);
    }
  }, [filterPlatform, filterStatus, t, beginRefresh, markLoaded]);

  useEffect(() => { loadBuilds({ showLoading: true }); }, [loadBuilds]);

  const pageCount = Math.max(1, Math.ceil(builds.length / PAGE_SIZE));
  const currentPage = Math.min(page, pageCount);
  const paginatedBuilds = builds.slice((currentPage - 1) * PAGE_SIZE, currentPage * PAGE_SIZE);

  const changePlatform = (p: string) => { setFilterPlatform(p); setPage(1); };
  const changeStatus = (s: string) => { setFilterStatus(s as "" | "success" | "failed" | "building"); setPage(1); };
  const clearFilters = () => { setFilterStatus(""); setFilterPlatform("all"); setPage(1); };

  // Real-time build updates: refresh when any async build finishes, when the
  // WS reconnects (sync snapshot), or when the socket first connects. A short
  // debounce coalesces bursts (e.g. parallel builds completing together).
  const { connected, subscribe } = useWS();
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const scheduleRefresh = useCallback(() => {
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
    refreshTimer.current = setTimeout(loadBuilds, 250);
  }, [loadBuilds]);
  useEffect(() => {
    if (connected) scheduleRefresh();
  }, [connected, scheduleRefresh]);
  useEffect(() => {
    const unsub = subscribe((msg) => {
      if (msg.type === "build_update" || msg.type === "sync") {
        if (msg.type === "build_update" && msg.status === "building") return;
        scheduleRefresh();
      }
    });
    return () => {
      unsub();
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
    };
  }, [subscribe, scheduleRefresh]);

  const handleDownload = async (build: BuildLog) => {
    const buildId = build.id;
    if (!buildId) return;
    try {
      const { blob, filename } = await api.downloadGet(
        paths.builds.download(String(buildId)),
        build.filename || `build-${buildId}`,
      );
      downloadBlob(blob, filename);
    } catch {
      toast.error(t("builds.toast.download_failed"));
    }
  };

  const platformIcon = (p: string) => {
    switch (p) {
      case "windows": return <Monitor className="size-3 text-muted-foreground" />;
      case "linux": return <Terminal className="size-3 text-muted-foreground" />;
      case "macos": return <Apple className="size-3 text-muted-foreground" />;
      default: return <Cpu className="size-3 text-muted-foreground" />;
    }
  };

  const platformColor = (p: string) => {
    switch (p) {
      case "linux": return "success";
      case "windows": return "default";
      case "macos": return "secondary";
      default: return "outline";
    }
  };

  return (
    <PageContainer embedded={embedded} title={t("builds.title")} subtitle={`${t("builds.subtitle")} · ${successCount} ${t("builds.success")} · ${failedCount} ${t("builds.failed")} · Avg ${avgDuration}s`}>
        {!embedded && (
          <div className={`flex items-center gap-2 ${embedded ? "ml-auto" : ""}`}>
            <Button render={<Link to="/payload-generator" />}>
              <Plus className="size-4" /> {t("builds.new_build")}
            </Button>
            <Button variant="secondary" onClick={() => loadBuilds({ showLoading: true })}>
              <RefreshCw className="size-4" /> {t("builds.refresh")}
            </Button>
          </div>
        )}

      <div className="grid grid-cols-1 sm:grid-cols-4 gap-4 mb-4">
        <StatCard label={t("builds.total_builds")} value={total} color="primary" icon={<Hammer className="size-4" />} />
        <StatCard label={t("builds.success")} value={successCount} color="emerald" icon={<CheckCircle className="size-4" />} />
        <StatCard label={t("builds.failed")} value={failedCount} color="destructive" icon={<AlertCircle className="size-4" />} />
        <StatCard label={t("builds.success_rate")} value={total > 0 ? `${Math.round((successCount / total) * 100)}%` : "0%"} color="warning" icon={<Activity className="size-4" />} />
      </div>

      <EffectivenessCard />

      <BuildCacheCard />

      {versionDist.length > 0 && (
        <Card className="p-(--card-spacing) mb-4">
          <div className="flex items-center justify-between mb-3">
            <span className="text-sm font-semibold text-foreground flex items-center gap-2">
              <Cpu className="size-4" /> {t("builds.version_dist")}
            </span>
            <span className="text-xs text-muted-foreground">{versionDist.reduce((s, v) => s + v.count, 0)} {t("builds.agents_unit")}</span>
          </div>
          <div className="space-y-2">
            {versionDist.map(v => {
              const pct = Math.round((v.count / versionDist.reduce((s, x) => s + x.count, 0)) * 100);
              return (
                <div key={v.version} className="flex items-center gap-3">
                   <Tooltip>
                     <TooltipTrigger>
                       <span className="text-xs font-mono text-foreground w-32 truncate">{v.version}</span>
                     </TooltipTrigger>
                     <TooltipContent>{v.version}</TooltipContent>
                   </Tooltip>
                  <Progress value={pct} className="h-4 flex-1" />
                  <span className="text-xs text-muted-foreground w-16 text-right">{v.count} ({pct}%)</span>
                </div>
              );
            })}
          </div>
        </Card>
      )}

      <Card className="p-(--card-spacing) mb-4">
        <div className="flex flex-wrap items-center gap-3">
          <Filter className="size-4" />
          <span className="text-sm font-semibold text-muted-foreground">{t("builds.platform")}</span>
          <div className="flex flex-wrap gap-2">
            {PLATFORMS.map((p) => (
              <Button key={p} variant={filterPlatform === p ? "default" : "outline"} size="sm"
                onClick={() => changePlatform(p)}
                className="rounded-lg gap-1.5">
                {platformIcon(p)}
                {p === "all" ? t("builds.filter_all") : p.charAt(0).toUpperCase() + p.slice(1)}
              </Button>
            ))}
          </div>
          <span className="text-sm font-semibold text-muted-foreground ml-2">{t("builds.status")}</span>
          <Select value={filterStatus} onValueChange={(v) => changeStatus(v ?? "")}>
            <SelectTrigger className="w-full">
              <SelectValue placeholder={t("builds.filter_all")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("builds.filter_all")}</SelectItem>
              <SelectItem value="success">{t("builds.success")}</SelectItem>
              <SelectItem value="failed">{t("builds.failed")}</SelectItem>
              <SelectItem value="building">{t("builds.filter_building")}</SelectItem>
            </SelectContent>
          </Select>
          {(filterStatus || filterPlatform !== "all") && (
            <Button variant="outline" size="sm" onClick={clearFilters}
              className="rounded-lg">
              <X className="size-4" /> {t("builds.clear")}
            </Button>
          )}
        </div>
      </Card>

      <DataState
        loading={loading}
        error={error}
        onRetry={() => void loadBuilds()}
        empty={!loading && !error && builds.length === 0}
        emptyIcon={Hammer}
        emptyTitle={t("builds.empty")}
        emptyMessage={t("builds.generate_new_implant")}
        emptyAction={
          <Button render={<Link to="/payload-generator" />}>
            <Plus className="size-4" /> {t("builds.go_generate")}
          </Button>
        }
        loadingSkeleton={
          <div className="space-y-3">
            {[1, 2, 3].map((i) => (
              <Card key={i} className="p-(--card-spacing)">
                <div className="flex items-center gap-4">
                  <Skeleton className="size-10 rounded-lg" />
                  <div className="flex-1 space-y-2">
                    <Skeleton className="h-3 w-40" />
                    <Skeleton className="h-2 w-60" />
                  </div>
                </div>
              </Card>
            ))}
          </div>
        }
      >
      <div className="space-y-3">
          {paginatedBuilds.map((build, idx) => {
            const id = build.id || String(idx);
            const status = build.status || "unknown";
            const platform = build.platform || "";
            const info = getStatusInfo(status, t);
            const startedAt = build.started_at || build.created_at || "";
            const completedAt = build.completed_at || "";
            const stderr = build.stderr || "";
            const stdout = build.stdout || "";
            const error = build.error || "";
            const isExpanded = expandedBuild === id;
            const isBuilding = status === "building";

            return (
              <Card key={id} className="overflow-hidden shadow-sm hover:shadow-md transition-shadow duration-200">
                <div className="p-(--card-spacing)">
                  <div className="flex items-center gap-4">
                    <div className={`size-10 rounded-xl ring-1 ring-border/50 flex items-center justify-center ${status === "success" ? "bg-success/10" : status === "failed" ? "bg-destructive/10" : status === "building" ? "bg-primary/10" : "bg-secondary"}`}>
                      {isBuilding ? <Spinner size="xs" /> : info.icon}
                    </div>
                    <div className="flex-1 min-w-0">
                      <div className="flex items-center gap-2 flex-wrap">
                        <span className="text-sm font-semibold text-foreground">#{String(id).substring(0, 8)}</span>
                        <Badge variant={platformColor(platform) as "success" | "outline"} className="px-2 py-0.5 text-micro-sm rounded-lg">
                          {platformIcon(platform)} {platform || "unknown"}
                        </Badge>
                        <Badge variant="outline" className="px-2 py-0.5 text-micro-sm rounded-lg">{build.format || "-"}</Badge>
                        <Badge variant={status === "success" ? "success" : status === "failed" ? "destructive" : "outline"}
                          className="px-2 py-0.5 text-micro-sm rounded-full">
                          <span className={`size-1.5 rounded-full inline-block mr-1 ${info.bg} ${isBuilding ? "animate-pulse" : ""}`}></span>
                          {info.label}
                        </Badge>
                      </div>
                      <div className="flex items-center gap-4 mt-1.5 text-xs text-muted-foreground">
                        <span><Calendar className="size-4" />{formatTime(startedAt)}</span>
                        <span><Timer className="size-4" />{getDuration(startedAt, completedAt || (isBuilding ? undefined : completedAt))}</span>
                        {build.user ? <span><User className="size-4" />{build.user}</span> : null}
                        {build.filename ? <span className="truncate max-w-[200px]"><File className="size-4" />{build.filename}</span> : null}
                      </div>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <Tooltip>
                        <TooltipTrigger render={<Button variant="ghost" size="icon-sm" onClick={() => setExpandedBuild(isExpanded ? null : id)}
                            aria-label={t("builds.toggle_logs")} />}>
                          <Terminal className="size-4" />
                        </TooltipTrigger>
                        <TooltipContent>{t("builds.logs")}</TooltipContent>
                      </Tooltip>
                      {(status === "success" || build.artifact_path) && (
                        <Tooltip>
                          <TooltipTrigger render={<Button variant="ghost" size="icon-sm" onClick={() => handleDownload(build)}
                              className="text-primary hover:bg-primary/10 dark:hover:bg-primary/20"
                              aria-label={t("builds.download_artifact")} />}>
                            <Download className="size-4" />
                          </TooltipTrigger>
                          <TooltipContent>{t("builds.download_artifact")}</TooltipContent>
                        </Tooltip>
                      )}
                    </div>
                  </div>
                </div>

                {isExpanded && (
                  <div className="border-t border-border">
                    {(status === "failed" || error || stderr) && (
                      <div className="p-4 border-b border-destructive/20">
                        <ErrorState title={t("builds.error")} icon={AlertTriangle} message={<pre className="text-xs font-mono whitespace-pre-wrap break-all">{error || stderr || t("builds.unknown_error")}</pre>} />
                      </div>
                    )}
                    <div className="bg-card p-4 max-h-[400px] overflow-y-auto">
                      <div className="flex items-center justify-between mb-3">
                        <span className="text-xs font-semibold text-muted-foreground flex items-center gap-1.5">
                          <Terminal className="size-4" /> {t("builds.log_title")}
                        </span>
                        <span className="text-micro-sm text-muted-foreground font-mono">{stdout.split("\n").length} {t("builds.lines")}</span>
                      </div>
                      <pre className="text-xs font-mono text-muted-foreground whitespace-pre-wrap leading-relaxed">
                        {stdout || <span className="text-muted-foreground italic">{t("builds.no_output")}</span>}
                      </pre>
                    </div>
                    {build.c2_url ? (
                      <div className="px-4 py-3 bg-muted border-t border-border flex items-center justify-between">
                        <span className="text-xs text-muted-foreground font-mono truncate">{build.c2_url}</span>
                      </div>
                    ) : null}
                  </div>
                )}
              </Card>
            );
          })}
      </div>
      </DataState>
      <Pagination page={currentPage} pageSize={PAGE_SIZE} total={builds.length} onPageChange={setPage} />
    </PageContainer>
  );
}

