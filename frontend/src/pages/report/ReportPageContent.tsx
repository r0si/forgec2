
import { registerFragment } from "@/lib/i18n";
import { enReport } from "@/lib/i18n/fragments/en.report";
registerFragment(enReport);

import { useState, useMemo } from "react";
import { paths } from "@/lib/api-paths";
import { api } from "@/lib/api";
import { downloadBlob, downloadText } from "@/lib/download";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { PageContainer } from "@/components/ui/page-container";
import { DataError } from "@/components/ui/data-state";
import { Spinner } from "@/components/ui/spinner";
import { StatCard } from "@/components/ui/animated-stat-card";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { StatusDot } from "@/components/ui/status-dot";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Progress } from "@/components/ui/progress";
import { RadioGroup, RadioGroupItem } from "@/components/ui/radio-group";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow, TableEmptyState } from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { CircleAlert, Crosshair, Download, Eye, FileText, Info, Inbox, Key, Lightbulb, ListChecks, PieChart, Radio, Bot, ShieldCheck, TriangleAlert, Trash2, Wand2 } from "lucide-react";
import { Accordion, AccordionItem, AccordionHeader, AccordionTrigger, AccordionPanel } from "@/components/ui/accordion";
import { severityColor } from "./components/types";
import { useReportData } from "./components/useReportData";
import IOCTab from "./components/IOCTab";

