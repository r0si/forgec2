import { useCallback, useEffect, useMemo, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Card } from "@/components/ui/card";
import { DataTable } from "@/components/ui/data-table";
import { Dialog, DialogContent, DialogHeader, DialogTitle } from "@/components/ui/dialog";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { StatTile } from "@/components/ui/stat-tile";
import { downloadBlob } from "@/lib/download";
import { useInteractStore } from "@/lib/interact-store";
import type { AuditLog } from "@/types/audit";
import {
  auditActionLabel,
  auditActionTone,
  auditSessionId,
  normalizeAuditLogs,
} from "./audit-log";
import { ActionPicker } from "./ActionPicker";
import { Download, Filter, RotateCw, Terminal } from "lucide-react";

const PER_PAGE = 50;

/** Category ids mirror `auditCategoryOrder` in the server classifier. */
const CATEGORY_IDS = [
  "auth",
  "agents",
  "tasks",
  "credentials",
  "files",
  "infrastructure",
  "automation",
  "ai",
  "tenants",
  "settings",
  "system",
  "other",
] as const;

const RANGE_IDS = ["24h", "7d", "30d", "all"] as const;
type RangeId = (typeof RANGE_IDS)[number];

/** Result ids mirror the server's audit filter ("success" / "failed"). */
const RESULT_IDS = ["success", "failed"] as const;

export interface OperationsDeepLink {
  result: string;
  category: string;
  action: string;
  range: RangeId;
}

/**
 * Read the filters a deep link carries and keep only the values the server
 * accepts.
 *
 * A link is written by another page or typed by a human, so it can carry
 * anything. An unknown category or result would be answered with 400 and the
 * operator would be left with an empty table and no explanation, so the
 * unknown values are dropped and the view lands on the unfiltered selection.
 */
export function normalizeOperationsDeepLink(input: {
  result?: string | null;
  category?: string | null;
  action?: string | null;
  range?: string | null;
}): OperationsDeepLink {
  const result = (input.result || "").trim().toLowerCase();
  const category = (input.category || "").trim().toLowerCase();
  const action = (input.action || "").trim();
  const range = (input.range || "").trim().toLowerCase();
  return {
    result: (RESULT_IDS as readonly string[]).includes(result) ? result : "",
    category: (CATEGORY_IDS as readonly string[]).includes(category) ? category : "",
    // The action is matched exactly server-side, and the server also accepts
    // the bare spelling of an "operator:"-prefixed action, so keep it as
    // written - but bounded, so a long pasted string cannot ride along.
    action: action.slice(0, 64),
    range: (RANGE_IDS as readonly string[]).includes(range) ? (range as RangeId) : "7d",
  };
}

interface AuditActionStat {
  action: string;
  category: string;
  count: number;
}

interface AuditCatalogue {
  actions: AuditActionStat[];
  categories: { id: string; count: number }[];
}

interface AppliedFilters {
  range: RangeId;
  category: string;
  action: string;
  result: string;
  user: string;
  search: string;
  agentId: string;
  includeSystem: boolean;
}

const EMPTY_FILTERS: AppliedFilters = {
  range: "7d",
  category: "",
  action: "",
  result: "",
  user: "",
  search: "",
  agentId: "",
  includeSystem: false,
};

export interface OperationsPanelProps {
  users?: { username: string }[];
  /** Beacon to pin the view to, set by the deep link from a beacon's page. */
  initialAgentId?: string;
  /** Filters carried by the deep link; unknown values are ignored. */
  initialResult?: string;
  initialCategory?: string;
  initialAction?: string;
  initialRange?: string;
}

/**
 * Operations log: who did what, to which target, and whether it worked.
 *
 * This is a curated read of the same hash-chained audit rows as "All events",
 * not a second store. The server classifies actions into categories, excludes
 * beacon/transport noise unless asked for it, and shares one filter
 * implementation between this table and the CSV export, so the file always
 * matches the screen.
 */
