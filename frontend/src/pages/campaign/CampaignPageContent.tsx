
import { registerFragment } from "@/lib/i18n";
import { enCampaign } from "@/lib/i18n/fragments/en.campaign";
registerFragment(enCampaign);

import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";

import { formatTime } from "@/lib/utils";
import { useI18n } from "@/lib/i18n";
import { phaseColor } from "@/lib/chart-palette";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";
import { PageContainer } from "@/components/ui/page-container";
import { Spinner } from "@/components/ui/spinner";
import { StatCard } from "@/components/ui/animated-stat-card";
import { toast } from "sonner";

import { Card } from "@/components/ui/card";
import { SectionCard } from "@/components/ui/section-card";
import { Banner } from "@/components/ui/banner";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { StatusBadge } from "@/components/ui/status-indicator";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { ArrowLeft, Check, Clock, ListChecks, ListTodo, Play, Plus, RefreshCw, Trash2, TriangleAlert, Zap } from "lucide-react";
import {
  Dialog,
  DialogContent,
  DialogHeader,
  DialogTitle,
  DialogFooter,
} from "@/components/ui/dialog";
import { Collapsible, CollapsibleContent } from "@/components/ui/collapsible";
import {
  PHASE_ORDER,
  type Campaign,
  type CampaignMITRE,
  type CampaignStats,
  type KillChainTemplate,
  type PhaseEvent,
  type AgentStat,
  type PhaseTask,
} from "./components/types";
import { useCampaignData } from "./components/useCampaignData";

