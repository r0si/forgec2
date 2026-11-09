import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
import { enGenerate } from "@/lib/i18n/fragments/en.generate";
registerFragment(enAgents);
registerFragment(enGenerate);

import { useEffect, useRef, useState, useCallback } from "react";
import { useParams } from "react-router-dom";
import { Link } from "react-router-dom";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { useMutation } from "@/lib/hooks/useMutation";

import { Spinner, PageSpinner } from "@/components/ui/spinner";
import { toast } from "sonner";
import { SectionCard } from "@/components/ui/section-card";
import { Banner } from "@/components/ui/banner";
import { PageContainer } from "@/components/ui/page-container";
import { DataError } from "@/components/ui/data-state";
import { Button } from "@/components/ui/button";
import { CalendarCheck, Cog, Boxes, FolderOpen, Puzzle, Server, Settings } from "lucide-react";
import { Bug, Info, Link as LinkIcon, ListChecks, Plug, RotateCw, Trash2 } from "lucide-react";

import type { AgentDetail } from "@/types/agent";
import { persistenceMethodQuality, type DestQuality } from "../components/dest-quality";
import { implantBlocksDest } from "@/lib/implant-version";

function PersistQualityMark({ quality }: { quality: DestQuality }) {
  const { t } = useI18n();
  if (quality === "experimental") return <>{t("generate.quality_experimental")}</>;
  if (quality === "scripted") return <>{t("cred.quality_scripted")}</>;
  if (quality === "core") return <>{t("generate.quality_core")}</>;
  return <>{t("generate.quality_hardened")}</>;
}

const PERSISTENCE_METHODS: { key: string; labelKey: string; icon: React.ReactNode }[] = [
  { key: "registry", labelKey: "agents.persistence_registry", icon: <Settings className="size-4" /> },
  { key: "scheduled_task", labelKey: "agents.persistence_scheduled_task", icon: <CalendarCheck className="size-4" /> },
  { key: "startup_folder", labelKey: "agents.persistence_startup_folder", icon: <FolderOpen className="size-4" /> },
  { key: "wmi", labelKey: "agents.persistence_wmi", icon: <Cog className="size-4" /> },
  { key: "service", labelKey: "agents.persistence_service", icon: <Server className="size-4" /> },
  { key: "image_file", labelKey: "agents.persistence_ifeo", icon: <Puzzle className="size-4" /> },
  { key: "com_hijack", labelKey: "agents.persistence_com_hijack", icon: <Boxes className="size-4" /> },
  { key: "dll_search_order", labelKey: "agents.persistence_dll_hijack", icon: <LinkIcon className="size-4" /> },
];