export function OperationsPanel({
  users = [],
  initialAgentId = "",
  initialResult,
  initialCategory,
  initialAction,
  initialRange,
}: OperationsPanelProps) {
  const { t } = useI18n();
  const initialTarget = initialAgentId.trim();
  const deepLink = useMemo(
    () =>
      normalizeOperationsDeepLink({
        result: initialResult,
        category: initialCategory,
        action: initialAction,
        range: initialRange,
      }),
    [initialResult, initialCategory, initialAction, initialRange],
  );
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [total, setTotal] = useState(0);
  const [failed, setFailed] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [catalogue, setCatalogue] = useState<AuditCatalogue>({ actions: [], categories: [] });
  const [selectedLog, setSelectedLog] = useState<AuditLog | null>(null);

  const [range, setRange] = useState<RangeId>(deepLink.range);
  const [category, setCategory] = useState(deepLink.category);
  const [action, setAction] = useState(deepLink.action);
  const [result, setResult] = useState(deepLink.result);
  const [user, setUser] = useState("");
  const [search, setSearch] = useState("");
  const [agentId, setAgentId] = useState(initialTarget);
  const [includeSystem, setIncludeSystem] = useState(false);
  const [applied, setApplied] = useState<AppliedFilters>({
    ...EMPTY_FILTERS,
    range: deepLink.range,
    category: deepLink.category,
    action: deepLink.action,
    result: deepLink.result,
    agentId: initialTarget,
  });

  useEffect(() => {
    const controller = new AbortController();
    // The catalogue is counted over the same window the table shows, so the
    // number next to a category is the number of rows behind it. A catalogue
    // counted over all time next to a seven-day table is a number the page
    // cannot honour.
    const params = new URLSearchParams();
    if (applied.range !== "all") params.set("since", applied.range);
    api.get(paths.audit.actions(params.toString()), { signal: controller.signal })
      .then((data) => {
        const payload = (data && typeof data === "object" ? data : {}) as Partial<AuditCatalogue>;
        setCatalogue({
          actions: Array.isArray(payload.actions) ? payload.actions : [],
          categories: Array.isArray(payload.categories) ? payload.categories : [],
        });
      })
      .catch((err) => {
        if ((err as Error).name === "AbortError") return;
        // The catalogue is a convenience: the table still works with free
        // category ids and the humanized action fallback.
        setCatalogue({ actions: [], categories: [] });
      });
    return () => controller.abort();
  }, [applied.range]);

  const queryFor = useCallback((filters: AppliedFilters, pageNumber?: number) => {
    const params = new URLSearchParams();
    if (pageNumber != null) {
      params.set("page", String(pageNumber));
      params.set("pageSize", String(PER_PAGE));
    }
    if (filters.user) params.set("user", filters.user);
    if (filters.action) params.set("action", filters.action);
    if (filters.category) params.set("category", filters.category);
    if (filters.result) params.set("result", filters.result);
    if (filters.search) params.set("search", filters.search);
    if (filters.agentId) params.set("agent_id", filters.agentId);
    if (filters.range !== "all") params.set("since", filters.range);
    params.set("scope", filters.includeSystem ? "all" : "operations");
    return params.toString();
  }, []);

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const data = await api.get(paths.audit.logs(queryFor(applied, page)), { signal });
      const payload = (data && typeof data === "object" ? data : {}) as Record<string, unknown>;
      setLogs(normalizeAuditLogs(payload.logs));
      setTotal(Number(payload.total) || 0);
      setFailed(Number(payload.failed) || 0);
    } catch (err) {
      if ((err as Error).name === "AbortError") return;
      setLogs([]);
      setTotal(0);
      setFailed(0);
      // Keep the key, not the translated string: `t` is not a dependency, so
      // a provider that recreates it cannot turn this into a refetch loop.
      setError("audit.ops_load_failed");
    } finally {
      setLoading(false);
    }
  }, [applied, page, queryFor]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const apply = () => {
    setPage(1);
    setApplied({
      range,
      category,
      action,
      result,
      user: user.trim(),
      search: search.trim(),
      agentId: agentId.trim(),
      includeSystem,
    });
  };

  const reset = () => {
    setRange("7d");
    setCategory("");
    setAction("");
    setResult("");
    setUser("");
    setSearch("");
    setAgentId("");
    setIncludeSystem(false);
    setPage(1);
    setApplied(EMPTY_FILTERS);
  };

  const clearTarget = () => {
    setAgentId("");
    setApplied((prev) => ({ ...prev, agentId: "" }));
    setPage(1);
  };

  const changeCategory = (value: string) => {
    setCategory(value);
    // An action from another category would silently AND with the new
    // category and return nothing; clear it instead of hiding the mismatch.
    if (value && action) {
      const stat = catalogue.actions.find((a) => a.action === action);
      if (stat && stat.category !== value) setAction("");
    }
  };

  // The chips are the category select, shortcut form: one click on the
  // category the operator already has in mind. The catalogue supplies the
  // counts, so a category nobody used yet is visible and not hidden.
  const pickCategory = (id: string) => {
    const next = applied.category === id ? "" : id;
    changeCategory(next);
    setPage(1);
    if (next) {
      // Applying straight away is the point of a chip; the pending selects
      // follow, so the visible filter and the request cannot disagree.
      const stat = catalogue.actions.find((a) => a.action === action);
      const nextAction = stat && stat.category !== next ? "" : action;
      setAction(nextAction);
      setApplied({
        range,
        category: next,
        action: nextAction,
        result,
        user: user.trim(),
        search: search.trim(),
        agentId: agentId.trim(),
        includeSystem,
      });
    } else {
      setApplied({
        ...applied,
        category: "",
        action: "",
      });
    }
  };

  const exportCsv = async () => {
    setExporting(true);
    setExportError(null);
    try {
      const { blob, filename } = await api.downloadGet(
        paths.audit.exportLogs(queryFor(applied)),
        "audit-operations.csv",
      );
      downloadBlob(blob, filename);
    } catch {
      // Keep this separate from the table error: a failed export must not
      // replace rows that loaded successfully with an error state.
      setExportError("audit.ops_export_failed");
    } finally {
      setExporting(false);
    }
  };

  const categoryLabel = (id: string) => {
    const key = `audit.cat.${id}`;
    const label = t(key);
    return label === key ? id : label;
  };

  const field = (log: AuditLog | null, key: keyof AuditLog | "severity") => {
    if (!log) return "";
    switch (key) {
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
    <div className="space-y-4">
      <div className="grid grid-cols-2 gap-4 sm:gap-5">
        <Card className="p-(--card-spacing)">
          <StatTile label={t("audit.ops_total")} value={total} />
        </Card>
        <Card className="p-(--card-spacing)">
          <StatTile
            label={t("audit.ops_failed")}
            value={<span className={failed > 0 ? "text-destructive-ink" : undefined}>{failed}</span>}
          />
        </Card>
      </div>

      <PageToolbar sticky>
        <Select value={range} onValueChange={(v) => setRange((v || "7d") as RangeId)}>
          <SelectTrigger className="w-[150px]" aria-label={t("audit.ops_range")}>
            <SelectValue>{(value) => t(`audit.ops_range_${value ?? "7d"}`)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {RANGE_IDS.map((id) => (
              <SelectItem key={id} value={id}>{t(`audit.ops_range_${id}`)}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select value={category || "all"} onValueChange={(v) => changeCategory(v === "all" || !v ? "" : v)}>
          <SelectTrigger className="w-[180px]" aria-label={t("audit.ops_category")}>
            <SelectValue>
              {(value) => (value === "all" ? t("audit.ops_all_categories") : categoryLabel(String(value ?? "")))}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("audit.ops_all_categories")}</SelectItem>
            {CATEGORY_IDS.map((id) => (
              <SelectItem key={id} value={id}>{categoryLabel(id)}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        <ActionPicker
          actions={catalogue.actions}
          value={action}
          onChange={(next) => {
            setAction(next);
            // An action from another category would silently AND with the
            // current category and return nothing; clear the category so the
            // visible filter and the request agree.
            if (next) {
              const stat = catalogue.actions.find((a) => a.action === next);
              if (category && stat && stat.category !== category) setCategory("");
            }
          }}
          category={category}
          allLabel={t("audit.ops_all_actions")}
          placeholder={t("audit.ops_action_search")}
          emptyLabel={t("audit.ops_action_none")}
        />

        <Select value={result || "all"} onValueChange={(v) => setResult(v === "all" || !v ? "" : v)}>
          <SelectTrigger className="w-[150px]" aria-label={t("audit.ops_result")}>
            <SelectValue>
              {(value) => (value === "all" ? t("audit.ops_all_results") : value === "failed" ? t("audit.login_failed") : t("audit.login_ok"))}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("audit.ops_all_results")}</SelectItem>
            <SelectItem value="success">{t("audit.login_ok")}</SelectItem>
            <SelectItem value="failed">{t("audit.login_failed")}</SelectItem>
          </SelectContent>
        </Select>

        <Select value={user || "all"} onValueChange={(v) => setUser(v === "all" || !v ? "" : v)}>
          <SelectTrigger className="w-[160px]" aria-label={t("audit.user")}>
            <SelectValue>{(value) => (value === "all" ? t("audit.all_users") : String(value ?? ""))}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("audit.all_users")}</SelectItem>
            {users.map((u) => (
              <SelectItem key={u.username} value={u.username}>{u.username}</SelectItem>
            ))}
          </SelectContent>
        </Select>

        <input
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") apply();
          }}
          placeholder={t("audit.ops_search_ph")}
          aria-label={t("audit.ops_search_ph")}
          className="h-9 w-[200px] rounded-lg border border-border bg-card px-3 text-sm"
        />

        <input
          value={agentId}
          onChange={(e) => setAgentId(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") apply();
          }}
          placeholder={t("audit.ops_target_ph")}
          aria-label={t("audit.ops_target_ph")}
          className="h-9 w-[220px] rounded-lg border border-border bg-card px-3 font-mono text-xs"
        />

        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          <input
            type="checkbox"
            checked={includeSystem}
            onChange={(e) => setIncludeSystem(e.target.checked)}
            className="size-4 accent-current"
          />
          {t("audit.ops_include_system")}
        </label>

        <Button onClick={apply} disabled={loading}>
          <Filter className="size-4" />
          <span>{t("audit.apply")}</span>
        </Button>
        <Button variant="outline" onClick={reset}>{t("audit.reset")}</Button>
        <Button variant="ghost" onClick={() => void load()} disabled={loading} aria-label={t("common.refresh")}>
          <RotateCw className="size-4" />
        </Button>
        <Button variant="outline" onClick={() => void exportCsv()} disabled={loading || exporting || total === 0}>
          <Download className="size-4" />
          <span>{t("audit.ops_export")}</span>
        </Button>
      </PageToolbar>

      {exportError && (
        <p role="alert" className="text-sm text-destructive-ink">{t(exportError)}</p>
      )}

      {catalogue.categories.length > 0 && (
        <div
          className="flex flex-wrap items-center gap-1.5"
          role="group"
          aria-label={t("audit.ops_categories_aria")}
        >
          {CATEGORY_IDS.map((id) => {
            const stat = catalogue.categories.find((c) => c.id === id);
            // A category nobody used yet is not a shortcut anyone needs.
            if (!stat || stat.count === 0) return null;
            const active = applied.category === id;
            return (
              <Button
                key={id}
                size="xs"
                variant={active ? "default" : "outline"}
                aria-pressed={active}
                onClick={() => pickCategory(id)}
              >
                {categoryLabel(id)} · {stat.count}
              </Button>
            );
          })}
        </div>
      )}

      {applied.agentId && (
        <div className="flex flex-wrap items-center gap-2">
          <Badge variant="secondary" className="gap-1 font-mono" title={applied.agentId}>
            {t("audit.ops_target_applied", { id: applied.agentId.slice(0, 12) })}
          </Badge>
          <Button variant="ghost" size="xs" onClick={clearTarget}>
            {t("audit.ops_target_clear")}
          </Button>
        </div>
      )}

      <Card className="overflow-hidden">
        <DataTable<AuditLog>
          data={logs}
          label={t("audit.tab_operations")}
          loading={loading}
          error={error ? t(error) : null}
          onRetry={() => void load()}
          emptyTitle={t("audit.ops_empty")}
          columns={[
            {
              id: "timestamp",
              header: t("audit.time"),
              sortValue: (log) => log.timestamp || "",
              cell: (log) => <span className="text-xs font-mono whitespace-nowrap">{field(log, "timestamp")}</span>,
            },
            {
              id: "username",
              header: t("audit.user"),
              sortValue: (log) => log.username || "",
              cell: (log) => <span className="text-sm font-medium">{field(log, "username")}</span>,
            },
            {
              id: "action",
              header: t("audit.action"),
              sortValue: (log) => field(log, "action"),
              cell: (log) => {
                const raw = field(log, "action");
                return (
                  <div className="min-w-0">
                    <Badge variant={auditActionTone(raw)}>{auditActionLabel(t, raw)}</Badge>
                    <div className="mt-0.5 truncate font-mono text-[10px] text-muted-foreground" title={raw}>{raw}</div>
                  </div>
                );
              },
            },
            {
              id: "target",
              header: t("audit.col_target"),
              cell: (log) => (
                <span className="text-xs font-mono" title={field(log, "target")}>
                  {field(log, "target").slice(0, 12)}
                </span>
              ),
            },
            {
              id: "resource",
              header: t("audit.resource"),
              hidden: "hidden lg:table-cell",
              cell: (log) => <span className="text-xs max-w-[160px] truncate">{field(log, "resource")}</span>,
              cellClassName: "max-w-[160px]",
            },
            {
              id: "result",
              header: t("audit.ops_result"),
              cell: (log) => (
                <Badge variant={field(log, "status").toLowerCase().includes("fail") ? "destructive" : "success"}>
                  {field(log, "status").toLowerCase().includes("fail") ? t("audit.login_failed") : t("audit.login_ok")}
                </Badge>
              ),
            },
            {
              id: "details",
              header: t("audit.details"),
              cell: (log) => <span className="text-xs max-w-[320px] truncate">{field(log, "details")}</span>,
              cellClassName: "max-w-[320px]",
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
          rowKey={(log, i) => field(log, "id") || String(i)}
          onRowClick={(log) => setSelectedLog(log)}
          pagination={{ page, pageSize: PER_PAGE, total, onPageChange: setPage }}
        />
      </Card>

      <Dialog open={!!selectedLog} onOpenChange={() => setSelectedLog(null)}>
        <DialogContent size="lg" className="max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{t("audit.ops_detail_title")}</DialogTitle>
          </DialogHeader>
          {selectedLog && (
            <div className="space-y-4">
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
              {(["timestamp", "username", "ip", "action", "resource", "target", "status", "details"] as const).map((key) => (
                <div key={key}>
                  <span className="text-xs font-medium uppercase tracking-wider text-muted-foreground">{key}</span>
                  <p className={`mt-0.5 text-sm ${key === "action" || key === "status" ? "" : "text-muted-foreground"}`}>
                    {key === "action" ? (
                      <Badge variant={auditActionTone(field(selectedLog, key))}>
                        {auditActionLabel(t, field(selectedLog, key))}
                      </Badge>
                    ) : key === "status" ? (
                      <Badge variant={field(selectedLog, key).toLowerCase().includes("fail") ? "destructive" : "success"}>
                        {field(selectedLog, key)}
                      </Badge>
                    ) : (
                      <span className="font-mono">{field(selectedLog, key)}</span>
                    )}
                  </p>
                </div>
              ))}
            </div>
          )}
        </DialogContent>
      </Dialog>
    </div>
  );
}

/** Kept outside the component so a row click does not re-create the callback. */
function openSession(agentId: string) {
  useInteractStore.getState().open(agentId);
}