export default function CampaignPageContent() {
  const { t } = useI18n();
  const {
    campaigns,
    loading,
    listError,
    selectedCampaign,
    setSelectedCampaign,
    campaignStats,
    statsLoading,
    statsError,
    creating,
    createCampaign,
    deleteCampaign,
    updateStatus,
    loadCampaigns,
    loadCampaignDetail,
  } = useCampaignData();

  const [showCreate, setShowCreate] = useState(false);
  const [newName, setNewName] = useState("");
  const [newDesc, setNewDesc] = useState("");
  const { confirm, modal } = useConfirm();

  const handleCreate = async () => {
    const ok = await createCampaign(newName, newDesc);
    if (ok) {
      setNewName("");
      setNewDesc("");
      setShowCreate(false);
    }
  };

  const handleDelete = async (id: string) => {
    if (!(await confirm({ message: t("campaign.delete_confirm") }))) return;
    await deleteCampaign(id);
  };

  const handleStatusChange = async (id: string, status: string) => {
    await updateStatus(id, status);
  };

  const selected = campaigns.find((c) => c.id === selectedCampaign);

  if (loading) return <div className="p-8 text-center text-muted-foreground"><Spinner size="sm" /> {t("campaign.loading_stats")}</div>;

  return (
    <>
      <PageContainer title={t("campaign.title")} subtitle={t("campaign.subtitle")} actions={<>
        <Button onClick={() => setShowCreate(true)}>
          <Plus className="size-4" />{t("campaign.new")}
        </Button>
      </>}>

      <Banner tone="warning" className="items-start">
        <div className="font-semibold">{t("campaign.honesty_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("campaign.honesty_desc")}</div>
      </Banner>

      <Dialog open={showCreate} onOpenChange={setShowCreate}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("campaign.dialog_create")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <Input aria-label={t("campaign.a11y_name")} name="campaign-name-0" placeholder={t("campaign.field_name")} value={newName}
              onChange={(e) => setNewName(e.target.value)} />
            <Textarea aria-label={t("campaign.desc_optional")} name="description-optional-1" rows={2} placeholder={t("campaign.field_desc")} value={newDesc}
              onChange={(e) => setNewDesc(e.target.value)} />
          </div>
          <DialogFooter>
            <Button variant="ghost" onClick={() => setShowCreate(false)}>{t("common.cancel")}</Button>
            <Button onClick={handleCreate} disabled={creating || !newName.trim()}>
              {creating ? t("common.saving") : t("common.create")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {selectedCampaign && selected ? (
        <CampaignDetailView
          key={selected.id}
          campaign={selected}
          stats={campaignStats}
          statsLoading={statsLoading}
          statsError={statsError}
          onRetryStats={() => { if (selected.id) void loadCampaignDetail(selected.id); }}
          onBack={() => setSelectedCampaign(null)}
          onDelete={() => handleDelete(selected.id)}
          onStatusChange={(s) => handleStatusChange(selected.id, s)}
          formatTime={formatTime}
        />
      ) : (
        <Card className="p-0 overflow-hidden shadow-sm hover:shadow-md transition-shadow duration-200">
          <Table className="text-sm">
            <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
              <TableRow className="hover:bg-transparent">
                <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_name")}</TableHead>
                <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_status")}</TableHead>
                <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_agents")}</TableHead>
                <TableHead className="max-sm:hidden py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_created")}</TableHead>
                <TableHead className="text-right py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {listError && (
                <TableRow><TableCell colSpan={5} className="py-10"><DataError message={t("campaign.list_unreadable", { message: listError })} onRetry={() => { void loadCampaigns(); }} /></TableCell></TableRow>
              )}
              {!listError && campaigns.length === 0 && (
                <TableRow><TableCell colSpan={5} className="text-center text-muted-foreground py-10 sm:py-12"><EmptyState icon={ListTodo} title={t("campaign.empty")} message={t("campaign.subtitle")} /></TableCell></TableRow>
              )}
              {campaigns.map((c) => (
                <TableRow key={c.id} className="cursor-pointer transition-colors"
                  tabIndex={0} role="button"
                  onClick={() => setSelectedCampaign(c.id)}
                  onKeyDown={(e) => { if (e.key === "Enter" || e.key === " ") { e.preventDefault(); setSelectedCampaign(c.id); } }}>
                  <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 font-medium text-foreground">{c.name}</TableCell>
                  <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5">
                    <StatusBadge status={c.status} label={t(`campaign.status_${c.status}`)} />
                  </TableCell>
                  <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-muted-foreground">{c.agents?.length || 0}</TableCell>
                  <TableCell className="max-sm:hidden py-3 px-4 sm:py-3.5 sm:px-5 text-muted-foreground text-sm">{formatTime(c.created_at)}</TableCell>
                  <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-right">
                    <Button variant="ghost" size="icon-xs" className="text-destructive-ink"
                      onClick={(e) => { e.stopPropagation(); handleDelete(c.id); }} aria-label={t("campaign.a11y_delete")}>
                      <Trash2 className="size-4" />
                    </Button>
                  </TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        </Card>
      )}
      {modal}
    </PageContainer>
    </>
  );
}

function CampaignDetailView({
  campaign, stats, statsLoading, statsError, onRetryStats, onBack, onDelete, onStatusChange, formatTime,
}: {
  campaign: Campaign;
  stats: CampaignStats | null;
  statsLoading: boolean;
  statsError: string | null;
  onRetryStats: () => void;
  onBack: () => void;
  onDelete: () => void;
  onStatusChange: (s: string) => void;
  formatTime: (t: string) => string;
}) {
  const { t } = useI18n();
  const [mitreData, setMitreData] = useState<CampaignMITRE | null>(null);
  const [templates, setTemplates] = useState<KillChainTemplate[]>([]);
  const [selectedTemplate, setSelectedTemplate] = useState("");
  const [executing, setExecuting] = useState(false);
  const [expandedPhase, setExpandedPhase] = useState<string | null>(null);
  const [phaseTasks, setPhaseTasks] = useState<Record<string, PhaseTask[]>>({});
  const [showTimeline, setShowTimeline] = useState(false);
  const [mitreError, setMitreError] = useState<string | null>(null);
  const [templatesError, setTemplatesError] = useState<string | null>(null);

  const abortRef = useRef<AbortController | null>(null);

  const loadSources = useCallback(() => {
    abortRef.current?.abort();
    const controller = new AbortController();
    abortRef.current = controller;
    setMitreError(null);
    setTemplatesError(null);
    void (async () => {
      // Settle independently: one failed source must not blank the other, and a
      // missing kill-chain template list must not look like "no templates".
      const [mitreRes, tplRes] = await Promise.allSettled([
        api.get<CampaignMITRE | { success: boolean; data?: CampaignMITRE }>(paths.campaigns.mitre(campaign.id), { signal: controller.signal }),
        api.get<KillChainTemplate[] | { success: boolean; data?: KillChainTemplate[] }>(paths.mitre.templates, { signal: controller.signal }),
      ]);
      if (controller.signal.aborted) return;
      if (mitreRes.status === "fulfilled") {
        // api.get auto-unwraps {success,data}: consume the payload directly —
        // checking .success here was always undefined (list/killchain dead).
        const mitre = (mitreRes.value as CampaignMITRE).phases !== undefined
          ? (mitreRes.value as CampaignMITRE)
          : (mitreRes.value as { data?: CampaignMITRE }).data;
        if (mitre) setMitreData(mitre);
      } else {
        setMitreError(mitreRes.reason instanceof Error ? mitreRes.reason.message : t("campaign.toast.load_failed"));
        toast.error(t("campaign.toast.load_failed"));
      }
      if (tplRes.status === "fulfilled") {
        const tpls = Array.isArray(tplRes.value)
          ? (tplRes.value as KillChainTemplate[])
          : (tplRes.value as { data?: KillChainTemplate[] }).data;
        if (tpls) setTemplates(tpls);
      } else {
        setTemplatesError(tplRes.reason instanceof Error ? tplRes.reason.message : t("campaign.toast.load_failed"));
        toast.error(t("campaign.toast.load_failed"));
      }
    })();
  }, [campaign.id, t]);

  useEffect(() => {
    loadSources();
    return () => abortRef.current?.abort();
  }, [loadSources]);

  const loadPhaseTasks = async (phase: string) => {
    try {
      const json = await api.get<PhaseTask[] | {success: boolean; data?: PhaseTask[]}>(paths.mitre.timeline(`campaign_id=${campaign.id}`));
      const events = Array.isArray(json) ? json : json.data || [];
      const tasks = events.filter((e: PhaseTask) => e.phase === phase);
      setPhaseTasks((prev) => ({ ...prev, [phase]: tasks }));
    } catch { toast.error(t("campaign.toast.load_failed")); }
  };

  const handlePhaseClick = (phase: string) => {
    if (expandedPhase === phase) {
      setExpandedPhase(null);
    } else {
      setExpandedPhase(phase);
      loadPhaseTasks(phase);
    }
  };

  const handleExecuteTemplate = async () => {
    if (!selectedTemplate) return;
    setExecuting(true);
    try {
      const res = await api.postJson<{ tasks_created?: number; agents_skipped?: number }>(
        paths.campaigns.killchain(campaign.id), { template: selectedTemplate });
      toast.success(t("campaign.toast.seeded", {
        n: String(res?.tasks_created ?? 0),
        skipped: String(res?.agents_skipped ?? 0),
      }));
      const mitreJson = await api.get<CampaignMITRE | {success: boolean; data?: CampaignMITRE}>(paths.campaigns.mitre(campaign.id));
      const mitre = (mitreJson as CampaignMITRE).phases !== undefined
        ? (mitreJson as CampaignMITRE)
        : (mitreJson as { data?: CampaignMITRE }).data;
      if (mitre) setMitreData(mitre);
    } catch (e) {
      // Distinct message: this is an execute failure, not a load failure.
      toast.error(e instanceof Error && e.message ? e.message : t("campaign.toast.load_failed"));
    }
    setExecuting(false);
    setSelectedTemplate("");
  };

  const phases = mitreData?.phases || [];

  return (
    <div>
      {mitreError && (
        <DataError
          message={t("campaign.mitre_unreadable", { message: mitreError })}
          onRetry={loadSources}
          className="mb-4"
        />
      )}
      {templatesError && (
        <DataError
          message={t("campaign.templates_unreadable", { message: templatesError })}
          onRetry={loadSources}
          className="mb-4"
        />
      )}
      <div className="flex items-center gap-3 mb-6">
        <Button variant="ghost" size="icon" onClick={onBack} aria-label={t("campaign.a11y_back")}><ArrowLeft className="size-4" /></Button>
        <div className="flex-1">
          <h2 className="text-2xl font-semibold tracking-tight text-foreground leading-tight">{campaign.name}</h2>
          {campaign.description && <p className="text-muted-foreground text-sm">{campaign.description}</p>}
        </div>
        <div className="flex gap-2">
          <Select value={campaign.status} onValueChange={(v) => { if (v) onStatusChange(v); }}>
            <SelectTrigger className="w-32 text-sm">
              <SelectValue />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="active">{t("campaign.status_active")}</SelectItem>
              <SelectItem value="completed">{t("campaign.status_completed")}</SelectItem>
              <SelectItem value="archived">{t("campaign.status_archived")}</SelectItem>
            </SelectContent>
          </Select>
          <Button variant="ghost" size="icon" className="text-destructive-ink" onClick={onDelete} aria-label={t("campaign.a11y_delete")}>
            <Trash2 className="size-4" />
          </Button>
        </div>
      </div>

      {stats ? (
        <>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-4 mb-6">
            <StatCard label={t("campaign.total_agents")} value={stats.total_agents} />
            <StatCard label={t("campaign.total_tasks")} value={stats.total_tasks} />
            <StatCard label={t("campaign.completed")} value={stats.completed_tasks} color="emerald" />
            <StatCard label={t("campaign.failed")} value={stats.failed_tasks} color="destructive" />
          </div>

          {/* Kill Chain Phase Progress Bar */}
<SectionCard
  title={t("campaign.kill_chain")}
  icon={ListTodo}
  tone="primary"
  headerAccent
  className="mb-6"
  action={
    <Button variant="ghost" size="sm" onClick={() => setShowTimeline(!showTimeline)}>
      {showTimeline ? <ListChecks className="size-4 mr-1" /> : <Clock className="size-4 mr-1" />}
      {showTimeline ? t("campaign.kill_chain") : t("campaign.timeline")}
    </Button>
  }
>
            <div className="p-(--card-spacing)">
            {!showTimeline ? (
              mitreError && !mitreData ? (
                // An unreadable MITRE read must not render as the all-pending
                // phase bar: that looks like a fresh campaign, not a failed
                // request (the banner above carries the detail + retry).
                <p className="text-xs text-muted-foreground py-3" role="alert">
                  {t("campaign.mitre_unreadable", { message: mitreError })}
                </p>
              ) : (
              <>
                {/* Horizontal Phase Progress Bar */}
                <Collapsible open={expandedPhase !== null} onOpenChange={(open) => { if (!open) setExpandedPhase(null); }}>
                <div className="w-full overflow-x-auto pb-2">
                  <div className="flex gap-1 min-w-max">
                    {PHASE_ORDER.map((phase) => {
                      const found = phases.find((p) => p.phase === phase);
                      const isCompleted = found?.status === "completed";
                      const isPending = !found || found.task_count === 0;
                      return (
                      <button
                        type="button"
                        key={phase}
                        onClick={() => handlePhaseClick(phase)}
                        className={`flex w-[84px] shrink-0 flex-col items-center gap-1 rounded-lg p-2 transition-all focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50 ${
                          expandedPhase === phase ? "ring-2 ring-primary" : ""
                        } ${isCompleted ? "bg-success/15" : isPending ? "hover:bg-muted/50" : "bg-warning/15"}`}
                        aria-label={phase}
                        title={phase}
                      >
                          <div className={`size-8 rounded-full flex items-center justify-center text-xs font-bold transition-colors ${
                            isCompleted ? "bg-success text-success-foreground" : isPending ? "bg-muted-foreground text-background" : "bg-warning-solid text-warning-foreground"
                          }`}>
                            {isCompleted ? <Check className="size-4" /> : isPending ? "" : <Play className="size-4" />}
                          </div>
                          <span className={`line-clamp-2 h-8 whitespace-normal text-micro-sm text-center leading-tight ${isCompleted ? "text-success-ink font-medium" : isPending ? "text-muted-foreground" : "text-warning-ink"}`}>
                            {phase}
                          </span>
                          {found && (
                            <span className="text-micro text-muted-foreground">{found.task_count}</span>
                          )}
                          </button>
                      );
                    })}
                  </div>
                </div>

                {/* Expanded phase tasks */}
                <CollapsibleContent>
                {expandedPhase && phaseTasks[expandedPhase] && phaseTasks[expandedPhase].length > 0 && (
                  <div className="mt-3 p-3 rounded-lg bg-muted/50">
                    <h4 className="text-sm font-semibold mb-2">{expandedPhase} {t("campaign.phase_tasks")}</h4>
                    <div className="space-y-1 max-h-48 overflow-y-auto">
                      {phaseTasks[expandedPhase].map((task: PhaseTask, i: number) => (
                        <div key={i} className="flex items-center justify-between text-xs py-1 px-2 rounded bg-card">
                          <span className="font-mono">{task.task_type}</span>
                          <span className="text-muted-foreground">{task.hostname || task.agent_id?.slice(0, 8)}</span>
                          <span className={`${
                            task.status === "completed" ? "text-success-ink" :
                            task.status === "failed" ? "text-destructive-ink" : "text-warning-ink"
                          }`}>{t(`tasks.${task.status}`)}</span>
                        </div>
                      ))}
                    </div>
                  </div>
                )}
                </CollapsibleContent>
                </Collapsible>
              </>
              )
            ) : mitreError && !mitreData ? (
              <p className="text-xs text-muted-foreground py-3" role="alert">
                {t("campaign.mitre_unreadable", { message: mitreError })}
              </p>
            ) : (
              <div className="relative pl-6 border-l-2 border-border">
                {(mitreData?.timeline || []).length === 0 ? (
                  <p className="text-muted-foreground text-sm py-2">{t("campaign.empty_timeline")}</p>
                ) : (
                  (mitreData?.timeline || []).map((event, i) => (
                    <div key={i} className="mb-3 relative">
                      <div className="absolute -left-[calc(1.5rem+1px)] top-1 size-3 rounded-full border-2 border-card"
                        style={{ background: phaseColor(event.phase) }} />
                      <div className="text-sm font-medium">{event.phase}</div>
                      <div className="text-xs text-muted-foreground">{formatTime(event.first_seen)}</div>
                      <div className="text-xs text-muted-foreground">{event.task_count} {t("campaign.tasks")}</div>
                    </div>
                  ))
                )}
              </div>
            )}
            </div>
          </SectionCard>

          {/* Kill Chain Template Executor */}
<SectionCard
  title={t("campaign.templates")}
  icon={Zap}
  tone="warning"
  headerAccent
  className="mb-6"
>
            <div className="p-(--card-spacing)">
            {templatesError && templates.length === 0 ? (
              // An unreadable template list must not render as an empty
              // selector: that looks like "no templates exist", not a failed
              // request (the banner above carries the detail + retry).
              <p className="text-xs text-muted-foreground py-3" role="alert">
                {t("campaign.templates_unreadable", { message: templatesError })}
              </p>
            ) : (
            <div className="flex gap-2 items-end">
              <div className="flex-1">
                <Select value={selectedTemplate} onValueChange={(v) => setSelectedTemplate(v ?? "")}>
                  <SelectTrigger className="w-full text-sm">
                    <SelectValue placeholder={t("campaign.field_name")} />
                  </SelectTrigger>
                  <SelectContent>
                    {templates.map((tpl) => (
                      <SelectItem key={tpl.name} value={tpl.name}>{tpl.name}</SelectItem>
                    ))}
                  </SelectContent>
                </Select>
                {selectedTemplate && (
                  <p className="text-xs text-muted-foreground mt-1">
                    {templates.find((t) => t.name === selectedTemplate)?.description}
                  </p>
                )}
              </div>
              <Button size="sm" disabled={!selectedTemplate || executing}
                onClick={handleExecuteTemplate}>
                {executing ? <Spinner size="xs" className="mr-1" /> : <Play className="size-4" />}
                {t("campaign.execute")}
              </Button>
            </div>
            )}
            {selectedTemplate && (
              <div className="mt-3">
                <h4 className="text-xs font-semibold text-muted-foreground mb-2">{t("campaign.steps")}</h4>
                <div className="flex flex-wrap gap-1">
                  {templates.find((t) => t.name === selectedTemplate)?.steps?.map((step, i) => (
                    <Badge key={`${step.task_type}-${i}`} variant="secondary" className="inline-flex items-center gap-1 px-2 py-1 rounded text-xs">
                      <span className="size-2 rounded-full" style={{ background: phaseColor(step.phase) }} />
                      {step.task_type}
                    </Badge>
                  )) ?? null}
                </div>
              </div>
            )}
            </div>
          </SectionCard>

          {/* Phase Timeline (legacy) */}
<SectionCard
  title={t("campaign.timeline")}
  icon={Clock}
  tone="info"
  headerAccent
  className="mb-6"
>
            <div className="p-(--card-spacing)">
            {stats.phase_timeline.length === 0 ? (
              <p className="text-muted-foreground">{t("campaign.empty_timeline")}</p>
            ) : (
              <div className="relative pl-6 border-l-2 border-border">
                {stats.phase_timeline.map((event: PhaseEvent, i: number) => (
                  <div key={i} className="mb-4 relative">
                    <div className="absolute -left-[calc(1.5rem+1px)] top-1 size-3 rounded-full border-2 border-card"
                      style={{ background: phaseColor(event.phase) }} />
                    <div className="text-sm font-medium">{event.phase}</div>
                    <div className="text-xs text-muted-foreground">{formatTime(event.first_seen)}</div>
                    <div className="text-xs text-muted-foreground">{event.task_count} {t("campaign.tasks")}</div>
                  </div>
                ))}
              </div>
            )}
            </div>
          </SectionCard>

          <SectionCard
            title={t("campaign.agent_breakdown")}
            icon={ListChecks}
            tone="success"
            headerAccent
          >
            <div className="p-(--card-spacing)">
            {stats.agent_breakdown.length === 0 ? (
              <p className="text-muted-foreground">{t("campaign.empty_agents")}</p>
            ) : (
              <Table>
                <TableHeader>
                  <TableRow>
                    <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_hostname")}</TableHead>
                    <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_user")}</TableHead>
                    <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">IP</TableHead>
                    <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_tasks")}</TableHead>
                    <TableHead className="py-3 px-4 sm:py-3.5 sm:px-5">{t("campaign.col_phases")}</TableHead>
                  </TableRow>
                </TableHeader>
                <TableBody>
                  {stats.agent_breakdown.map((a: AgentStat) => (
                    <TableRow key={a.agent_id}>
                      <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-foreground truncate max-w-[200px]">{a.hostname}</TableCell>
                      <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-muted-foreground truncate max-w-[200px]">{a.username}</TableCell>
                      <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-muted-foreground">{a.ip}</TableCell>
                      <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-muted-foreground">{a.task_count}</TableCell>
                      <TableCell className="py-3 px-4 sm:py-3.5 sm:px-5 text-xs">
                        {Object.entries(a.phases || {}).map(([phase, count]) => (
                          <Badge key={phase} variant="secondary" className="inline-block mr-1 mb-1 px-1.5 py-0.5 rounded text-xs">
                            {phase}:{String(count)}
                          </Badge>
                        ))}
                      </TableCell>
                    </TableRow>
                  ))}
                </TableBody>
              </Table>
            )}
            </div>
          </SectionCard>
        </>
      ) : statsError ? (
        <div className="flex flex-col sm:flex-row sm:items-center gap-3 p-4 bg-destructive/10 rounded-lg border border-destructive/20">
          <TriangleAlert className="size-4 text-destructive-ink shrink-0" />
          <p className="flex-1 text-sm text-destructive-ink">{statsError}</p>
          <Button variant="outline" size="sm" onClick={onRetryStats} disabled={statsLoading} className="shrink-0">
            <RefreshCw className="size-4" />{t("common.try_again")}
          </Button>
        </div>
      ) : (
        <p className="text-center text-muted-foreground py-8">
          <Spinner size="sm" /> {t("campaign.loading_stats")}
        </p>
      )}
    </div>
  );
}

