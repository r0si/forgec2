import { registerFragment } from "@/lib/i18n";
import { enListeners } from "@/lib/i18n/fragments/en.listeners";
registerFragment(enListeners);

import { PageContainer } from "@/components/ui/page-container";

import { useEffect, useState, useCallback, useMemo } from "react";
import { useParams } from "react-router-dom";
import { Link } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { PageSpinner } from "@/components/ui/spinner";
import { Card } from "@/components/ui/card";
import { SectionCard } from "@/components/ui/section-card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Activity, Info, Plug, Radio } from "lucide-react";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";
import { POLL } from "@/lib/polling";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { firstArray } from "@/lib/envelope";
import { isTLSScheme } from "../components/types";
import {
  appendHealthSamples,
  healthForListener,
  indexListenerHealth,
  translateHealthStatus,
  type HealthSample,
  type ListenerHealth,
} from "@/lib/listener-health";
import { HealthSparkline } from "@/components/ui/health-sparkline";
import { StatusIndicator } from "@/components/ui/status-indicator";
import { formatTime, isZeroTime } from "@/lib/utils";

interface ListenerDetail {
  ID?: string;
  id?: string;
  Name?: string;
  name?: string;
  Host?: string;
  host?: string;
  Port?: number | string;
  port?: number | string;
  Type?: string;
  type?: string;
  Scheme?: string;
  scheme?: string;
  Protocol?: string;
  protocol?: string;
  Enabled?: boolean | string;
  enabled?: boolean;
  Notes?: string;
  notes?: string;
  CreatedAt?: string;
  created_at?: string;
  // Per-listener TLS posture (https/tls listeners only).
  client_ca_file?: string;
  require_client_cert?: boolean;
  min_tls_version?: string;
}

interface ListenerAgent {
  id?: string;
  hostname?: string;
  ip?: string;
  os?: string;
  arch?: string;
  last_seen?: string;
  status?: string;
}

