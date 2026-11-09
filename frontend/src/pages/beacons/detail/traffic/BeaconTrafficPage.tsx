import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
registerFragment(enAgents);

import { useEffect, useRef, useState, useCallback } from "react";
import { useParams } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { formatNumber } from "@/lib/intl";

import { Spinner, PageSpinner } from "@/components/ui/spinner";
import { EmptyState } from "@/components/ui/empty-state";
import { ErrorState } from "@/components/ui/error-state";
import { toast } from "sonner";
import { Card, CardContent } from "@/components/ui/card";
import { PageContainer } from "@/components/ui/page-container";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Label } from "@/components/ui/label";
import { Badge } from "@/components/ui/badge";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Activity, Wand2 } from "lucide-react";
import { formatTime } from "@/lib/utils";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";

interface BeaconRecord {
  time: string;
  method: string;
  path: string;
  status: number;
  size: number;
}

interface AdaptationSuggestion {
  desired_interval: number;
  desired_jitter: number;
  pad_size: number;
  reason: string;
  confidence: string;
}

interface BaselineReport {
  agent_id: string;
  sample_count: number;
  baseline_interval: number;
  baseline_jitter: number;
  baseline_packet_size: number;
  mean_interval: number;
  stddev_interval: number;
  mean_packet_size: number;
  cv: number;
  auto_adapt: boolean;
  recent_records: BeaconRecord[];
  suggestion: AdaptationSuggestion | null;
}

