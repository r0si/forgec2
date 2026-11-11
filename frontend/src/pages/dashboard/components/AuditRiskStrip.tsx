import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { Badge } from "@/components/ui/badge";
import { Card } from "@/components/ui/card";
import { DataError } from "@/components/ui/data-state";
import { SectionCard } from "@/components/ui/section-card";
import { StatTile } from "@/components/ui/stat-tile";
import { timeAgo } from "@/lib/utils";
import { auditActionLabel, normalizeAuditLogs } from "@/pages/audit/components/audit-log";
import type { AuditLog } from "@/types/audit";
import { AlertTriangle } from "lucide-react";

interface LoginSummary {
  failed: number;
  ok: number;
  distinct_ips: number;
  /** Sign-ins from an address the account has never used (24h window). */
  new_ip?: number;
  by_region: { country: string; city?: string; failed: number; ok: number }[];
}

/**
 * A compact security strip on the dashboard: failed operations and failed
 * sign-ins over the same 24h window as the login rollup, plus the regions the
 * failures came from. It reuses the audit endpoints rather than adding a
 * second aggregation path, and disappears for roles without `audit.read`.
 */
export function AuditRiskStrip() {
  const { t } = useI18n();
  const { can } = usePermissions();
  const allowed = can("audit.read");
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [failedOps, setFailedOps] = useState(0);
  const [failedRows, setFailedRows] = useState<AuditLog[]>([]);
  const [summary, setSummary] = useState<LoginSummary | null>(null);

  const load = useCallback(async (signal?: AbortSignal) => {
    if (!allowed) return;
    setLoading(true);
    setError(null);
    try {
      const [ops, logins] = await Promise.all([
        api.get(paths.audit.logs("pageSize=5&since=24h&result=failed&scope=operations"), { signal }),
        api.get(paths.audit.logins("pageSize=1&since=24h"), { signal }),
      ]);
      const opsPayload = (ops && typeof ops === "object" ? ops : {}) as Record<string, unknown>;
      const loginPayload = (logins && typeof logins === "object" ? logins : {}) as {
        summary?: LoginSummary;
      };
      setFailedOps(Number(opsPayload.failed ?? opsPayload.total) || 0);
      // The counts alone do not say what broke; the rows do.
      setFailedRows(normalizeAuditLogs(opsPayload.logs));
      setSummary(loginPayload.summary ?? null);
    } catch (err) {
      if ((err as Error).name === "AbortError") return;
      setError("dashboard.audit_unavailable");
      setFailedOps(0);
      setFailedRows([]);
      setSummary(null);
    } finally {
      setLoading(false);
    }
  }, [allowed]);

  useEffect(() => {
    if (!allowed) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [allowed, load]);

  if (!allowed) return null;

  const failedLogins = summary?.failed ?? 0;
  const distinctIPs = summary?.distinct_ips ?? 0;
  const newIPs = summary?.new_ip ?? 0;
  const regions = (summary?.by_region ?? []).filter((r) => r.failed > 0).slice(0, 5);

  return (
    <SectionCard
      title={t("dashboard.audit_risk_title")}
      icon={AlertTriangle}
      tone="warning"
      href="/audit?tab=operations"
      linkLabel={t("dashboard.audit_open")}
    >
      <div className="space-y-4 p-(--card-spacing)">
        {error ? (
          <DataError message={t(error)} onRetry={() => void load()} />
        ) : (
          <>
            <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 lg:grid-cols-4">
              <Card className="p-(--card-spacing)">
                <StatTile
                  label={t("dashboard.audit_failed_ops")}
                  value={loading ? "—" : failedOps}
                  tone={failedOps > 0 ? "destructive" : "success"}
                />
              </Card>
              <Card className="p-(--card-spacing)">
                <StatTile
                  label={t("dashboard.audit_failed_logins")}
                  value={loading ? "—" : failedLogins}
                  tone={failedLogins > 0 ? "destructive" : "success"}
                />
              </Card>
              <Card className="p-(--card-spacing)">
                <StatTile
                  label={t("dashboard.audit_distinct_ips")}
                  value={loading ? "—" : distinctIPs}
                  tone="muted"
                />
              </Card>
              <Card className="p-(--card-spacing)">
                <StatTile
                  label={t("dashboard.audit_new_ip")}
                  value={loading ? "—" : newIPs}
                  tone={newIPs > 0 ? "warning" : "muted"}
                />
              </Card>
            </div>
            {!loading && failedOps === 0 && failedLogins === 0 ? (
              <p className="text-sm text-muted-foreground">{t("dashboard.audit_no_anomalies")}</p>
            ) : (
              <>
                {failedRows.length > 0 && (
                  <div className="space-y-1.5">
                    <span className="text-xs font-medium text-muted-foreground">
                      {t("dashboard.audit_failed_list_title")}
                    </span>
                    <div className="divide-y divide-border">
                      {failedRows.map((row, i) => (
                        <div key={row.id || i} className="flex items-center justify-between gap-3 py-1.5">
                          <div className="flex min-w-0 items-center gap-2">
                            <Badge variant="destructive">{auditActionLabel(t, row.action || "")}</Badge>
                            <span className="truncate text-xs text-muted-foreground">
                              {[row.username, row.details].filter(Boolean).join(" · ")}
                            </span>
                          </div>
                          <span className="micro-sm whitespace-nowrap text-muted-foreground">
                            {row.timestamp ? timeAgo(String(row.timestamp), t) : ""}
                          </span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
                {regions.length > 0 && (
                  <div className="flex flex-wrap items-center gap-2">
                    <span className="text-xs font-medium text-muted-foreground">
                      {t("dashboard.audit_top_regions")}
                    </span>
                    {regions.map((r) => (
                      <Badge key={`${r.country}|${r.city || ""}`} variant="destructive">
                        {[r.country || t("dashboard.audit_region_unknown"), r.city]
                          .filter(Boolean)
                          .join(" · ")}{" "}
                        · {r.failed}
                      </Badge>
                    ))}
                  </div>
                )}
              </>
            )}
          </>
        )}
      </div>
    </SectionCard>
  );
}
