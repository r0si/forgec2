
import { useEffect, useMemo, useState, useRef } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { POLL } from "@/lib/polling";
import { formatTime, formatBytes } from "@/lib/utils";
import { PageContainer } from "@/components/ui/page-container";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { DataState } from "@/components/ui/data-state";
import { useI18n } from "@/lib/i18n";
import { Card } from "@/components/ui/card";
import { StatTile } from "@/components/ui/stat-tile";
import { Button } from "@/components/ui/button";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import { Checkbox } from "@/components/ui/checkbox";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Label } from "@/components/ui/label";
import { Skeleton } from "@/components/ui/skeleton";
import { ListX, Network, RefreshCw } from "lucide-react";
import { normalizeTrafficEntry, summarizeTraffic, type TrafficEntry } from "./traffic-data";

const EMPTY_TRAFFIC: TrafficEntry[] = [];

export default function TrafficPage({ embedded = false }: { embedded?: boolean } = {}) {
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [autoScroll, setAutoScroll] = useState(false);
  const [sourceIpFilter, setSourceIpFilter] = useState("");
  const containerRef = useRef<HTMLDivElement>(null);
  const { t } = useI18n();

  const { data, loading, error, refresh: loadTraffic, setData } = useApiResource<TrafficEntry[]>({
    fetcher: async () => {
      const data = await api.get(paths.traffic.api);
      const arr = Array.isArray(data) ? data : (data?.data ?? data?.traffic ?? []);
      const list = Array.isArray(arr) ? (arr as TrafficEntry[]) : [];
      return list.map(normalizeTrafficEntry);
    },
    pollMs: autoRefresh ? POLL.traffic : 0,
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("traffic.toast.load_failed"),
  });
  const entries = data ?? EMPTY_TRAFFIC;

  const { filteredEntries, sourceIps, totalRequests, beacons, errors, dataTransferred } = useMemo(
    () => summarizeTraffic(entries, sourceIpFilter),
    [entries, sourceIpFilter],
  );

  const isUserScrolledRef = useRef(false);
  useEffect(() => {
    const el = containerRef.current;
    if (!el) return;
    const onScroll = () => {
      isUserScrolledRef.current = el.scrollTop + el.clientHeight < el.scrollHeight - 20;
    };
    el.addEventListener("scroll", onScroll);
    return () => el.removeEventListener("scroll", onScroll);
  }, []);
  useEffect(() => {
    if (autoScroll && !isUserScrolledRef.current && containerRef.current) {
      containerRef.current.scrollTop = containerRef.current.scrollHeight;
    }
  }, [filteredEntries, autoScroll]);

  // View-only clear: the server keeps no deletable log, so pause
  // auto-refresh as well — otherwise the next poll silently repopulates.
  const clearLog = () => { setData([]); setAutoRefresh(false); };

  const getMethodStyle = (method: string) => {
    const m = method.toUpperCase();
    if (m === "POST") return "bg-success/10 text-success-ink";
    if (m === "GET") return "bg-info/10 text-info-ink";
    if (m === "BEACON") return "bg-chart-6/10 text-chart-6";
    if (m === "PUT") return "bg-warning/10 text-warning-ink";
    if (m === "DELETE") return "bg-destructive/10 text-destructive-ink";
    return "bg-muted text-muted-foreground";
  };

  return (
    <PageContainer embedded={embedded} title={!embedded ? t("traffic.title") : undefined} subtitle={!embedded ? `${t("traffic.request_log")} · C2 Beacon ${t("traffic.comm_record")}` : undefined} actions={<>
        <div className="flex items-center gap-2 flex-wrap">
          <Label className="flex items-center gap-x-2 text-sm cursor-pointer">
            <Checkbox checked={autoRefresh} onCheckedChange={setAutoRefresh} />
             {t("traffic.auto_refresh")}
          </Label>
          <Label className="flex items-center gap-x-2 text-sm cursor-pointer">
            <Checkbox checked={autoScroll} onCheckedChange={setAutoScroll} />
            {t("traffic.auto_scroll")}
          </Label>
          <Select value={sourceIpFilter || "__all"} onValueChange={v => setSourceIpFilter(v === "__all" ? "" : v ?? "")}>
            <SelectTrigger className="w-44 max-w-full">
              <SelectValue>
                {(value) => (value === "__all" ? t("traffic.all_ip") : String(value ?? ""))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="__all">{t("traffic.all_ip")}</SelectItem>
              {sourceIps.map(ip => (
                <SelectItem key={ip} value={ip}>{ip}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button onClick={loadTraffic} size="sm">
            <RefreshCw className="size-4" />
            <span>{t("traffic.refresh")}</span>
          </Button>
          <Button onClick={clearLog} size="sm" variant="outline" disabled={entries.length === 0}>
            <ListX className="size-4" />
            <span>{t("traffic.clear_view")}</span>
          </Button>
        </div>
      </>}>

      <Card className="p-(--card-spacing) shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4 md:gap-5">
          <StatTile centered label={t("traffic.total_requests")} value={totalRequests} tone="primary" />
          <StatTile centered label={t("traffic.beacons")} value={beacons} tone="violet" />
          <StatTile centered label={t("traffic.errors")} value={errors} tone="destructive" />
          <StatTile centered label={t("traffic.data_transfer")} value={formatBytes(dataTransferred)} tone="success" />
        </div>
      </Card>

      <Card className="overflow-hidden">
<CardHeaderRow accent={false} icon={Network} title={t("traffic.beacon_comm")} action={<span className="text-xs text-muted-foreground">{t("traffic.showing")} {filteredEntries.length} / {loading ? "..." : entries.length} {t("traffic.records")}</span>} />

        <DataState
          loading={loading}
          error={error}
          onRetry={loadTraffic}
          empty={!loading && !error && filteredEntries.length === 0}
          emptyIcon={Network}
          emptyTitle={t("traffic.empty")}
          loadingSkeleton={
            <Table label={t("traffic.beacon_comm")}>
              <TableHeader>
                <TableRow className="bg-muted border-b border-border sticky top-0">
                  <TableHead className="text-xs font-medium min-w-[80px]">{t("traffic.time")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[80px]">{t("traffic.col_method")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[200px]">{t("traffic.col_path")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[120px]">{t("traffic.col_source_ip")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[100px]">{t("traffic.col_agent")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[60px] text-center">{t("traffic.status")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[60px] text-right">{t("traffic.col_size")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[70px] text-right">{t("traffic.col_latency")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="font-mono">
                {[1, 2, 3, 4, 5].map(i => (
                  <TableRow key={i}>
                    {[1, 2, 3, 4, 5, 6, 7, 8].map(j => (
                      <TableCell key={j}><Skeleton className="h-3 w-16" /></TableCell>
                    ))}
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          }
        >
          <div ref={containerRef} className="max-h-[500px] overflow-y-auto">
            <Table label={t("traffic.beacon_comm")}>
              <TableHeader>
                <TableRow className="bg-muted border-b border-border sticky top-0">
                  <TableHead className="text-xs font-medium min-w-[80px]">{t("traffic.time")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[80px]">{t("traffic.col_method")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[200px]">{t("traffic.col_path")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[120px]">{t("traffic.col_source_ip")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[100px]">{t("traffic.col_agent")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[60px] text-center">{t("traffic.status")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[60px] text-right">{t("traffic.col_size")}</TableHead>
                  <TableHead className="text-xs font-medium min-w-[70px] text-right">{t("traffic.col_latency")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody className="font-mono">
                {filteredEntries.map((e, i) => {
                  const id = e.id || `${e.timestamp || ""}-${e.agent_id || ""}-${i}`;
                  const ts = e.timestamp || "";
                  const method = e.method || "";
                  const path = e.path || "";
                  const ip = e.source_ip || "";
                  const agent = e.agent_id || "";
                  const status = e.status_code ?? 0;
                  const size = e.size ?? 0;
                  const latency = e.latency ?? 0;

                  return (
                    <TableRow key={id}>
                      <TableCell className="text-xs text-muted-foreground">{formatTime(ts)}</TableCell>
                      <TableCell>
                        <span className={`px-2 py-0.5 rounded text-xs font-bold ${getMethodStyle(method)}`}>{method || "—"}</span>
                      </TableCell>
                      <TableCell className="text-xs text-muted-foreground max-w-[250px] truncate">{path}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">{ip}</TableCell>
                      <TableCell className="text-xs text-muted-foreground">{agent ? agent.substring(0, 8) : "-"}</TableCell>
                      <TableCell className="text-center">
                        <span className={`text-xs font-medium ${status === 0 ? "text-muted-foreground" : status >= 400 ? "text-destructive-ink" : status >= 300 ? "text-warning-ink" : "text-success-ink"}`}>{status || "—"}</span>
                      </TableCell>
                      <TableCell className="text-right text-xs text-muted-foreground">{formatBytes(size)}</TableCell>
                      <TableCell className="text-right text-xs text-muted-foreground">{latency}ms</TableCell>
                    </TableRow>
                  );
                })}
              </TableBody>
            </Table>
          </div>
        </DataState>
      </Card>
    </PageContainer>
  );
}