export default function ReportPage() {
  const [activeSection, setActiveSection] = useState("overview");
  const [viewingReport, setViewingReport] = useState<{ name: string; content: string } | null>(null);
  const [loadingReport, setLoadingReport] = useState(false);
  const [downloading, setDownloading] = useState<string | null>(null);

  const { t } = useI18n();
  const { confirm, modal } = useConfirm();
  const {
    stats,
    overviewError,
    previewError,
    previewPartial,
    previewFailedSections,
    historyError,
    refreshOverview,
    loading,
    generating,
    datePreset,
    setDatePreset,
    customStart,
    setCustomStart,
    customEnd,
    setCustomEnd,
    template,
    setTemplate,
    agents,
    taskStats,
    creds,
    listeners,
    findings,
    history,
    loadHistory,
    generateReport,
    deleteReport,
    htmlExportUrl,
  } = useReportData();

  const totalCreds = useMemo(() => creds.reduce((s, c) => s + (c.count ?? 1), 0), [creds]);

  const SECTIONS = [
    { key: "overview", label: t("report.sec_overview"), icon: <PieChart className="size-5" /> },
    { key: "agents", label: t("report.sec_agents"), icon: <Bot className="size-5" /> },
    { key: "tasks", label: t("report.sec_tasks"), icon: <ListChecks className="size-5" /> },
    { key: "credentials", label: t("report.sec_credentials"), icon: <Key className="size-5" /> },
    { key: "network", label: t("report.sec_network"), icon: <Radio className="size-5" /> },
    { key: "ioc", label: t("report.sec_ioc"), icon: <Crosshair className="size-5" /> },
    { key: "recommendations", label: t("report.sec_recommendations"), icon: <Lightbulb className="size-5" /> },
  ];

  const TEMPLATES = [
    { value: "full", label: t("report.tpl_full_label"), desc: t("report.tpl_full_desc") },
    { value: "executive", label: t("report.tpl_exec_label"), desc: t("report.tpl_exec_desc") },
    { value: "technical", label: t("report.tpl_tech_label"), desc: t("report.tpl_tech_desc") },
  ];

  const DATE_PRESETS = [
    { value: "7d", label: t("report.preset_7d") },
    { value: "30d", label: t("report.preset_30d") },
    { value: "90d", label: t("report.preset_90d") },
    { value: "custom", label: t("report.preset_custom") },
  ];

  const handleGenerate = async () => {
    let sections: string[];
    if (template === "technical") {
      sections = ["agents", "tasks", "credentials", "network", "recommendations"];
    } else if (template === "executive") {
      sections = ["overview", "recommendations"];
    } else {
      sections = SECTIONS.map((s) => s.key);
    }
    await generateReport(sections);
  };

  const downloadReport = async (path: string, fallbackFilename: string, key: string) => {
    if (downloading) return;
    setDownloading(key);
    try {
      const { blob, filename } = await api.downloadGet(path, fallbackFilename);
      downloadBlob(blob, filename);
    } catch {
      toast.error(t("report.toast.download_failed"));
    } finally {
      setDownloading(null);
    }
  };

  const handleExportPDF = async () => {
    await downloadReport(htmlExportUrl(), "forgec2-report.html", "html");
  };

  const handleHandover = async () => {
    await downloadReport(paths.report.handoverExport(30), "forgec2-handover.zip", "handover");
  };

  const handleDeleteReport = async (id: string) => {
    const ok = await confirm({ title: t("report.delete_title"), message: t("report.delete_confirm"), danger: true });
    if (!ok) return;
    await deleteReport(id);
  };

  const isAIReport = (tpl?: string) => (tpl || "").startsWith("ai_");

  const handleViewAIReport = async (id: string) => {
    setLoadingReport(true);
    try {
      const d = await api.get<{ report?: { name?: string; content?: string } }>(paths.report.generated(id));
      if (d.report?.content) {
        setViewingReport({ name: d.report.name || t("report.ai_report"), content: d.report.content });
      } else {
        toast.error(t("report.toast.load_failed"));
      }
    } catch {
      toast.error(t("report.toast.load_failed"));
    } finally {
      setLoadingReport(false);
    }
  };

  const handleDownloadAIReport = () => {
    if (!viewingReport) return;
    downloadText(viewingReport.content, `${viewingReport.name.replace(/[^\w-]+/g, "_")}.md`, "text/markdown");
  };

  if (loading) {
    return <PageContainer loading />;
  }

  return (
    <PageContainer
      title={t("report.title")} icon={<FileText className="size-4" />}
      subtitle={t("report.subtitle")}
      actions={
        <>
          <Button onClick={() => void handleHandover()} disabled={downloading !== null} variant="secondary" className="gap-x-2">
            {downloading === "handover" ? <Spinner size="xs" /> : <Download className="size-4" />}{t("report.handover")}
          </Button>
          <Button onClick={() => void handleExportPDF()} disabled={downloading !== null} variant="destructive" className="gap-x-2">
            {downloading === "html" ? <Spinner size="xs" /> : <FileText className="size-4" />}{t("report.export_html")}
          </Button>
          <Button onClick={handleGenerate} disabled={generating} className="gap-x-2">
            {generating ? <Spinner size="xs" /> : <Wand2 className="size-4" />}
            {generating ? t("report.generating") : t("report.generate")}
          </Button>
        </>
      }
    >

      {overviewError && (
        <DataError
          message={t("report.overview_unreadable", { message: overviewError })}
          onRetry={refreshOverview}
          className="mb-4"
        />
      )}

      <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 sm:gap-5">
        <StatCard color="indigo" label={t("report.stat_agents_total")} value={stats?.total_agents ?? "—"} sub={stats ? `${stats.online_agents || 0} ${t("report.online")}` : t("status.unknown")} subColor="text-success-ink" />
        <StatCard color="emerald" label={t("report.stat_task_exec")} value={stats?.total_tasks ?? "—"} sub={stats ? `${stats.success_tasks || 0} ${t("report.success")} / ${stats.failed_tasks || 0} ${t("report.failed")}` : t("status.unknown")} subColor="text-muted-foreground" />
        <StatCard color="amber" label={t("report.stat_creds")} value={stats?.total_creds ?? "—"} sub={stats ? t("report.collected") : t("status.unknown")} subColor="text-muted-foreground" />
        <StatCard color="destructive" label={t("report.stat_findings")} value={stats?.total_findings ?? "—"} sub={stats ? `${t("report.critical")}: ${stats.critical_findings || 0} | ${t("report.high")} ${stats.high_findings || 0}` : t("status.unknown")} subColor="text-destructive-ink" />
      </div>

      {previewError && (
        <DataError
          message={t("report.preview_unreadable", { message: previewError })}
          className="mb-4"
        />
      )}

      {previewPartial && !previewError && (
        <div className="mb-4 rounded-lg border border-amber-500/40 bg-amber-500/10 px-4 py-3 text-sm text-amber-700 dark:text-amber-300">
          <span className="font-medium">{t("report.partial_title")}</span>{" "}
          {t("report.partial_body", { sections: previewFailedSections.join(", ") })}
        </div>
      )}

      <Tabs value={activeSection} onValueChange={setActiveSection} orientation="vertical">
        <div className="grid grid-cols-1 lg:grid-cols-4 gap-4 mb-6">
          <div className="lg:col-span-1">
            <Card className="p-(--card-spacing) sticky lg:top-[96px]">
              <TabsList variant="sidebar" className="gap-1">
                {SECTIONS.map((s) => (
                  <TabsTrigger key={s.key} value={s.key}
                    className="w-full flex items-center gap-3 px-4 py-3 rounded-lg text-left text-sm font-medium transition-colors data-[selected]:bg-primary/10 data-[selected]:text-primary text-muted-foreground hover:bg-muted/50">
                    {s.icon}
                    {s.label}
                  </TabsTrigger>
                ))}
              </TabsList>
            </Card>
          </div>

          <div className="lg:col-span-3">
          <TabsContent value="overview" className="mt-0">
            <Card className="p-(--card-spacing)">
              <h2 className="text-lg font-semibold text-foreground mb-6">{t("report.settings")}</h2>
              <div className="space-y-6">
                <div>
                  <Label className="text-sm mb-3">{t("report.report_template")}</Label>
                  <RadioGroup value={template} onValueChange={setTemplate} className="grid grid-cols-1 sm:grid-cols-3 gap-3">
                    {TEMPLATES.map((tpl) => (
                      <div key={tpl.value} className={`flex flex-col items-start p-4 rounded-lg cursor-pointer transition-colors ${template === tpl.value ? "border-2 border-primary bg-primary/10" : "border border-border hover:bg-muted/50"}`}>
                        <div className="flex items-center space-x-2 mb-2">
                          <RadioGroupItem value={tpl.value} id={`tpl-${tpl.value}`} />
                          <Label htmlFor={`tpl-${tpl.value}`} className="text-sm text-foreground cursor-pointer">{tpl.label}</Label>
                        </div>
                        <div className="text-xs text-muted-foreground mt-1">{tpl.desc}</div>
                      </div>
                    ))}
                  </RadioGroup>
                </div>

                <div>
                  <Label className="text-sm mb-3">{t("report.date_range")}</Label>
                  <div className="flex flex-wrap gap-2 mb-3">
                    {DATE_PRESETS.map((p) => (
                      <Button key={p.value} variant={datePreset === p.value ? "default" : "secondary"} size="sm" onClick={() => setDatePreset(p.value)}>
                        {p.label}
                      </Button>
                    ))}
                  </div>
                  {datePreset === "custom" && (
                    <div className="grid grid-cols-2 gap-3">
                      <Input aria-label={t("report.start_date")} type="date" value={customStart} onChange={(e) => setCustomStart(e.target.value)} placeholder={t("report.start_date")} />
                      <Input aria-label={t("report.end_date")} type="date" value={customEnd} onChange={(e) => setCustomEnd(e.target.value)} placeholder={t("report.end_date")} />
                    </div>
                  )}
                </div>

                <div className="border-t border-border pt-4">
                  <h3 className="text-sm font-semibold text-foreground mb-3">{t("report.history_title")}</h3>
                  {historyError ? (
                    <DataError message={t("report.history_unreadable", { message: historyError })} onRetry={loadHistory} />
                  ) : history.length === 0 ? (
                    <div className="text-center py-6 text-muted-foreground">
                      <Inbox className="size-4" />
                      <p className="text-sm">{t("report.no_history")}</p>
                    </div>
                  ) : (
                    <div className="space-y-2">
                      {history.map((r, i) => {
                        const id = r.id || String(i);
                        return (
                          <div key={id} className="flex items-center justify-between p-3 bg-muted rounded-lg hover:bg-secondary transition-colors">
                            <div className="flex items-center gap-3">
                              <FileText className="size-4" />
                              <div>
                                <div className="text-sm font-medium text-muted-foreground">
                                  {isAIReport(r.template) && <Badge variant="info" className="mr-2">AI</Badge>}
                                  {r.name || r.template || t("report.unknown_template")} - {r.format?.toUpperCase() || "HTML"}
                                </div>
                                <div className="text-xs text-muted-foreground">{r.created_at || "-"} {r.size ? `· ${r.size}` : ""}</div>
                              </div>
                            </div>
                            <div className="flex items-center gap-2">
                              {isAIReport(r.template) && (
                                <Button variant="ghost" size="icon" onClick={() => handleViewAIReport(id)} disabled={loadingReport} aria-label={t("report.a11y_view")}>
                                  <Eye className="size-4" />
                                </Button>
                              )}
                              <Button
                                variant="ghost"
                                size="icon"
                                disabled={downloading !== null}
                                onClick={() => void downloadReport(paths.report.download(id), `report-${id}.html`, `history-${id}`)}
                                className="text-primary hover:bg-primary/10 dark:hover:bg-primary/20"
                                aria-label={t("report.download")}
                              >
                                {downloading === `history-${id}` ? <Spinner size="xs" /> : <Download className="size-4" />}
                              </Button>
                              <Button variant="ghost" size="icon" onClick={() => handleDeleteReport(id)} className="text-destructive-ink hover:bg-destructive/10" aria-label={t("report.a11y_delete")}>
                                <Trash2 className="size-4" />
                              </Button>
                            </div>
                          </div>
                        );
                      })}
                    </div>
                  )}
                </div>
              </div>
            </Card>
          </TabsContent>

          <TabsContent value="agents" className="mt-0">
            <Card className="overflow-hidden shadow-sm hover:shadow-md transition-shadow duration-200">
              <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                <h2 className="text-lg font-semibold text-foreground">{t("report.agents_detail")} <span className="text-sm font-normal text-muted-foreground ml-2">{t("report.total")} {agents.length} {t("report.units")}</span></h2>
              </div>
              <div className="overflow-x-auto scrollbar-thin">
                <Table>
                  <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
                    <TableRow className="text-xs hover:bg-transparent">
                      <TableHead className="font-medium">{t("report.col_hostname")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_ip")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_os")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_lastseen")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_status")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {agents.length === 0 ? (
                      <TableEmptyState colSpan={5} message={t("report.no_data")} />
                    ) : agents.map((a, i) => (
                      <TableRow key={a.id || i}>
                        <TableCell className="font-medium truncate max-w-[200px]">{a.hostname || "-"}</TableCell>
                        <TableCell className="font-mono text-xs truncate max-w-[200px]">{a.ip || "-"}</TableCell>
                        <TableCell>{a.os || "-"}</TableCell>
                        <TableCell className="text-xs">{a.last_seen || "-"}</TableCell>
                        <TableCell>
                          <Badge variant={a.status === "online" ? "success" : "secondary"} className="gap-1">
                            <StatusDot tone={a.status === "online" ? "success" : "muted"} size="xs" />
                            {a.status || "unknown"}
                          </Badge>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
          </TabsContent>

          <TabsContent value="tasks" className="mt-0">
            <Card className="overflow-hidden">
              <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                <h2 className="text-lg font-semibold text-foreground">{t("report.task_stats")} <span className="text-sm font-normal text-muted-foreground ml-2">{t("report.by_type")}</span></h2>
              </div>
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow className="text-xs">
                      <TableHead className="font-medium">{t("report.col_task_type")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_total")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_success")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_failed")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_success_rate")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {taskStats.length === 0 ? (
                      <TableEmptyState colSpan={5} message={t("report.no_data")} />
                    ) : taskStats.map((ts) => (
                      <TableRow key={ts.type}>
                        <TableCell className="font-medium">{ts.type || "-"}</TableCell>
                        <TableCell>{ts.total ?? 0}</TableCell>
                        <TableCell className="text-success-ink">{ts.success ?? 0}</TableCell>
                        <TableCell className="text-destructive-ink">{ts.failed ?? 0}</TableCell>
                        <TableCell>
                          <div className="flex items-center gap-2">
                            <Progress value={ts.success_rate ?? Math.round(((ts.success ?? 0) / (ts.total || 1)) * 100)} className="flex-1 max-w-[100px]" />
                            <span className="text-xs tabular-nums">{ts.success_rate ?? Math.round(((ts.success ?? 0) / (ts.total || 1)) * 100)}%</span>
                          </div>
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
          </TabsContent>

          <TabsContent value="credentials" className="mt-0">
            <Card className="overflow-hidden">
              <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                <h2 className="text-lg font-semibold text-foreground">{t("report.cred_summary")}<span className="text-sm font-normal text-muted-foreground ml-2">{t("report.total_prefix")}{totalCreds} {t("report.items")}</span></h2>
              </div>
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow className="text-xs">
                      <TableHead className="font-medium">{t("report.col_cred_type")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_count")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_source")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {creds.length === 0 ? (
                      <TableEmptyState colSpan={3} message={t("report.no_data")} />
                    ) : creds.map((c) => (
                      <TableRow key={c.type}>
                        <TableCell className="font-medium">{c.type || "-"}</TableCell>
                        <TableCell className="text-primary font-semibold">{c.count ?? 0}</TableCell>
                        <TableCell>{c.source || "-"}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
          </TabsContent>

          <TabsContent value="network" className="mt-0">
            <Card className="overflow-hidden">
              <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                <h2 className="text-lg font-semibold text-foreground">{t("report.network_status")} <span className="text-sm font-normal text-muted-foreground ml-2">{t("report.listener_overview")}</span></h2>
              </div>
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow className="text-xs">
                      <TableHead className="font-medium">{t("report.col_listener")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_protocol")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_status")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_agent_count")}</TableHead>
                      <TableHead className="font-medium">{t("report.col_traffic")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {listeners.length === 0 ? (
                      <TableEmptyState colSpan={5} message={t("report.no_data")} />
                    ) : listeners.map((l, i) => (
                      <TableRow key={l.id || i}>
                        <TableCell className="font-medium">{l.name || "-"}</TableCell>
                        <TableCell><Badge variant="secondary" className="font-mono">{l.protocol || "-"}</Badge></TableCell>
                        <TableCell>
                          <span className={`inline-flex items-center gap-1 text-xs font-medium ${l.status === "active" ? "text-success-ink" : "text-muted-foreground"}`}>
                            <StatusDot tone={l.status === "active" ? "success" : "muted"} size="xs" />
                            {l.status || "-"}
                          </span>
                        </TableCell>
                        <TableCell>{l.agent_count ?? 0}</TableCell>
                        <TableCell>{l.traffic || "-"}</TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              </div>
            </Card>
          </TabsContent>

          <TabsContent value="ioc" className="mt-0">
            <IOCTab />
          </TabsContent>

          <TabsContent value="recommendations" className="mt-0">
            <Card className="overflow-hidden">
              <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                <h2 className="text-lg font-semibold text-foreground">{t("report.sec_findings_advice")}<span className="text-sm font-normal text-muted-foreground ml-2">{t("report.total_prefix")}{findings.length} {t("report.items")}</span></h2>
              </div>
              {findings.length === 0 ? (
                <div className="py-16 sm:py-20 text-center text-muted-foreground">
                  <ShieldCheck className="size-4" />
                  <p className="text-sm">{t("report.no_findings")}</p>
                </div>
              ) : (
                <Accordion>
                  {findings.map((f, i) => {
                    const id = f.id || String(i);
                    return (
                      <AccordionItem key={id} value={id}>
                        <AccordionHeader>
                          <AccordionTrigger className="px-4 py-3 hover:bg-muted/50">
                            <div className="flex items-start gap-3 flex-1 text-left">
                              <div className="mt-0.5">
                                {f.severity === "critical" ? <CircleAlert className="size-4 text-destructive-ink" /> : f.severity === "high" ? <TriangleAlert className="size-4 text-warning-ink" /> : f.severity === "medium" ? <CircleAlert className="size-4 text-warning-ink" /> : <Info className="size-4 text-info-ink" />}
                              </div>
                              <div className="flex-1">
                                <div className="flex items-center gap-2 flex-wrap">
                                  <span className="text-sm font-medium text-foreground">{f.title || "-"}</span>
                                  <Badge variant="outline" className={`text-micro-sm ${severityColor(f.severity || "low")}`}>
                                    {(f.severity || "unknown").toUpperCase()}
                                  </Badge>
                                  {f.cve_id && <Badge variant="secondary" className="text-micro-sm font-mono">{f.cve_id}</Badge>}
                                </div>
                              </div>
                            </div>
                          </AccordionTrigger>
                        </AccordionHeader>
                        <AccordionPanel className="px-4 pb-4">
                          <div className="space-y-2">
                            <p className="text-sm text-muted-foreground">{f.description || ""}</p>
                            {f.recommendation && (
                              <p className="text-sm text-primary">
                                <Lightbulb className="size-4" />{t("report.recommendation_label")} {f.recommendation}
                              </p>
                            )}
                          </div>
                        </AccordionPanel>
                      </AccordionItem>
                    );
                  })}
                </Accordion>
              )}
            </Card>
          </TabsContent>
        </div>
      </div>
      </Tabs>

      <Dialog open={!!viewingReport} onOpenChange={(open) => { if (!open) setViewingReport(null); }}>
        <DialogContent size="2xl" className="max-h-[80vh] flex flex-col">
          <DialogHeader>
            <DialogTitle className="flex items-center gap-2 truncate">
              <Wand2 className="size-4 shrink-0" />{viewingReport?.name}
            </DialogTitle>
          </DialogHeader>
          <pre className="flex-1 overflow-auto whitespace-pre-wrap font-mono text-xs bg-muted/50 rounded-lg p-4 text-foreground scrollbar-thin">
            {viewingReport?.content}
          </pre>
          <DialogFooter>
            <Button variant="outline" onClick={() => setViewingReport(null)}>{t("common.close")}</Button>
            <Button onClick={handleDownloadAIReport} className="gap-x-2">
              <Download className="size-4" />{t("report.download_md")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {modal}
    </PageContainer>
  );
}
