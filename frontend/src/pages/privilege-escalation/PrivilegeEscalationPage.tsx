
import { registerFragment } from "@/lib/i18n";
import { enPrivesc } from "@/lib/i18n/fragments/en.privesc";
registerFragment(enPrivesc);

import { useEffect, useState, useCallback, useRef } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { downloadText, downloadJSON } from "@/lib/download";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { POLL } from "@/lib/polling";
import { useI18n } from "@/lib/i18n";
import { formatTime } from "@/lib/utils";
import { Spinner } from "@/components/ui/spinner";
import { PageContainer } from "@/components/ui/page-container";
import { StatTile } from "@/components/ui/stat-tile";
import { IconBadge } from "@/components/ui/icon-badge";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { toast } from "sonner";
import { Card } from "@/components/ui/card";
import { Banner } from "@/components/ui/banner";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Label } from "@/components/ui/label";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { ArrowUp, Bot, Bug, ChevronDown, CircleAlert, CircleQuestionMark, CreditCard, Eye, EyeOff, FileCode, FileSpreadsheet, Hammer, History, Info, KeyRound, Lightbulb, Play, Printer, Search, Shield, ShieldAlert, ShieldCheck, ShieldOff, TriangleAlert, TrendingUp, Wrench, Zap } from "lucide-react";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { Collapsible, CollapsibleTrigger, CollapsibleContent } from "@/components/ui/collapsible";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";

interface PrivescAgent {
  id?: string;
  hostname?: string;
  ip?: string;
  os?: string;
}

interface PrivescHistory {
  id?: string;
  agent_id?: string;
  check_type?: string;
  status?: string;
  result?: string;
  created_at?: string;
  findings_count?: number;
}

interface PrivescFinding {
  id?: string;
  title?: string;
  severity?: string;
  cve_id?: string;
  description?: string;
  recommendation?: string;
}

function severityBadge(severity: string) {
  switch (severity) {
    case "critical": return "bg-destructive/10 text-destructive-ink border-destructive/30";
    case "high": return "bg-warning/10 text-warning-ink dark:text-warning-ink border-warning/30";
    case "medium": return "bg-warning/10 text-warning-ink dark:text-warning-ink border-warning/30";
    case "low": return "bg-primary/10 text-primary border-primary/30";
    default: return "bg-secondary/50 text-muted-foreground border-border";
  }
}

function severityIcon(severity: string): React.ReactNode {
  switch (severity) {
    case "critical": return <CircleAlert className="size-4 text-destructive-ink" />;
    case "high": return <TriangleAlert className="size-4 text-warning-ink" />;
    case "medium": return <CircleAlert className="size-4 text-warning-ink" />;
    case "low": return <Info className="size-4 text-primary" />;
    default: return <CircleQuestionMark className="size-4 text-muted-foreground" />;
  }
}