export default function AgentPersistencePage() {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const params = useParams();
  const id = params?.id as string;
  const [agent, setAgent] = useState<AgentDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [queuedMethods, setQueuedMethods] = useState<string[]>([]);
  // The per-method spinner names which action is in flight; the mutations own
  // error routing and the stale-completion guard.
  const [actionLoading, setActionLoading] = useState<string | null>(null);
  const [listOutput, setListOutput] = useState<string | null>(null);

  // t rotates on locale switch; the load callback keeps it in a ref so its
  // deps stay [id]: a locale switch must not reset the page to its spinner
  // and refetch. Named tRef so the i18n checker's ref-usage pattern sees the
  // lookups.
  const tRef = useRef(t);
  tRef.current = t;
  // A load for a previous agent id must not overwrite the agent the new id
  // started fetching when the route param changes mid-flight.
  const loadSeqRef = useRef(0);


  const loadAgent = useCallback(async () => {
    if (!id) return;
    const seq = ++loadSeqRef.current;
    try {
      const data = await api.get(paths.agents.one(id));
      if (seq !== loadSeqRef.current) return; // a newer load superseded this one
      setAgent(data.agent || data);
      setLoadError(null);
    } catch {
      if (seq !== loadSeqRef.current) return;
      // Distinguish "could not load" from "no such agent": a failed request used
      // to render the not-found page, which reads as a deleted implant. The
      // inline DataError + retry is the failure surface — a toast would only
      // repeat its text (the config/traffic page loads do not toast either).
      setLoadError(tRef.current("agents.persistence_load_failed"));
    } finally {
      if (seq === loadSeqRef.current) setLoading(false);
    }
  }, [id]);

  // The list run polls for up to 15 s; a monotonically increasing run id lets
  // a superseded run (id change, unmount, re-click) stop at its next check.
  // A shared boolean would have to be reset for the new run — un-cancelling
  // the old one, whose output then lands on the wrong agent's page.
  const listRunRef = useRef(0);

  const { mutate: runList, isPending: listLoading, reset: resetList } = useMutation({
    // `run` identifies this launch; every step re-checks it. The POST uses
    // the id captured when the button was clicked.
    fn: async (run: number) => {
      await api.post(paths.agents.persistence(id), { action: "list" });
      // `api.post` throws on a success:false envelope, so reaching here means
      // the task was queued; the failure path is the mutation's onError.
      if (listRunRef.current !== run) return;
      toast.success(t("agents.persistence_list_success"));
      for (let attempt = 0; attempt < 10; attempt++) {
        await new Promise((resolve) => setTimeout(resolve, 1500));
        if (listRunRef.current !== run) break;
        const tasks = await api.get<{ tasks?: Array<{ type: string; result: string }> }>(
          paths.agents.tasks(id),
        );
        if (listRunRef.current !== run) break;
        const latest = (tasks.tasks || []).find(
          (task) => task.type === "persistence_list" && task.result && task.result.trim(),
        );
        if (latest) {
          setListOutput(latest.result);
          break;
        }
      }
    },
    onError: (e) => toastError(e, "agents.persistence_load_failed"),
  });

  const { mutate: addMethod, reset: resetAdd } = useMutation({
    fn: (method: string) =>
      api.post<{ task_id?: number }>(paths.agents.persistence(id), { action: "add", method }),
    onSuccess: (data, method) => {
      setQueuedMethods((prev) => (prev.includes(method) ? prev : [...prev, method]));
      toast.success(t("agents.persistence_task_queued", { id: String(data.task_id ?? "") }));
    },
    onError: (e) => toastError(e, "agents.persistence_install_failed"),
  });

  const { mutate: removeMethod, reset: resetRemove } = useMutation({
    fn: (method: string) =>
      api.post<{ task_id?: number }>(paths.agents.persistence(id), { action: "remove", method }),
    onSuccess: (data, method) => {
      setQueuedMethods((prev) => prev.filter((m) => m !== method));
      toast.success(t("agents.persistence_task_queued", { id: String(data.task_id ?? "") }));
    },
    onError: (e) => toastError(e, "agents.persistence_remove_failed"),
  });

  // Route reuse: /beacons/A/persistence → /beacons/B/persistence keeps this
  // instance (the route carries no per-id key), so A's page state is dropped
  // and A's in-flight callbacks retired before B's fetch lands — B must not
  // render A's hostname, queued methods or detection output.
  useEffect(() => {
    setAgent(null);
    setLoading(true);
    setLoadError(null);
    setQueuedMethods([]);
    setListOutput(null);
    setActionLoading(null);
    resetList();
    resetAdd();
    resetRemove();
    void loadAgent();
  }, [loadAgent, resetList, resetAdd, resetRemove]);

  useEffect(() => {
    if (!id) return;
    const run = ++listRunRef.current;
    void runList(run);
    return () => {
      listRunRef.current += 1; // retire this run on unmount or id change
    };
  }, [id, runList]);

  const confirmDialog = useConfirm();

  const addPersistence = async (method: string) => {
    if (!id) return;
    if (implantBlocksDest(agent?.version, persistenceMethodQuality(method))) {
      toast.error(t("agents.version_unknown_dest")); // validation, not an API failure
      return;
    }
    const okInstall = await confirmDialog.confirm({ message: t("agents.persistence_install") + "?", danger: true });
    if (!okInstall) return;
    // Identity spinner local by design (see the conventions); the mutation
    // owns error routing and the stale-completion guard.
    setActionLoading(method);
    try {
      await addMethod(method);
    } finally {
      setActionLoading(null);
    }
  };

  const removePersistence = async (method: string) => {
    if (!id) return;
    const okRemove = await confirmDialog.confirm({ message: t("agents.persistence_remove") + "?", danger: true });
    if (!okRemove) return;
    setActionLoading(`remove_${method}`);
    try {
      await removeMethod(method);
    } finally {
      setActionLoading(null);
    }
  };

  const agentStatus = agent?.status || "offline";
  const statusColor = agentStatus === "online" ? "bg-success" : agentStatus === "stale" ? "bg-warning" : "bg-destructive";
  const hostname = agent?.hostname || t("agents.unknown");

  if (loading) {
    return <PageContainer><PageSpinner /></PageContainer>;
  }

  if (!agent) {
    return (
      <PageContainer>
        {loadError ? (
          <DataError message={loadError} onRetry={() => { setLoading(true); void loadAgent(); }} />
        ) : (
          <div className="text-center py-20">
            <Bug className="size-4" aria-hidden="true" />
            <h2 className="text-xl font-semibold text-foreground mb-2">{t("agents.persistence_not_found_title")}</h2>
            <p className="text-sm text-muted-foreground mb-6">{t("agents.persistence_not_found_desc")}</p>
            <Button render={<Link to="/beacons" />}>
                {t("agents.persistence_back_to_agents")}
            </Button>
          </div>
        )}
      </PageContainer>
    );
  }

  return (
    <PageContainer
      title={hostname}
      subtitle={t("nav.beacon_persistence")}
      icon={<LinkIcon className="size-5" aria-hidden="true" />}
      meta={
        <span className="inline-flex items-center gap-1.5">
          <span className={`size-2 rounded-full ${statusColor}`} aria-hidden="true" />
          {agentStatus.toUpperCase()}
        </span>
      }
      actions={
        <Button onClick={() => void runList(++listRunRef.current)} size="sm" className="gap-1.5">
          {listLoading ? <Spinner size="sm" /> : <RotateCw className="size-4" aria-hidden="true" />}
          {t("agents.persistence_refresh_list")}
        </Button>
      }
    >

      <Banner tone="warning" className="items-start mb-4">
        <div className="font-semibold">{t("agents.persistence_honesty_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("agents.persistence_honesty_desc")}</div>
      </Banner>

      <div className="grid grid-cols-1 xl:grid-cols-2 gap-4">
        <SectionCard
          title={t("agents.persistence_install")}
          icon={Plug}
          tone="primary"
          headerAccent
        >
          <div className="grid grid-cols-1 sm:grid-cols-2 gap-2 p-(--card-spacing)">
            {PERSISTENCE_METHODS.map((method) => {
              const quality = persistenceMethodQuality(method.key) ?? "hardened";
              const versionBlocked = implantBlocksDest(agent.version, quality);
              return (
              <Button
                key={method.key}
                onClick={() => addPersistence(method.key)}
                disabled={actionLoading === method.key || versionBlocked}
                title={versionBlocked ? t("agents.version_unknown_dest") : undefined}
                variant="outline"
                size="sm"
                className="flex items-center gap-3 px-4 py-3 h-auto justify-start"
              >
                {actionLoading === method.key ? (
                  <Spinner size="sm" className="shrink-0" />
                ) : (
                  <span className="text-primary shrink-0">{method.icon}</span>
                )}
                <span className="text-left leading-tight">
                  {t(method.labelKey)}
                  <span className="block text-micro font-normal text-muted-foreground">
                    <PersistQualityMark quality={quality} />
                  </span>
                </span>
              </Button>
              );
            })}
          </div>
        </SectionCard>

        <SectionCard
          title={<>{t("agents.persistence_installed")} <span className="text-xs font-normal text-muted-foreground">(queued)</span></>}
          icon={ListChecks}
          tone="info"
          count={queuedMethods.length}
          headerAccent
        >
          <div className="p-(--card-spacing)">
          {queuedMethods.length === 0 ? (
            <div className="text-center py-8">
              <Info className="size-4 mb-2" />
              <p className="text-xs text-muted-foreground">{t("agents.persistence_no_methods")}</p>
              <p className="text-micro-sm text-muted-foreground mt-1">{t("persistence.queued_hint")}</p>
            </div>
          ) : (
            <div className="space-y-2 max-h-[400px] overflow-y-auto">
              {queuedMethods.map((method) => {
                const info = PERSISTENCE_METHODS.find((m) => m.key === method);
                const isLoading = actionLoading === `remove_${method}`;
                return (
                  <div
                    key={method}
                    className="flex items-center justify-between px-4 py-3 bg-card border border-border rounded-lg"
                  >
                    <div className="flex items-center gap-3">
                      {info?.icon || <Cog className="size-4 text-primary" />}
                      <span className="text-sm font-medium text-foreground">{info?.labelKey ? t(info.labelKey) : method}</span>
                    </div>
                    <Button
                      onClick={() => removePersistence(method)}
                      disabled={isLoading}
                      variant="destructive"
                      size="sm"
                      className="flex items-center gap-1"
                    >
                      {isLoading ? (
                        <Spinner size="xs" color="white" />
                      ) : (
                        <Trash2 className="size-4" />
                      )}
                      {t("agents.persistence_remove")}
                    </Button>
                  </div>
                );
              })}
            </div>
          )}
        {listOutput ? (
            <details className="mt-3">
              <summary className="text-xs font-medium text-muted-foreground cursor-pointer">
                {t("agents.persistence_last_detection")}
              </summary>
              <pre className="mt-2 max-h-60 overflow-auto font-mono text-xs whitespace-pre-wrap bg-muted/40 rounded-lg p-3">
                {listOutput}
              </pre>
            </details>
          ) : (
            listLoading && (
              <div className="mt-3 flex items-center gap-2 text-xs text-muted-foreground">
                <Spinner size="sm" />
                {t("agents.persistence_detecting")}
              </div>
            )
          )}
          </div>
        </SectionCard>
      </div>
      {confirmDialog.modal}
    </PageContainer>
  );
}
