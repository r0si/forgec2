import { useCallback, useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Card } from "@/components/ui/card";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { downloadBlob } from "@/lib/download";
import { loginFilterParams, type LoginEvent } from "./login-log";
import { Download, Filter, RotateCw } from "lucide-react";

export type { LoginEvent };

export interface LoginSummary {
  window_hours: number;
  failed: number;
  ok: number;
  distinct_ips: number;
  /** Sign-ins from an address the account has never used (24h window). */
  new_ip?: number;
  by_region: { country: string; city?: string; failed: number; ok: number }[];
}

const PER_PAGE = 50;

/** Result ids the login log accepts ("ok" / "failed"). */
const LOGIN_RESULT_IDS = ["ok", "failed"] as const;
/** Range ids the login log accepts. */
const LOGIN_RANGE_IDS = ["24h", "7d", "30d", "all"] as const;

export interface LoginLogPanelProps {
  /** Filters carried by the deep link; unknown values are ignored. */
  initialResult?: string;
  initialRange?: string;
  /** Only the sign-ins from an address the account had never used. */
  initialNewIp?: boolean;
}

/**
 * Read the filters a deep link carries and keep only the values the server
 * accepts - an unknown result or range would otherwise be sent as-is and come
 * back as an empty table.
 */
export function normalizeLoginDeepLink(input: {
  result?: string | null;
  range?: string | null;
  newIp?: boolean | null;
}): { result: string; range: string; newIp: boolean } {
  const result = (input.result || "").trim().toLowerCase();
  const range = (input.range || "").trim().toLowerCase();
  return {
    result: (LOGIN_RESULT_IDS as readonly string[]).includes(result) ? result : "",
    range: (LOGIN_RANGE_IDS as readonly string[]).includes(range) ? range : "all",
    newIp: input.newIp === true,
  };
}

/**
 * A region is the country plus, when a provider knows it, the city. The login
 * log is about "from where", and two sign-ins from the same country but
 * different cities are different facts, so the city is part of the label and
 * not a tooltip.
 */
function regionLabel(t: (key: string) => string, country: string, city?: string): string {
  if (!country) return t("audit.login_region_unknown");
  if (country === "local") return t("audit.login_region_local");
  const place = (city || "").trim();
  if (!place || place === country) return country;
  return `${country} · ${place}`;
}

/**
 * Login log panel: who signed in, from which IP and region, and when.
 *
 * The row data comes from GET /audit/logins, which reads the hash-chained
 * audit rows (action login / login_failed / logout) and joins a cached
 * IP→region table. The panel never mutates the audit rows: writing a region
 * after the fact would break the chain those rows exist to protect.
 */