export default function PrivescPage() {
  const [agents, setAgents] = useState<PrivescAgent[]>([]);
  const [history, setHistory] = useState<PrivescHistory[]>([]);
  const [findings, setFindings] = useState<PrivescFinding[]>([]);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [selectedAgent, setSelectedAgent] = useState("");
  const [checkType, setCheckType] = useState("all");
  const [running, setRunning] = useState(false);
  const [expandedFinding, setExpandedFinding] = useState<string | null>(null);
  const [statusFilter, setStatusFilter] = useState("all");

  const { modal } = useConfirm();

  const { t } = useI18n();

  const CHECK_TYPES = [
    { value: "all", icon: <Search className="size-4" />, label: t("privesc.ct_all"), desc: t("privesc.ct_all_desc") },
    { value: "printnightmare", icon: <Printer className="size-4" />, label: "PrintNightmare", desc: t("privesc.ct_printnightmare_desc") },
    { value: "elevate", icon: <ArrowUp className="size-4" />, label: t("privesc.ct_elevate"), desc: t("privesc.ct_elevate_desc") },
    { value: "uac_bypass", icon: <Shield className="size-4" />, label: t("privesc.ct_uac"), desc: t("privesc.ct_uac_desc") },
    { value: "amsi_bypass", icon: <ShieldOff className="size-4" />, label: t("privesc.ct_amsi"), desc: t("privesc.ct_amsi_desc") },
    { value: "etw_bypass", icon: <EyeOff className="size-4" />, label: t("privesc.ct_etw"), desc: t("privesc.ct_etw_desc") },
    { value: "cvescan", icon: <Bug className="size-4" />, label: t("privesc.ct_cvescan"), desc: t("privesc.ct_cvescan_desc") },
    { value: "binary_abuse", icon: <Hammer className="size-4" />, label: t("privesc.ct_binary"), desc: t("privesc.ct_binary_desc") },
    { value: "service_exploit", icon: <Wrench className="size-4" />, label: t("privesc.ct_service"), desc: t("privesc.ct_service_desc") },
    { value: "token_abuse", icon: <CreditCard className="size-4" />, label: t("privesc.ct_token"), desc: t("privesc.ct_token_desc") },
    { value: "kernel_exploit", icon: <Zap className="size-4" />, label: t("privesc.ct_kernel"), desc: t("privesc.ct_kernel_desc") },
    { value: "password_finder", icon: <KeyRound className="size-4" />, label: t("privesc.ct_password"), desc: t("privesc.ct_password_desc") },
  ];

  const loadBusyRef = useRef(false);
  const hasDataRef = useRef(false);
  const delayedReloadRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  const loadData = useCallback(async () => {
    if (loadBusyRef.current) return;
    loadBusyRef.current = true;
    try {
      const data = await api.get(paths.privesc.page);
      setAgents((data.agents || []) as PrivescAgent[]);
      setHistory((data.history || []) as PrivescHistory[]);
      setFindings((data.findings || []) as PrivescFinding[]);
      hasDataRef.current = true;
      setLoadError(null);
    } catch (err) {
      // Keep the last good data on transient poll failures — wiping the
      // arrays would blank the table every time the server blips. The
      // banner only matters when nothing has ever loaded (matches the
      // retain-on-error semantics useApiResource established).
      if (!hasDataRef.current) {
        setLoadError(err instanceof Error ? err.message : t("privesc.load_failed"));
      }
    }
    setLoading(false);
    loadBusyRef.current = false;
  }, [t]);

  useEffect(() => {
    return () => {
      if (delayedReloadRef.current) clearTimeout(delayedReloadRef.current);
      loadBusyRef.current = false;
    };
  }, []);

  useEffect(() => { loadData(); }, [loadData]);
  useVisibleInterval(loadData, POLL.listing);

  const handleRun = async () => {
    if (!selectedAgent) {
      toast.error(t("privesc.toast_select_agent"));
      return;
    }
    setRunning(true);
    try {
      await api.postJson(paths.privesc.run, { agent_id: selectedAgent, check_type: checkType });
      if (delayedReloadRef.current) clearTimeout(delayedReloadRef.current);
      delayedReloadRef.current = setTimeout(loadData, 2000);
    } catch { toast.error(t("privesc.toast.start_check_failed")); }
    setRunning(false);
  };

  const handleViewHistory = async (historyId: string) => {
    try {
      const data = await api.get(paths.privesc.history(historyId));
      setFindings((data.findings || data.tasks || []) as PrivescFinding[]);
    } catch { toast.error(t("privesc.toast.load_history_failed")); }
  };

  const handleExportJSON = () => {
    downloadJSON(findings, `privesc_findings_${new Date().toISOString().split("T")[0]}.json`);
  };

  const handleExportCSV = () => {
    const headers = ["Title", "Severity", "CVE", "Description", "Recommendation"];
    const rows = findings.map((f) => [
      f.title || "", f.severity || "", f.cve_id || "", (f.description || "").replace(/,/g, ";"), (f.recommendation || "").replace(/,/g, ";"),
    ]);
    const csv = [headers, ...rows].map((r) => r.map((c) => `"${c}"`).join(",")).join("\n");
    downloadText(csv, `privesc_findings_${new Date().toISOString().split("T")[0]}.csv`, "text/csv");
  };

  const totalChecks = history.length;
  const criticalCount = findings.filter((f) => f.severity === "critical").length;
  const highCount = findings.filter((f) => f.severity === "high").length;
  const mediumCount = findings.filter((f) => f.severity === "medium").length;
  const lowCount = findings.filter((f) => f.severity === "low").length;

  return (
    <PageContainer title={t("privesc.title")} icon={<TrendingUp className="size-4" />} subtitle={t("privesc.subtitle")} actions={<>
        <div className="flex items-center gap-2">
          <Button variant="secondary" size="sm" onClick={handleExportJSON}>
            <FileCode className="size-4" /> JSON
          </Button>
          <Button variant="secondary" size="sm" onClick={handleExportCSV}>
            <FileSpreadsheet className="size-4" /> CSV
          </Button>
        </div>
      </>}>

      <Banner tone="warning" className="items-start">
        <div className="font-semibold">{t("privesc.honesty_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("privesc.honesty_desc")}</div>
      </Banner>

      <div className="grid grid-cols-2 sm:grid-cols-5 gap-4">
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("privesc.stat_total_checks")} value={totalChecks} tone="primary" />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("privesc.stat_critical")} value={criticalCount} tone="destructive" />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("privesc.stat_high")} value={highCount} tone="warning" />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("privesc.stat_medium")} value={mediumCount} tone="warning" />
        </Card>
        <Card interactive className="p-(--card-spacing)">
          <StatTile label={t("privesc.stat_low")} value={lowCount} tone="primary" />
        </Card>
      </div>

      <Card className="px-6 py-6">
        <div className="flex items-center gap-x-3 mb-5">
          <IconBadge icon={ShieldAlert} color="primary" size="lg" />
          <div>
            <div className="text-sm font-semibold">{t("privesc.new_task")}</div>
            <div className="text-xs text-muted-foreground">{t("privesc.new_task_desc")}</div>
          </div>
        </div>

        <div className="flex flex-col gap-(--page-gap)">
          <div>
            <Label className="text-sm mb-2 block">
              <Bot className="size-4" />{t("privesc.target_agent")}
            </Label>
            <Select value={selectedAgent || "placeholder"} onValueChange={(v) => setSelectedAgent(v === "placeholder" ? "" : v ?? "")}>
              <SelectTrigger className="w-full">
                <SelectValue>
                  {(value) => (value === "placeholder" ? t("privesc.select_target_implant") : String(value ?? ""))}
                </SelectValue>
              </SelectTrigger>
              <SelectContent>
                {agents.map((a) => {
                  const id = a.id || "";
                  const hostname = a.hostname || "";
                  const ip = a.ip || "";
                  const os = a.os || "";
                  return <SelectItem key={id} value={id}>{hostname} ({ip}) - {os}</SelectItem>;
                })}
                {!agents.length && <SelectItem value="placeholder" disabled>{t("privesc.select_target_implant")}</SelectItem>}
              </SelectContent>
            </Select>
          </div>

          <div>
            <span className="block text-sm font-semibold mb-3">{t("privesc.check_type_label")}</span>
            <RadioGroup value={checkType} onValueChange={setCheckType} className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
              {CHECK_TYPES.map((ct) => (
                <div key={ct.value} className={`flex items-center gap-3 p-3 rounded-lg cursor-pointer transition-colors ${checkType === ct.value ? "border-2 border-primary bg-primary/10" : "border border-border hover:bg-muted/50"}`}>
                  <RadioGroupItem value={ct.value} id={`ct-${ct.value}`} />
                  <Label htmlFor={`ct-${ct.value}`} className="min-w-0 cursor-pointer">
                    <div className="flex items-center gap-2 text-sm font-medium">
                      <span aria-hidden="true" className="text-muted-foreground">{ct.icon}</span>
                      <span>{ct.label}</span>
                    </div>
                    <div className="text-xs text-muted-foreground line-clamp-2">{ct.desc}</div>
                  </Label>
                </div>
              ))}
            </RadioGroup>
          </div>

          <div className="flex gap-3 pt-3 border-t border-border">
            <Button onClick={handleRun} disabled={running} className="flex-1 h-11">
              {running ? <><Spinner size="xs" className="mr-2" />{t("privesc.running")}</> : <><Play className="size-4" />{t("privesc.exec_privesc")}</>}
            </Button>
          </div>
        </div>
      </Card>

      <Card className="overflow-hidden p-0">
        <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border flex items-center justify-between flex-wrap gap-3">
          <h2 className="text-lg font-semibold">{t("privesc.findings_title")} <span className="text-sm font-normal text-muted-foreground ml-2">{findings.length} </span></h2>
          <div className="flex items-center gap-2">
            {["all", "critical", "high", "medium", "low"].map((s) => (
              <Button key={s} variant={statusFilter === s ? "default" : "secondary"} size="xs" onClick={() => setStatusFilter(s)}>
                {s === "all" ? t("privesc.filter_all") : s.charAt(0).toUpperCase() + s.slice(1)}
              </Button>
            ))}
          </div>
        </div>
        <div className="divide-y divide-border max-h-[400px] overflow-y-auto">
          {loading ? (
            <div className="flex items-center justify-center py-12">
              <Spinner size="sm" />
            </div>
          ) : loadError ? (
            <DataError message={loadError} onRetry={() => { setLoading(true); void loadData(); }} className="py-10" />
          ) : findings.length === 0 ? (
            <EmptyState icon={ShieldCheck} title={t("privesc.no_findings")} message={t("privesc.no_findings_hint")} />
          ) : findings.filter((f) => statusFilter === "all" || f.severity === statusFilter).map((f, i) => {
            const fid = f.id || String(i);
            const isExpanded = expandedFinding === fid;
            return (
              <div key={fid} className="p-4 hover:bg-muted/50 transition-colors">
                <Collapsible open={isExpanded} onOpenChange={(open) => setExpandedFinding(open ? fid : null)}>
                  <div className="flex items-start justify-between gap-3">
                    <div className="flex items-start gap-3 flex-1">
                      {severityIcon(f.severity || "low")}
                      <div className="flex-1 min-w-0">
                        <div className="flex items-center gap-2 flex-wrap">
                          <span className="text-sm font-medium">{f.title || "-"}</span>
                          <Badge variant="secondary" className={`px-2 py-0.5 text-micro-sm rounded-full border font-medium ${severityBadge(f.severity || "low")}`}>
                            {(f.severity || "unknown").toUpperCase()}
                          </Badge>
                          {f.cve_id && <Badge variant="secondary" className="font-mono text-micro-sm">{f.cve_id}</Badge>}
                        </div>
                        <CollapsibleContent>
                          <div className="mt-3 space-y-2">
                            {f.description && <p className="text-sm text-muted-foreground">{f.description}</p>}
                            {f.recommendation && (
                              <p className="text-sm text-primary">
                                <Lightbulb className="size-4" />{t("privesc.recommendation_label")} {f.recommendation}
                              </p>
                            )}
                          </div>
                        </CollapsibleContent>
                      </div>
                    </div>
                    <CollapsibleTrigger render={<Button variant="ghost" size="icon-xs" aria-label={t("common.expand")} />}>
                      <ChevronDown className="size-4" />
                    </CollapsibleTrigger>
                  </div>
                </Collapsible>
              </div>
            );
          })}
        </div>
      </Card>

      <Card className="overflow-hidden p-0">
        <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
          <h2 className="text-lg font-semibold">{t("privesc.history_title")} <span className="text-sm font-normal text-muted-foreground ml-2">{history.length} </span></h2>
        </div>
        {history.length === 0 ? (
          <EmptyState icon={History} title={t("privesc.no_history")} />
        ) : (
          <Table className="text-sm">
            <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
              <TableRow className="hover:bg-transparent">
                <TableHead className="text-xs">{t("privesc.col_time")}</TableHead>
                <TableHead className="text-xs">{t("privesc.col_agent")}</TableHead>
                <TableHead className="text-xs">{t("privesc.check_type_label")}</TableHead>
                <TableHead className="text-xs">{t("privesc.col_status")}</TableHead>
                <TableHead className="text-xs">{t("privesc.col_result")}</TableHead>
                <TableHead className="text-xs">{t("privesc.col_action")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {history.map((item, i) => {
                const hid = item.id || String(i);
                return (
                  <TableRow key={hid}>
                    <TableCell className="text-xs font-mono text-muted-foreground">{formatTime(item.created_at || "")}</TableCell>
                    <TableCell className="text-xs font-mono">{(item.agent_id || "").substring(0, 8)}</TableCell>
                    <TableCell className="text-xs">{item.check_type || "-"}</TableCell>
                    <TableCell>
                      {(item.status) === "completed" ? (
                        <Badge variant="success">{item.status || "-"}</Badge>
                      ) : (item.status) === "running" ? (
                        <Badge variant="default">{item.status || "-"}</Badge>
                      ) : (
                        <Badge variant="secondary">{item.status || "-"}</Badge>
                      )}
                    </TableCell>
                    <TableCell className="text-xs text-primary font-medium">{item.findings_count ?? 0}</TableCell>
                    <TableCell>
                      <div className="flex items-center gap-1">
                         <Tooltip>
                           <TooltipTrigger render={<Button variant="ghost" size="icon-xs" onClick={() => handleViewHistory(hid)} aria-label={t("privesc.view_history")} />}>
                              <Eye className="size-4" />
                            </TooltipTrigger>
                           <TooltipContent>{t("privesc.view_result")}</TooltipContent>
                         </Tooltip>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
        )}
      </Card>

      {modal}
    </PageContainer>
  );
}


