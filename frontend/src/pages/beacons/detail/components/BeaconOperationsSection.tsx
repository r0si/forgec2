import { memo, useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { SectionCard } from "@/components/ui/section-card";
import { Skeleton } from "@/components/ui/skeleton";
import { timeAgo } from "@/lib/utils";
import { auditActionLabel, auditActionTone, normalizeAuditLogs } from "@/pages/audit/components/audit-log";
import type { AuditLog } from "@/types/audit";
import { History } from "lucide-react";

const PAGE_SIZE = 5;

interface BeaconOperationsSectionProps {
  agentId: string;
}

/**
 * The operations that touched this beacon.
 *
 * The audit page's Operations tab holds the full history; this is the same
 * selection - the newest rows for this beacon - shown where the operator is
 * already looking when they ask "who touched this host?". It needs
 * `audit.read` and disappears for roles without it.
 */
export default memo(function BeaconOperationsSection({ agentId }: BeaconOperationsSectionProps) {
  const { t } = useI18n();
  const { can } = usePermissions();
  const allowed = can("audit.read");
  const [logs, setLogs] = useState<AuditLog[]>([]);
  const [loading, setLoading] = useState(true);
  const [unavailable, setUnavailable] = useState(false);

  const load = useCallback(
    async (signal?: AbortSignal) => {
      if (!allowed || !agentId) return;
      setLoading(true);
      setUnavailable(false);
      try {
        const query = `pageSize=${PAGE_SIZE}&agent_id=${encodeURIComponent(agentId)}&scope=operations`;
        const payload = (await api.get(paths.audit.logs(query), { signal })) as Record<string, unknown> | null;
        setLogs(normalizeAuditLogs(payload?.logs));
      } catch (err) {
        if ((err as Error).name === "AbortError") return;
        // An unreadable history must not take the beacon page down with it.
        setUnavailable(true);
        setLogs([]);
      } finally {
        setLoading(false);
      }
    },
    [agentId, allowed],
  );

  useEffect(() => {
    if (!allowed) return;
    const controller = new AbortController();
    void load(controller.signal);
    return () => controller.abort();
  }, [allowed, load]);

  if (!allowed) return null;

  return (
    <SectionCard
      className="mb-(--page-gap)"
      title={t("agents.operations_title")}
      icon={History}
      tone="muted"
      href={`/audit?tab=operations&agent_id=${encodeURIComponent(agentId)}`}
      linkLabel={t("agents.operations_view_all")}
    >
      <div className="px-4 py-2">
        {loading ? (
          <Skeleton className="h-6 w-full" />
        ) : unavailable ? (
          <div className="flex items-center gap-2 py-1">
            <span className="text-xs text-muted-foreground">{t("agents.operations_unavailable")}</span>
            <Button size="xs" variant="outline" onClick={() => void load()}>{t("common.try_again")}</Button>
          </div>
        ) : logs.length === 0 ? (
          <p className="py-1 text-xs text-muted-foreground">{t("agents.operations_empty")}</p>
        ) : (
          <div className="divide-y divide-border">
            {logs.map((log, i) => (
              <div key={log.id || i} className="flex items-center justify-between gap-2 py-2">
                <div className="flex min-w-0 items-center gap-2">
                  {/* A failed operation is the one that needs the operator's
                      eye: the tone follows the outcome, not the action's
                      usual category colour. */}
                  <Badge variant={log.success === false ? "destructive" : auditActionTone(log.action || "")}>
                    {auditActionLabel(t, log.action || "")}
                  </Badge>
                  {log.success === false && (
                    <span className="micro-sm font-medium text-destructive-ink">
                      {t("agents.operations_failed")}
                    </span>
                  )}
                  {log.details && (
                    <span className="truncate text-xs text-muted-foreground">{log.details}</span>
                  )}
                </div>
                <span className="micro-sm whitespace-nowrap text-muted-foreground">
                  {log.timestamp ? timeAgo(String(log.timestamp), t) : ""}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
    </SectionCard>
  );
});