export default function AgentTrafficPage() {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const params = useParams();
  const id = params?.id as string;
  const [report, setReport] = useState<BaselineReport | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState(false);

  const [autoAdapt, setAutoAdapt] = useState(false);

  // A load for a previous agent id must not overwrite the report the new id
  // started fetching when the route param changes mid-flight.
  const loadSeqRef = useRef(0);

  const loadReport = useCallback(async () => {
    if (!id) return;
    const seq = ++loadSeqRef.current;
    try {
      // B4 fix: api.get already strips one layer of the {success,data} envelope,
      // so `data` IS the report object. The previous code did `data.data` which
      // was always undefined → report always null.
      const reportData = await api.get(paths.agents.trafficProfile(id));
      if (seq !== loadSeqRef.current) return; // a newer load superseded this one
      setReport((reportData as unknown as BaselineReport) || null);
      setLoadError(false);
      if (reportData) {
        setAutoAdapt((reportData as unknown as BaselineReport).auto_adapt);
      }
    } catch {
      if (seq !== loadSeqRef.current) return;
      // No toast: the full-page ErrorState (with retry) is the surface.
      setLoadError(true);
    } finally {
      if (seq === loadSeqRef.current) setLoading(false);
    }
  }, [id]);

  useEffect(() => { loadReport(); }, [loadReport]);

  const { mutate: adaptTraffic, isPending: adapting, reset: resetAdapt } = useMutation({
    fn: () => api.postJson<{ message?: string }>(paths.agents.trafficAdapt(id), {}),
    onSuccess: (data) => {
      toast.success(t("agents.traffic_adapt_queued").replace("{message}", String(data?.message || "")));
      loadReport();
    },
    onError: (e) => toastError(e, "agents.traffic_adapt_failed"),
  });

  const { mutate: setAutoAdaptEnabled, isPending: togglingAutoAdapt, reset: resetAutoAdapt } = useMutation({
    fn: (enabled: boolean) => api.postJson(paths.agents.trafficAutoAdapt(id), { enabled }),
    onSuccess: (_data, enabled) => {
      setAutoAdapt(enabled);
      toast(enabled ? t("agents.traffic_auto_adapt_enabled") : t("agents.traffic_auto_adapt_disabled"));
    },
    onError: (e) => toastError(e, "agents.traffic_adapt_failed"),
  });

  // Route reuse: /beacons/A/traffic → /beacons/B/traffic keeps this page
  // mounted; the previous agent's report and in-flight actions must not carry
  // over (a stale completion would toast for the wrong agent).
  useEffect(() => {
    setReport(null);
    setAutoAdapt(false);
    setLoadError(false);
    setLoading(true);
    resetAdapt();
    resetAutoAdapt();
  }, [id, resetAdapt, resetAutoAdapt]);

  const handleAdapt = () => {
    if (!id) return;
    adaptTraffic();
  };

  if (loading) {
    return <PageContainer><PageSpinner /></PageContainer>;
  }

  if (loadError) {
    return (
      <PageContainer>
        <Card className="p-(--card-spacing)">
          <ErrorState
            title={t("agents.traffic_load_failed")}
            message={t("agents.traffic_load_failed_hint")}
            action={<Button variant="outline" size="sm" onClick={() => { setLoading(true); setLoadError(false); loadReport(); }}>{t("agents.detail_retry")}</Button>}
            className="mx-auto max-w-md"
          />
        </Card>
      </PageContainer>
    );
  }

  return (
    <PageContainer>
      <Card className="p-(--card-spacing)">
        <div className="flex items-center justify-between mb-6">
          <h2 className="text-lg font-semibold text-foreground">{t("agents.traffic_title")}</h2>
          <div className="flex items-center gap-3">
            <Label className="flex items-center gap-2 text-sm cursor-pointer">
              <Checkbox
                checked={autoAdapt}
                disabled={togglingAutoAdapt}
                onCheckedChange={() => setAutoAdaptEnabled(!autoAdapt)}
              />
              {t("agents.traffic_auto_adapt")}
            </Label>
            <Button
              onClick={handleAdapt}
              disabled={adapting || !report || !report.suggestion}
              className="h-9 text-xs"
            >
              {adapting ? (
                <Spinner size="xs" color="white" />
              ) : (
                <Wand2 className="size-4" />
              )}
              {t("agents.traffic_adapt")}
            </Button>
          </div>
        </div>

        {!report ? (
          <EmptyState icon={Activity} title={t("agents.traffic_no_data")} />
        ) : (
          <>
            <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 sm:gap-5 mb-6">
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-1">{t("agents.traffic_samples")}</div>
                  <div className="text-2xl font-bold text-foreground">{report.sample_count}</div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-1">{t("agents.traffic_baseline_interval")}</div>
                  <div className="text-2xl font-bold text-foreground">{report.baseline_interval || "—"}s</div>
                  <div className="text-xs text-muted-foreground mt-1">{t("agents.traffic_mean", { value: report.mean_interval ? report.mean_interval.toFixed(1) : "—" })}</div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-1">{t("agents.traffic_baseline_jitter")}</div>
                  <div className="text-2xl font-bold text-foreground">{report.baseline_jitter || "—"}%</div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-1">{t("agents.traffic_baseline_packet")}</div>
                  <div className="text-2xl font-bold text-foreground">{report.mean_packet_size ? formatNumber(Math.round(report.mean_packet_size)) : "—"}b</div>
                  <div className="text-xs text-muted-foreground mt-1">{t("agents.traffic_baseline_val", { value: report.baseline_packet_size ? formatNumber(report.baseline_packet_size) : "—" })}</div>
                </CardContent>
              </Card>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-6">
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-2">{t("agents.traffic_timing_regularity")}</div>
                  <div className="flex items-center gap-2">
                    <div className="text-2xl font-bold text-foreground">{report.cv ? report.cv.toFixed(3) : "—"}</div>
                    <Badge variant={report.cv < 0.15 ? "destructive" : report.cv < 0.3 ? "secondary" : "success"} className="px-2 py-0.5 rounded-full text-xs font-medium">
                      {report.cv < 0.15 ? t("agents.traffic_too_regular") : report.cv < 0.3 ? t("agents.traffic_moderate") : t("agents.traffic_natural")}
                    </Badge>
                  </div>
                  <div className="text-xs text-muted-foreground mt-1">{t("agents.traffic_stddev", { value: report.stddev_interval ? report.stddev_interval.toFixed(2) : "—" })}</div>
                </CardContent>
              </Card>
              <Card>
                <CardContent className="p-4">
                  <div className="text-xs text-muted-foreground mb-2">{t("agents.traffic_current_suggestion")}</div>
                  {report.suggestion ? (
                    <div>
                      <div className="text-sm font-medium text-foreground">{report.suggestion.reason}</div>
                      <div className="flex items-center gap-2 mt-1">
                        <Badge variant={report.suggestion.confidence === "high" ? "success" : "secondary"} className="px-2 py-0.5 rounded-full text-xs font-medium">
                          {report.suggestion.confidence}
                        </Badge>
                        {report.suggestion.desired_interval > 0 && (
                          <span className="text-xs text-muted-foreground">{t("agents.traffic_suggest_interval", { value: report.suggestion.desired_interval })}</span>
                        )}
                        {report.suggestion.desired_jitter > 0 && (
                          <span className="text-xs text-muted-foreground">{t("agents.traffic_suggest_jitter", { value: report.suggestion.desired_jitter })}</span>
                        )}
                        {report.suggestion.pad_size > 0 && (
                          <span className="text-xs text-muted-foreground">{t("agents.traffic_suggest_pad", { value: report.suggestion.pad_size })}</span>
                        )}
                      </div>
                    </div>
                  ) : (
                    <div className="text-sm text-muted-foreground">{t("agents.traffic_collecting_baseline")}</div>
                  )}
                </CardContent>
              </Card>
            </div>

            {report.recent_records && report.recent_records.length > 0 && (
              <div>
                <div className="text-sm font-semibold text-foreground mb-3">{t("agents.traffic_beacon_timeline", { count: report.recent_records.length })}</div>
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead className="text-left font-medium">{t("agents.traffic_col_time")}</TableHead>
                      <TableHead className="text-right font-medium">{t("agents.traffic_col_size")}</TableHead>
                      <TableHead className="text-right font-medium">{t("agents.traffic_col_interval")}</TableHead>
                      <TableHead className="text-center font-medium">{t("agents.traffic_col_transport")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {report.recent_records.map((rec, i) => {
                      const prev = i > 0 ? report.recent_records[i - 1] : null;
                      const interval = prev
                        ? ((new Date(rec.time).getTime() - new Date(prev.time).getTime()) / 1000).toFixed(1)
                        : "—";
                      return (
                        <TableRow key={i}>
                          <TableCell className="text-foreground">{formatTime(rec.time)}</TableCell>
                          <TableCell className="text-right text-foreground">{formatNumber(rec.size)}b</TableCell>
                          <TableCell className="text-right text-foreground">{interval}s</TableCell>
                          <TableCell className="text-center text-muted-foreground">{rec.method} {rec.path}</TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
            )}
          </>
        )}
      </Card>


    </PageContainer>
  );
}