export default function ListenerDetailPage() {
  const params = useParams();
  const id = params?.id as string;
  const [listener, setListener] = useState<ListenerDetail | null>(null);
  const [agents, setAgents] = useState<ListenerAgent[]>([]);
  const [stats, setStats] = useState({ total: 0, active: 0 });
  const [loading, setLoading] = useState(true);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [health, setHealth] = useState<ListenerHealth | undefined>(undefined);
  const [healthSamples, setHealthSamples] = useState<HealthSample[]>([]);
  const { t } = useI18n();

  const loadDetail = useCallback(async () => {
    if (!id) return;
    try {
      const data = await api.get(paths.listeners.one(id));
      setListener(data.listener || data);
      setDetailError(null);
      const a: ListenerAgent[] = (data.agents || []) as ListenerAgent[];
      setAgents(a);
      setStats({ total: a.length, active: a.filter(ag => (ag.status) === "online").length });
    } catch {
      // A failed request is not proof the listener was deleted. Keep them
      // separate so a 500 does not render as "listener not found".
      setListener(null);
      setAgents([]);
      setStats({ total: 0, active: 0 });
      setDetailError(t("listeners.load_failed"));
    } finally {
      setLoading(false);
    }
  }, [id, t]);

  const loadHealth = useCallback(async () => {
    if (!id) return;
    try {
      const data = await api.get(paths.circuitBreaker.detail);
      const rows = firstArray(data, ["listeners", "data"]) as ListenerHealth[];
      const found = healthForListener(indexListenerHealth(rows), id);
      setHealth(found);
      if (found) {
        setHealthSamples((prev) => {
          const keyed = indexListenerHealth([found]);
          const merged = appendHealthSamples({ [id]: prev }, keyed);
          return merged[id] ?? prev;
        });
      }
    } catch {
      // Keep last known health; detail page should stay usable offline.
    }
  }, [id]);

  useEffect(() => { loadDetail(); }, [loadDetail]);
  useEffect(() => { void loadHealth(); }, [loadHealth]);
  useVisibleInterval(() => { void loadHealth(); }, POLL.listeners);

  const healthLabel = useMemo(
    () => (health ? translateHealthStatus(t, health.status) : t("listeners.health_unmonitored")),
    [health, t],
  );

  if (loading) {
    return <PageContainer><PageSpinner /></PageContainer>;
  }

  if (!listener) {
    return (
      <PageContainer>
        {detailError ? (
          <DataError message={detailError} onRetry={() => { setLoading(true); void loadDetail(); }} />
        ) : (
          <div className="text-center py-20 text-muted-foreground">
            <Plug className="size-4 mx-auto" />
            <p className="mt-2">{t("listeners.not_found")}</p>
          </div>
        )}
      </PageContainer>
    );
  }

  const name = listener.name || t("listeners.unknown");
  const scheme = listener.scheme || listener.protocol || listener.type || "http";
  const host = listener.host || "0.0.0.0";
  const port = listener.port ?? 8080;
  const isEnabled = listener.enabled === true;
  const notes = listener.notes || "-";
  const createdAt = formatTime(listener.created_at);

  return (
    <PageContainer title={name} subtitle={`${scheme}://${host}:${port}`}>
      <div className="flex items-center gap-x-4">
        <div>

        </div>
        <div className="ml-auto flex items-center gap-2">
          {health && (
            <span className="inline-flex items-center gap-1.5 text-xs text-muted-foreground">
              <StatusIndicator
                status={health.status || "unknown"}
                variant="dot"
                label={healthLabel}
                size="sm"
              />
              {health.last_probe && !isZeroTime(health.last_probe) && (
                <span className="font-mono text-micro-sm">
                  {formatTime(health.last_probe)}
                </span>
              )}
              <HealthSparkline samples={healthSamples} width={88} height={18} />
            </span>
          )}
          {isEnabled ? (
            <Badge variant="default">{t("listener.enabled")}</Badge>
          ) : (
            <Badge variant="secondary">{t("listener.disabled")}</Badge>
          )}
        </div>
      </div>

      <div className="grid grid-cols-1 lg:grid-cols-3 gap-4">
        <div className="lg:col-span-1">
          <SectionCard
            title={t("listener.info_title")}
            icon={Info}
            tone="muted"
            headerAccent
          >
            <div className="p-(--card-spacing)">
            <div className="space-y-3 text-sm">
              <div><span className="text-muted-foreground">{t("listener.scheme")}</span> <span className="text-foreground">{scheme}</span></div>
              <div><span className="text-muted-foreground">{t("listener.address")}</span> <span className="text-foreground">{host}:{port}</span></div>
              <div><span className="text-muted-foreground">{t("listener.transport_type")}</span> <span className="text-foreground">{listener.type || scheme}</span></div>
              {isTLSScheme(scheme) && (
                <>
                  <div><span className="text-muted-foreground">{t("listener.tls_client_ca")}</span> <span className="text-foreground">{listener.client_ca_file || t("listener.tls_inherit")}</span></div>
                  <div><span className="text-muted-foreground">{t("listener.tls_client_cert")}</span> <span className="text-foreground">{listener.require_client_cert === true ? t("listener.tls_required") : t("listener.tls_not_required")}</span></div>
                  <div><span className="text-muted-foreground">{t("listener.tls_min_version")}</span> <span className="text-foreground">{listener.min_tls_version || t("listener.tls_inherit")}</span></div>
                </>
              )}
              <div><span className="text-muted-foreground">{t("listener.notes")}</span> <span className="text-foreground">{notes}</span></div>
              <div><span className="text-muted-foreground">{t("listener.created")}</span> <span className="text-foreground">{createdAt}</span></div>
              <div>
                <span className="text-muted-foreground">{t("listeners.col_health")}</span>{" "}
                <span className="text-foreground">
                  {health ? healthLabel : t("listeners.health_unmonitored")}
                </span>
                {healthSamples.length >= 2 && (
                  <div className="mt-1.5">
                    <HealthSparkline samples={healthSamples} width={160} height={22} />
                  </div>
                )}
              </div>
            </div>
            <div className="mt-6 flex gap-2">
               <Button render={<Link to={`/payload-generator?listener_id=${id}`} />} className="flex-1">{t("listener.generate_implant")}</Button>
            </div>
            </div>
          </SectionCard>
        </div>

        <div className="lg:col-span-2">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <Card className="p-(--card-spacing)">
              <div className="text-xs text-muted-foreground">{t("listener.agent_total")}</div>
              <div className="text-4xl font-semibold mt-2 text-foreground">{stats.total}</div>
            </Card>
            <Card className="p-(--card-spacing)">
              <div className="text-xs text-muted-foreground">{t("listener.active_now")}</div>
              <div className="text-4xl font-semibold mt-2 text-success-ink">{stats.active}</div>
            </Card>
            <Card className="p-(--card-spacing)">
              <div className="text-xs text-muted-foreground">{t("listener.lb_hint_title")}</div>
              <div className="text-sm mt-2 text-muted-foreground">{t("listener.lb_hint")}</div>
            </Card>
          </div>
        </div>
      </div>

      <div className="mt-8">
        <SectionCard
          title={t("listener.agents_using")}
          icon={Radio}
          tone="info"
          count={stats.total}
          headerAccent
          action={
            <Link to="/beacons" className="text-sm text-primary hover:underline transition-colors">
              {t("listener.view_all_agents")}
            </Link>
          }
        >
          <div className="p-(--card-spacing)">
          <Table>
            <TableHeader>
              <TableRow>
                <TableHead className="py-3 px-6">{t("listeners.col_agent")}</TableHead>
                <TableHead className="py-3 px-4">IP</TableHead>
                <TableHead className="py-3 px-4">{t("listener.os")}</TableHead>
                <TableHead className="py-3 px-4">{t("listener.last_seen")}</TableHead>
                <TableHead className="py-3 px-4 text-center">{t("listener.status")}</TableHead>
                <TableHead className="py-3 px-6 text-right">{t("listener.action")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {agents.length > 0 ? (
                agents.map((a, i) => {
                  const aid = a.id || String(i);
                  const status = a.status || "offline";
                  return (
                    <TableRow key={aid}>
                      <TableCell className="py-3 px-6 font-medium text-foreground">{a.hostname || "-"}</TableCell>
                      <TableCell className="py-3 px-4 font-mono text-xs text-muted-foreground">{a.ip || "-"}</TableCell>
                      <TableCell className="py-3 px-4 text-xs text-muted-foreground">{a.os || "-"}/{a.arch || "-"}</TableCell>
                      <TableCell className="py-3 px-4 text-xs text-muted-foreground">{formatTime(a.last_seen)}</TableCell>
                      <TableCell className="py-3 px-4 text-center">
                        {status === "online" ? (
                          <Badge variant="default">{t("listener.online")}</Badge>
                        ) : (
                          <Badge variant="secondary">{t("listener.offline")}</Badge>
                        )}
                      </TableCell>
                      <TableCell className="py-3 px-6 text-right">
                        <Link to={`/beacons/${aid}`} className="text-primary hover:text-primary hover:underline text-sm transition-colors">{t("listener.detail")}</Link>
                      </TableCell>
                    </TableRow>
                  );
                })
              ) : (
                <TableRow>
                  <TableCell colSpan={6}><EmptyState compact icon={Activity} title={t("listener.empty")} /></TableCell>
                </TableRow>
              )}
            </TableBody>
          </Table>
          </div>
        </SectionCard>
      </div>
    </PageContainer>
  );
}