export function LoginLogPanel({ initialResult, initialRange, initialNewIp }: LoginLogPanelProps = {}) {
  const { t } = useI18n();
  const deepLink = useMemo(
    () => normalizeLoginDeepLink({ result: initialResult, range: initialRange, newIp: initialNewIp }),
    [initialResult, initialRange, initialNewIp],
  );
  const [events, setEvents] = useState<LoginEvent[]>([]);
  const [summary, setSummary] = useState<LoginSummary | null>(null);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [loading, setLoading] = useState(true);
  const [exporting, setExporting] = useState(false);
  const [exportError, setExportError] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [userFilter, setUserFilter] = useState("");
  const [resultFilter, setResultFilter] = useState(deepLink.result);
  const [range, setRange] = useState(deepLink.range);
  const [newIpOnly, setNewIpOnly] = useState(deepLink.newIp);
  const [applied, setApplied] = useState({
    user: "",
    result: deepLink.result,
    range: deepLink.range,
    newIp: deepLink.newIp,
  });

  const load = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    setError(null);
    try {
      const params = loginFilterParams(applied);
      params.set("page", String(page));
      params.set("pageSize", String(PER_PAGE));
      const payload = (await api.get(paths.audit.logins(params.toString()), { signal })) as {
        events?: LoginEvent[];
        total?: number;
        summary?: LoginSummary;
      };
      setEvents(payload?.events ?? []);
      setTotal(Number(payload?.total) || 0);
      setSummary(payload?.summary ?? null);
    } catch (err) {
      if ((err as Error).name === "AbortError") return;
      setEvents([]);
      setTotal(0);
      // Store the key, not the translated string: `t` is intentionally not a
      // dependency of this callback, so a provider that re-creates `t` cannot
      // turn this effect into a refetch loop.
      setError("audit.login_load_failed");
    } finally {
      setLoading(false);
    }
  }, [applied, page]);

  useEffect(() => {
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [load]);

  const apply = () => {
    setPage(1);
    setApplied({ user: userFilter.trim(), result: resultFilter, range, newIp: newIpOnly });
  };
  const reset = () => {
    setUserFilter("");
    setResultFilter("");
    setRange("all");
    setNewIpOnly(false);
    setPage(1);
    setApplied({ user: "", result: "", range: "all", newIp: false });
  };

  const exportCsv = async () => {
    setExporting(true);
    setExportError(null);
    try {
      const params = loginFilterParams(applied);
      const { blob, filename } = await api.downloadGet(
        paths.audit.exportLogins(params.toString()),
        "login-log.csv",
      );
      downloadBlob(blob, filename);
    } catch {
      // Keep the table's load error separate: a failed export must not replace
      // rows that loaded successfully with an error state.
      setExportError("audit.login_export_failed");
    } finally {
      setExporting(false);
    }
  };

  return (
    <div className="space-y-4">
      {summary && (
        <Card className="p-(--card-spacing)">
          <div className="flex flex-wrap items-center gap-x-6 gap-y-2">
            <span className="text-sm font-medium">{t("audit.login_summary_title")}</span>
            <span className="text-sm text-muted-foreground">
              {t("audit.login_summary_ok")} <span className="font-mono text-foreground">{summary.ok}</span>
            </span>
            <span className="text-sm text-muted-foreground">
              {t("audit.login_summary_failed")}{" "}
              <span className={`font-mono ${summary.failed > 0 ? "text-destructive-ink" : "text-foreground"}`}>
                {summary.failed}
              </span>
            </span>
            <span className="text-sm text-muted-foreground">
              {t("audit.login_summary_ips")} <span className="font-mono text-foreground">{summary.distinct_ips}</span>
            </span>
            <span className="text-sm text-muted-foreground">
              {t("audit.login_new_ip")}{" "}
              {/* The count is a question ("which sign-ins?"); the link is the
                  answer, in the login log itself. */}
              <Link
                to="/audit?tab=logins&new_ip=1&range=24h"
                className="font-mono text-foreground underline-offset-2 hover:underline"
                title={t("audit.login_new_ip_view")}
              >
                {summary.new_ip ?? 0}
              </Link>
            </span>
          </div>
          {summary.by_region.length > 0 && (
            <div className="mt-3 flex flex-wrap gap-1.5">
              {summary.by_region.slice(0, 8).map((row) => {
                const label = regionLabel(t, row.country, row.city);
                return (
                  <Badge
                    key={`${row.country}|${row.city || ""}`}
                    variant={row.failed > 0 ? "destructive" : "secondary"}
                    title={`${label}: ${row.failed} failed / ${row.ok} ok`}
                  >
                    {label} · {row.failed}/{row.ok}
                  </Badge>
                );
              })}
            </div>
          )}
        </Card>
      )}
      <PageToolbar sticky>
        <Select value={range} onValueChange={(v) => setRange(v || "all")}>
          <SelectTrigger className="w-[150px]" aria-label={t("audit.login_range")}>
            <SelectValue>{(value) => t(`audit.login_range_${value ?? "all"}`)}</SelectValue>
          </SelectTrigger>
          <SelectContent>
            {["24h", "7d", "30d", "all"].map((id) => (
              <SelectItem key={id} value={id}>{t(`audit.login_range_${id}`)}</SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={resultFilter || "all"} onValueChange={(v) => setResultFilter(v === "all" || !v ? "" : v)}>
          <SelectTrigger className="w-[180px]" aria-label={t("audit.login_result")}>
            <SelectValue>
              {(value) =>
                value === "all"
                  ? t("audit.login_all_results")
                  : value === "failed"
                    ? t("audit.login_failed")
                    : t("audit.login_ok")}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("audit.login_all_results")}</SelectItem>
            <SelectItem value="ok">{t("audit.login_ok")}</SelectItem>
            <SelectItem value="failed">{t("audit.login_failed")}</SelectItem>
          </SelectContent>
        </Select>
        <input
          value={userFilter}
          onChange={(e) => setUserFilter(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") apply();
          }}
          placeholder={t("audit.login_user_filter")}
          aria-label={t("audit.login_user_filter")}
          className="h-9 w-[180px] rounded-lg border border-border bg-card px-3 text-sm"
        />
        <label className="flex items-center gap-2 text-sm text-muted-foreground">
          <input
            type="checkbox"
            checked={newIpOnly}
            onChange={(e) => setNewIpOnly(e.target.checked)}
            className="size-4 accent-current"
          />
          {t("audit.login_new_ip_only")}
        </label>
        <Button onClick={apply} disabled={loading}>
          <Filter className="size-4" />
          <span>{t("audit.apply")}</span>
        </Button>
        <Button variant="outline" onClick={reset}>
          {t("audit.reset")}
        </Button>
        <Button variant="ghost" onClick={() => void load()} disabled={loading} aria-label={t("common.refresh")}>
          <RotateCw className="size-4" />
        </Button>
        <Button variant="outline" onClick={() => void exportCsv()} disabled={loading || exporting || total === 0}>
          <Download className="size-4" />
          <span>{t("audit.login_export")}</span>
        </Button>
      </PageToolbar>

      {exportError && (
        <p role="alert" className="text-sm text-destructive-ink">{t(exportError)}</p>
      )}

      <Card className="overflow-hidden">
        <DataTable<LoginEvent>
          data={events}
          label={t("audit.tab_logins")}
          loading={loading}
          error={error ? t(error) : null}
          onRetry={() => void load()}
          emptyTitle={t("audit.login_empty")}
          columns={[
            {
              id: "created_at",
              header: t("audit.time"),
              sortValue: (e) => e.created_at || "",
              cell: (e) => <span className="text-xs font-mono whitespace-nowrap">{e.created_at}</span>,
            },
            {
              id: "user",
              header: t("audit.user"),
              sortValue: (e) => e.user || "",
              cell: (e) => <span className="text-sm font-medium">{e.user || "-"}</span>,
            },
            {
              id: "ip",
              header: "IP",
              cell: (e) => <span className="text-xs font-mono">{e.ip || "-"}</span>,
            },
            {
              id: "region",
              header: t("audit.login_region"),
              cell: (e) => <span className="text-xs">{regionLabel(t, e.country, e.city)}</span>,
            },
            {
              id: "result",
              header: t("audit.login_result"),
              cell: (e) => (
                <Badge variant={e.success ? "success" : "destructive"}>
                  {e.success ? t("audit.login_ok") : t("audit.login_failed")}
                </Badge>
              ),
            },
            {
              id: "details",
              header: t("audit.details"),
              cell: (e) => <span className="text-xs max-w-[260px] truncate">{e.details || "-"}</span>,
              cellClassName: "max-w-[260px]",
            },
          ]}
          rowKey={(e, i) => String(e.id || i)}
          pagination={{ page, pageSize: PER_PAGE, total, onPageChange: setPage }}
        />
      </Card>
    </div>
  );
}
