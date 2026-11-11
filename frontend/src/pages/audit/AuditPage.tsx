
import { useEffect, useState, useCallback } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { toast } from "sonner";
import { downloadText } from "@/lib/download";
import { enumLabel } from "@/lib/utils";
import { PageContainer } from "@/components/ui/page-container";
import { useI18n } from "@/lib/i18n";
import { Card } from "@/components/ui/card";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { Button } from "@/components/ui/button";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { Badge } from "@/components/ui/badge";
import { Download, FileText, Filter, ShieldCheck, Terminal } from "lucide-react";
import { StatTile } from "@/components/ui/stat-tile";
import { IconBadge } from "@/components/ui/icon-badge";
import type { AuditLog } from "@/types/audit";
import { useInteractStore } from "@/lib/interact-store";
import { auditActionLabel, auditActionTone, auditEventsQuery, auditLogsToCSV, auditSessionId, normalizeAuditLogs } from "./components/audit-log";
import { LoginLogPanel } from "./components/LoginLogPanel";
import { OperationsPanel } from "./components/OperationsPanel";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";

const SEVERITY_BADGES: Record<string, string> = {
  info: "secondary",
  warning: "warning",
  error: "destructive",
  critical: "destructive",
};



export default function AuditPage() {
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [perPage] = useState(50);
  const [userFilter, setUserFilter] = useState("");
  const [actionFilter, setActionFilter] = useState("");
  const [appliedFilters, setAppliedFilters] = useState({ user: "", action: "" });
  const [selectedLog, setSelectedLog] = useState<AuditLog | null>(null);
  const [users, setUsers] = useState<{ username: string }[]>([]);
  const [verifyState, setVerifyState] = useState<{
    running: boolean;
    result: { ok: boolean; checked: number; first_bad_id: number; reason: string } | null;
  }>({ running: false, result: null });
  // The page header's export belongs to the events table. It must disappear on
  // the login tab: otherwise two identical "Export page CSV" buttons are
  // visible and the header one silently exports a table the operator cannot
  // see.
  const [searchParams, setSearchParams] = useSearchParams();
  // The tab lives in the URL. A drill-down from the dashboard has to land on
  // the right tab even when the operator is already on the audit page, and
  // the address has to survive a reload or a shared link.
  const requestedTab = searchParams.get("tab");
  const tab = requestedTab === "operations" || requestedTab === "logins" ? requestedTab : "events";
  const initialAgentId = searchParams.get("agent_id") || "";
  const initialResult = searchParams.get("result") || "";
  const initialCategory = searchParams.get("category") || "";
  const initialAction = searchParams.get("action") || "";
  const initialRange = searchParams.get("range") || "";
  const { t } = useI18n();

  const handleTabChange = useCallback(
    (next: string) => {
      const params = new URLSearchParams(searchParams);
      if (next === "events") params.delete("tab");
      else params.set("tab", next);
      setSearchParams(params);
    },
    [searchParams, setSearchParams],
  );

  useEffect(() => {
    const controller = new AbortController();
    api.get(paths.users.list, { signal: controller.signal })
      .then((data) => {
        const list = (data.users || data.data || []) as { username: string }[];
        setUsers(list);
      })
      .catch(() => {
        // Non-fatal: user filter can still use free text
      });
    return () => controller.abort();
  }, []);

  const loadLogs = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.get(paths.audit.logs(auditEventsQuery(appliedFilters, page, perPage)), { signal });
      const payload = (data && typeof data === "object") ? data as Record<string, unknown> : {};
      setLogs(normalizeAuditLogs(payload.logs));
      setTotal(Number(payload.total) || 0);
    } catch (e) {
      if ((e as Error).name === "AbortError") return;
      setLogs([]);
      setTotal(0);
      const msg = t("audit.toast.load_failed");
      setError(msg);
      toast.error(msg);
    } finally {
      setLoading(false);
    }
  }, [page, perPage, appliedFilters, t]);

  useEffect(() => {
    const controller = new AbortController();
    loadLogs(controller.signal);
    return () => controller.abort();
  }, [loadLogs]);

  const applyFilters = () => {
    setPage(1);
    setAppliedFilters({ user: userFilter, action: actionFilter });
  };
  const resetFilters = () => {
    setUserFilter("");
    setActionFilter("");
    setPage(1);
    setAppliedFilters({ user: "", action: "" });
  };

  const handleExport = () => {
    downloadText(
      auditLogsToCSV(logs.filter(Boolean)),
      `audit-logs-page-${page}.csv`,
      "text/csv;charset=utf-8",
    );
  };

  const verifyChain = async () => {
    setVerifyState({ running: true, result: null });
    try {
      const data = await api.get(paths.audit.verify("limit=5000"));
      const payload = (data && typeof data === "object" ? data : {}) as Record<string, unknown>;
      setVerifyState({
        running: false,
        result: {
          ok: Boolean(payload.ok),
          checked: Number(payload.checked) || 0,
          first_bad_id: Number(payload.first_bad_id) || 0,
          reason: typeof payload.reason === "string" ? payload.reason : "",
        },
      });
    } catch {
      setVerifyState({ running: false, result: null });
      toast.error(t("audit.verify_error"));
    }
  };

  const getSeverityBadge = (severity: string) => {
    const s = (severity || "").toLowerCase();
    return SEVERITY_BADGES[s] || "secondary";
  };

  const openSession = (agentId: string) => {
    useInteractStore.getState().open(agentId);
  };

  const getLogField = (log: AuditLog | null, field: keyof AuditLog | "severity") => {
    if (!log) return "";
    switch (field) {
      case "id": return String(log.id || "");
      case "timestamp": return String(log.timestamp || "");
      case "username": return String(log.username || "-");
      case "action": return String(log.action || "-");
      case "resource": return String(log.resource || "-");
      case "target": return String(log.target || "-");
      case "status": return String(log.status || "-");
      case "details": return String(log.details || "-");
      case "ip": return String(log.ip || "-");
      case "severity": return String(log.severity || "info");
      case "agent_id": return String(log.agent_id || "");
      default: return "";
    }
  };

  return (
    <PageContainer
      title={t("audit.title")}
      subtitle={t("audit.subtitle")}
      actions={
        tab === "events" ? (
          <div className="flex flex-wrap items-center gap-2">
            <Button
              variant="outline"
              onClick={() => void verifyChain()}
              disabled={verifyState.running}
              title={t("audit.verify_hint")}
            >
              <ShieldCheck className="size-4" />
              <span>{verifyState.running ? t("audit.verify_running") : t("audit.verify_chain")}</span>
            </Button>
            <Button onClick={handleExport} disabled={loading || logs.length === 0}>
              <Download className="size-4" />
              <span>{t("audit.export_page_csv")}</span>
            </Button>
          </div>
        ) : null
      }
    >

      <Tabs value={tab} onValueChange={handleTabChange}>
        <TabsList>
          <TabsTrigger value="events">{t("audit.tab_events")}</TabsTrigger>
          <TabsTrigger value="operations">{t("audit.tab_operations")}</TabsTrigger>
          <TabsTrigger value="logins">{t("audit.tab_logins")}</TabsTrigger>
        </TabsList>

        <TabsContent value="events" className="space-y-4">
      {verifyState.result && (
        <div className="flex flex-wrap items-center gap-2">
          {verifyState.result.ok ? (
            <Badge variant="success">{t("audit.verify_ok", { n: verifyState.result.checked })}</Badge>
          ) : (
            <Badge variant="destructive" title={verifyState.result.reason}>
              {t("audit.verify_failed", { id: verifyState.result.first_bad_id })}
            </Badge>
          )}
        </div>
      )}

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 sm:gap-5">
        <Card className="p-(--card-spacing)">
          <div className="flex items-center justify-between">
            <StatTile label={t("audit.total_records")} value={total} />
            <IconBadge icon={FileText} color="primary" size="xl" />
          </div>
        </Card>
      </div>

      <PageToolbar sticky>
          <Select value={actionFilter || "all"} onValueChange={(v) => setActionFilter(v === "all" || !v ? "" : v)}>
            <SelectTrigger className="w-[180px]">
              <SelectValue>
                {(value) => (value === "all" ? t("audit.all_actions") : enumLabel(t, "audit", String(value ?? "")))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("audit.all_actions")}</SelectItem>
              <SelectItem value="login">{t("audit.login")}</SelectItem>
              <SelectItem value="logout">{t("audit.logout")}</SelectItem>
              <SelectItem value="create">{t("audit.create")}</SelectItem>
              <SelectItem value="update">{t("audit.update")}</SelectItem>
              <SelectItem value="delete">{t("audit.delete")}</SelectItem>
              <SelectItem value="failed">{t("audit.failed")}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={userFilter || "all"} onValueChange={(v) => setUserFilter(v === "all" || !v ? "" : v)}>
            <SelectTrigger className="w-[180px]">
              <SelectValue>
                {(value) => (value === "all" ? t("audit.all_users") : String(value ?? ""))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("audit.all_users")}</SelectItem>
              {users.map((u) => (
                <SelectItem key={u.username} value={u.username}>{u.username}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button onClick={applyFilters} disabled={loading}>
            <Filter className="size-4" />
            <span>{t("audit.apply")}</span>
          </Button>
          <Button variant="outline" onClick={resetFilters}>
            {t("audit.reset")}
          </Button>
      </PageToolbar>

      <Card className="overflow-hidden">
        <DataTable<AuditLog>
          data={logs.filter(Boolean)}
          label={t("audit.title")}
          loading={loading}
          error={error}
          onRetry={() => loadLogs()}
          emptyTitle={t("audit.empty")}
          columns={[
            {
              id: "timestamp",
              header: t("audit.time"),
              sortValue: (log) => log.timestamp || "",
              cell: (log) => <span className="text-xs font-mono whitespace-nowrap">{getLogField(log, "timestamp")}</span>,
            },
            {
              id: "username",
              header: t("audit.user"),
              sortValue: (log) => log.username || "",
              cell: (log) => <span className="text-sm font-medium text-muted-foreground">{getLogField(log, "username")}</span>,
            },
            {
              id: "ip",
              header: "IP",
              cell: (log) => <span className="text-xs font-mono">{getLogField(log, "ip")}</span>,
            },
            {
              id: "action",
              header: t("audit.action"),
              sortValue: (log) => getLogField(log, "action"),
              cell: (log) => {
                const raw = getLogField(log, "action");
                return (
                  <div className="min-w-0">
                    <Badge variant={auditActionTone(raw)}>{auditActionLabel(t, raw)}</Badge>
                    <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground" title={raw}>{raw}</div>
                  </div>
                );
              },
            },
            {
              id: "severity",
              header: t("audit.severity"),
              sortValue: (log) => getLogField(log, "severity"),
              cell: (log) => (
                <Badge variant={getSeverityBadge(getLogField(log, "severity")) as "secondary" | "warning" | "destructive"}>
                  {getLogField(log, "severity")}
                </Badge>
              ),
            },
            {
              id: "resource",
              header: t("audit.resource"),
              cell: (log) => <span className="text-xs max-w-[200px] truncate">{getLogField(log, "resource")}</span>,
              cellClassName: "max-w-[200px]",
            },
            {
              id: "target",
              header: t("audit.col_target"),
              cell: (log) => <span className="text-xs font-mono">{getLogField(log, "target")}</span>,
            },
            {
              id: "status",
              header: t("audit.status"),
              cell: (log) => (
                <Badge variant={getLogField(log, "status").toLowerCase().includes("fail") ? "destructive" : "success"}>
                  {getLogField(log, "status")}
                </Badge>
              ),
            },
            {
              id: "details",
              header: t("audit.details"),
              cell: (log) => <span className="text-xs max-w-[300px] truncate">{getLogField(log, "details")}</span>,
              cellClassName: "max-w-[300px]",
            },
            {
              id: "interact",
              header: t("audit.interact"),
              align: "right",
              cell: (log) => {
                const sessionId = auditSessionId(log);
                return sessionId ? (
                  <Button
                    type="button"
                    variant="ghost"
                    size="xs"
                    className="gap-1"
                    onClick={(e) => {
                      e.stopPropagation();
                      openSession(sessionId);
                    }}
                  >
                    <Terminal aria-hidden="true" className="size-3.5" />
                    {t("audit.interact")}
                  </Button>
                ) : null;
              },
            },
          ]}
          rowKey={(log, i) => getLogField(log, "id") || String(i)}
          onRowClick={(log) => setSelectedLog(log)}
          pagination={{ page, pageSize: perPage, total, onPageChange: setPage }}
        />
      </Card>

      <Dialog open={!!selectedLog} onOpenChange={() => setSelectedLog(null)}>
        <DialogContent size="lg" className="max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("audit.detail_title")}</DialogTitle>
          </DialogHeader>
          {selectedLog && (<div className="space-y-4">
            {auditSessionId(selectedLog) && (
              <Button
                type="button"
                className="gap-1"
                onClick={() => {
                  openSession(auditSessionId(selectedLog));
                  setSelectedLog(null);
                }}
              >
                <Terminal aria-hidden="true" className="size-4" />
                {t("audit.interact")}
              </Button>
            )}
            {(["timestamp", "username", "ip", "action", "severity", "resource", "target", "status", "details"] as const).map(field => (
              <div key={field}>
                <span className="text-xs font-medium text-muted-foreground uppercase tracking-wider">{field}</span>
                <p className={`text-sm mt-0.5 ${field === "action" || field === "severity" || field === "status" ? "" : "text-muted-foreground"}`}>
                  {field === "action" ? (
                    <>
                      <Badge variant={auditActionTone(getLogField(selectedLog, field))}>
                        {auditActionLabel(t, getLogField(selectedLog, field))}
                      </Badge>
                      <div className="mt-1 font-mono text-xs text-muted-foreground">{getLogField(selectedLog, field)}</div>
                    </>
                  ) : field === "severity" ? (
                    <Badge variant={getSeverityBadge(getLogField(selectedLog, field)) as "secondary" | "warning" | "destructive"}>
                      {selectedLog.severity || "info"}
                    </Badge>
                  ) : field === "status" ? (
                    <Badge variant={(getLogField(selectedLog, field)).toLowerCase().includes("fail") ? "destructive" : "success"}>
                      {getLogField(selectedLog, field)}
                    </Badge>
                  ) : (
                    <span className="font-mono">{getLogField(selectedLog, field)}</span>
                  )}
                </p>
              </div>
            ))}
          </div>)}
        </DialogContent>
      </Dialog>
        </TabsContent>

        <TabsContent value="operations">
          {/* The key carries the deep-link filters: a new selection in the URL
              has to rebuild the panel, because the panel reads its filters
              once when it mounts. */}
          <OperationsPanel
            key={`${initialAgentId}|${initialResult}|${initialCategory}|${initialAction}|${initialRange}`}
            users={users}
            initialAgentId={initialAgentId}
            initialResult={initialResult}
            initialCategory={initialCategory}
            initialAction={initialAction}
            initialRange={initialRange}
          />
        </TabsContent>

        <TabsContent value="logins">
          <LoginLogPanel
            key={`${initialResult}|${initialRange}`}
            initialResult={initialResult}
            initialRange={initialRange}
          />
        </TabsContent>
      </Tabs>
    </PageContainer>
  );
}
