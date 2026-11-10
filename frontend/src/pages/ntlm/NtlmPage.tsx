
import { registerFragment } from "@/lib/i18n";
import { enNtlm } from "@/lib/i18n/fragments/en.ntlm";
registerFragment(enNtlm);

import { useState } from "react";
import { Spinner } from "@/components/ui/spinner";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";
import { PageContainer } from "@/components/ui/page-container";
import { IconBadge } from "@/components/ui/icon-badge";
import { ErrorState } from "@/components/ui/error-state";
import { Button } from "@/components/ui/button";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { useI18n } from "@/lib/i18n";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useMutation } from "@/lib/hooks/useMutation";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { POLL } from "@/lib/polling";
import { toast } from "sonner";
import { Card } from "@/components/ui/card";
import { Banner } from "@/components/ui/banner";
import { StatusDot } from "@/components/ui/status-dot";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { ArrowLeftRight, BarChart2, Database, Megaphone, Play, RefreshCw, Square, TowerControl, Zap } from "lucide-react";

import type { Agent } from "@/types/agent";

type TabType = "coerce" | "relay" | "status";

export default function NtlmPage() {
  const { t } = useI18n();
  const [selectedAgent, setSelectedAgent] = useState("");
  const [target, setTarget] = useState("");
  const [listenAddr, setListenAddr] = useState("0.0.0.0:8080");
  const [coerceType, setCoerceType] = useState("printerbug");
  const [relayListener, setRelayListener] = useState("0.0.0.0:8445");
  const [relayTarget, setRelayTarget] = useState("");
  const [relayFlags, setRelayFlags] = useState("");
  const [activeTab, setActiveTab] = useState<TabType>("coerce");

  const { data: agentsData, error: agentsError, setError: setAgentsError } = useApiResource<{ agents?: Agent[] }>({
    fetcher: async () => {
      const agents = await fetchAgentListCached();
      return { agents };
    },
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("ntlm.toast.load_agents_failed"),
  });
  // `agentsData?.agents || agentsData` falls back to the whole wrapper object,
  // which is truthy and not an array — `.map`/`.filter` downstream then throws.
  const agents = agentsData?.agents ?? [];

  const { data: relayData, loading: relayLoading, error: relayError, refresh: loadRelayStatus } = useApiResource<{ active: Record<string, unknown>[]; count: number; running: boolean }>({
    fetcher: async () => {
      const data = await api.get<{ active: Record<string, unknown>[]; count: number; running: boolean }>("/ntlm/relay_status");
      return data;
    },
    enabled: activeTab === "status",
    toastThrottleMs: POLL.toastThrottle,
    errorMessage: t("ntlm.toast.relay_status_failed"),
  });
  const relayStatus = relayData ?? { active: [], count: 0, running: false };
  const getAgentId = (a: Agent) => a.id || "";
  const getHostname = (a: Agent) => a.hostname || "";
  const getIP = (a: Agent) => a.ip || "";

  const { toastError } = useErrorToast();

  // All three NTLM actions share one pending indicator: an in-flight request
  // disables every action button (the old shared bool's job), so a second
  // request cannot start while a first is out. Validation stays direct.
  const coerce = useMutation<[], Record<string, unknown>>({
    fn: () => {
      const body = new URLSearchParams({ target, listen_addr: listenAddr });
      return api.post(paths.agents.coerce(selectedAgent, coerceType), Object.fromEntries(body));
    },
    onSuccess: (data) => {
      if (data.success) toast.success(t("ntlm.toast.coerce_dispatched", { task_id: String(data.task_id) }));
      else toast.error((data.error as string) || t("ntlm.toast.coerce_failed"));
    },
    onError: (e) => toastError(e, "ntlm.toast.coerce_failed"),
  });

  const relayStart = useMutation<[], Record<string, unknown>>({
    fn: () => {
      const body = new URLSearchParams({ target: relayTarget, listener: relayListener, flags: relayFlags });
      return api.post(paths.agents.relayStart(selectedAgent), Object.fromEntries(body));
    },
    onSuccess: (data) => {
      if (data.success) toast.success(t("ntlm.toast.relay_started", { task_id: String(data.task_id) }));
      else toast.error((data.error as string) || t("ntlm.toast.start_failed"));
    },
    onError: (e) => toastError(e, "ntlm.toast.start_failed"),
  });

  const relayStop = useMutation<[], Record<string, unknown>>({
    fn: () => api.post(paths.agents.relayStop(selectedAgent), {}),
    onSuccess: (data) => {
      if (data.success) toast.success(t("ntlm.toast.relay_stopped"));
      else toast.error((data.error as string) || t("ntlm.toast.stop_failed"));
    },
    onError: (e) => toastError(e, "ntlm.toast.stop_failed"),
  });

  const loading = coerce.isPending || relayStart.isPending || relayStop.isPending;

  const handleCoerce = () => {
    if (!selectedAgent || !target) {
      toast.error(t("ntlm.toast.select_agent_target"));
      return;
    }
    void coerce.mutate();
  };

  const handleRelayStart = () => {
    if (!selectedAgent || !relayTarget) {
      toast.error(t("ntlm.toast.select_agent_relay"));
      return;
    }
    void relayStart.mutate();
  };

  const handleRelayStop = () => {
    if (!selectedAgent) {
      toast.error(t("ntlm.toast.select_agent_first"));
      return;
    }
    void relayStop.mutate();
  };

  const tabs: { key: TabType; label: string; icon: React.ReactNode }[] = [
    { key: "coerce", label: t("ntlm.tab_coerce"), icon: <Megaphone className="size-4" /> },
    { key: "relay", label: t("ntlm.tab_relay"), icon: <ArrowLeftRight className="size-4" /> },
    { key: "status", label: t("ntlm.tab_status"), icon: <BarChart2 className="size-4" /> },
  ];

  return (
    <PageContainer title={t("ntlm.title")} icon={<Zap className="size-4" />} subtitle={t("ntlm.subtitle")}>

      <Banner tone="warning" className="items-start">
        <div className="font-semibold">{t("ntlm.experimental_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("ntlm.experimental_desc")}</div>
      </Banner>

      {agentsError && (
        <ErrorState message={agentsError} className="mb-4" action={<Button variant="ghost" size="sm" onClick={() => setAgentsError(null)} aria-label={t("common.dismiss")}>&times;</Button>} />
      )}

      {/* Tabs */}
      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v as TabType)}>
        <TabsList sticky>
          {tabs.map((tab) => (
            <TabsTrigger key={tab.key} value={tab.key} className="gap-2">
              {tab.icon}
              {tab.label}
            </TabsTrigger>
          ))}
        </TabsList>

      {/* Agent selector (common) */}
      <Card className="p-(--card-spacing)">
        <Label className="mb-1.5">{t("ntlm.target_agent")}</Label>
        <Select value={selectedAgent || "none"} onValueChange={(v) => setSelectedAgent(v === "none" ? "" : v ?? "")}>
          <SelectTrigger className="max-w-md">
            <SelectValue>
              {(value) => (value === "none" ? t("ntlm.select_agent_placeholder") : String(value ?? ""))}
            </SelectValue>
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="none">{t("ntlm.select_agent_placeholder")}</SelectItem>
            {agents.map(a => {
              const id = getAgentId(a);
              return <SelectItem key={id} value={id}>{getHostname(a)} ({getIP(a)})</SelectItem>;
            })}
          </SelectContent>
        </Select>
      </Card>

      {/* Coercion Tab */}
      <TabsContent value="coerce">
<Card className="p-(--card-spacing)">
          <div className="flex items-center gap-x-3 mb-5">
            <IconBadge icon={Megaphone} color="warning" size="lg" />
            <div>
              <div className="text-sm font-semibold text-foreground">{t("ntlm.coerce_title")}</div>
              <div className="text-xs text-muted-foreground">{t("ntlm.coerce_desc")}</div>
            </div>
          </div>

          <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-4">
            <div>
              <Label className="mb-1.5">{t("ntlm.coerce_technique")}</Label>
              <Select value={coerceType} onValueChange={(v) => { if (v) setCoerceType(v); }}>
                <SelectTrigger>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="printerbug">PrinterBug (MS-RPRN)</SelectItem>
                  <SelectItem value="petitpotam">PetitPotam (MS-EFSR)</SelectItem>
                  <SelectItem value="dfs">DFSCoerce (MS-DFSNM)</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div>
              <Label className="mb-1.5">{t("ntlm.target_ip")}</Label>
              <Input
                value={target}
                onChange={(e) => setTarget(e.target.value)}
                placeholder={t("ntlm.target_ph")}
              />
            </div>
            <div>
              <Label className="mb-1.5">{t("ntlm.callback_listener")}</Label>
              <Input
                value={listenAddr}
                onChange={(e) => setListenAddr(e.target.value)}
                placeholder="0.0.0.0:8080"
              />
            </div>
          </div>

          <Button
            onClick={handleCoerce}
            disabled={loading || !selectedAgent || !target}
            className="disabled:opacity-50"
          >
            {loading ? <><Spinner size="sm" className="mr-2" />{t("ntlm.dispatching")}</> : <><Play className="size-4" />{t("ntlm.execute_coercion")}</>}
          </Button>
        </Card>
      </TabsContent>

      {/* Relay Tab */}
      <TabsContent value="relay">
        <div className="flex flex-col gap-(--page-gap)">
<Card className="p-(--card-spacing)">
            <div className="flex items-center gap-x-3 mb-5">
<IconBadge icon={TowerControl} color="primary" size="lg" />
              <div>
                <div className="text-sm font-semibold text-foreground">{t("ntlm.relay_title")}</div>
                <div className="text-xs text-muted-foreground">{t("ntlm.relay_desc")}</div>
              </div>
            </div>

            <div className="grid grid-cols-1 sm:grid-cols-3 gap-4 mb-4">
              <div>
                <Label className="mb-1.5">{t("ntlm.relay_target")}</Label>
                <Input
                  value={relayTarget}
                  onChange={(e) => setRelayTarget(e.target.value)}
                  placeholder={t("ntlm.relay_target_ph")}
                />
              </div>
              <div>
                <Label className="mb-1.5">{t("ntlm.relay_listener")}</Label>
                <Input
                  value={relayListener}
                  onChange={(e) => setRelayListener(e.target.value)}
                  placeholder="0.0.0.0:8445"
                />
              </div>
              <div>
                <Label className="mb-1.5">{t("ntlm.flags")}</Label>
                <Input
                  value={relayFlags}
                  onChange={(e) => setRelayFlags(e.target.value)}
                  placeholder={t("ntlm.args_ph")}
                />
              </div>
            </div>

            <div className="flex gap-3">
              <Button
                onClick={handleRelayStart}
                disabled={loading || !selectedAgent || !relayTarget}
                className="disabled:opacity-50"
              >
                {loading ? <><Spinner size="sm" className="mr-2" />{t("ntlm.starting")}</> : <><Play className="size-4" />{t("ntlm.start_relay")}</>}
              </Button>
              <Button
                variant="destructive"
                onClick={handleRelayStop}
                disabled={loading || !selectedAgent}
              >
                <Square className="size-4" />{t("ntlm.stop_relay")}
              </Button>
            </div>
          </Card>
        </div>
      </TabsContent>

      {/* Status Tab */}
      <TabsContent value="status">
<Card className="p-(--card-spacing)">
          <div className="flex items-center gap-x-3 mb-5">
            <IconBadge icon={BarChart2} color="success" size="lg" />
            <div>
              <div className="text-sm font-semibold text-foreground">{t("ntlm.relay_sessions")}</div>
              <div className="text-xs text-muted-foreground">{t("ntlm.relay_sessions_desc")}</div>
            </div>
          </div>

          <div className="mb-4 flex items-center gap-x-3">
            {relayLoading ? (
              <>
                <Spinner size="sm" />
                <span className="text-sm text-muted-foreground">{t("common.loading")}</span>
              </>
            ) : relayError ? (
              <>
                <StatusDot tone="destructive" size="md" />
                <span className="text-sm font-medium text-destructive-ink">{t("ntlm.relay_status_unknown")}</span>
              </>
            ) : (
              <>
                <StatusDot tone={relayStatus.running ? "success" : "muted"} size="md" pulse={relayStatus.running} />
                <span className="text-sm font-medium text-foreground">
                  {relayStatus.running ? `${relayStatus.count} ${t("ntlm.sessions_active")}` : t("ntlm.sessions_inactive")}
                </span>
              </>
            )}
              <Button variant="outline" size="sm" onClick={loadRelayStatus} className="ml-auto">
              <RefreshCw className="size-4 mr-1" />{t("ntlm.refresh")}
            </Button>
          </div>

          {relayError ? (
            <DataError message={relayError} onRetry={() => { void loadRelayStatus(); }} className="py-8" />
          ) : relayStatus.active.length === 0 ? (
            <EmptyState icon={Database} title={t("ntlm.no_sessions")} message={t("ntlm.no_sessions_hint")} />
          ) : (
            <Table>
              <TableHeader>
                <TableRow>
                  <TableHead>{t("ntlm.col_session_id")}</TableHead>
                  <TableHead>{t("ntlm.col_target")}</TableHead>
                  <TableHead>{t("ntlm.col_listener")}</TableHead>
                  <TableHead>{t("ntlm.col_started")}</TableHead>
                  <TableHead className="text-right">{t("ntlm.col_hashes")}</TableHead>
                </TableRow>
              </TableHeader>
              <TableBody>
                {relayStatus.active.map((session: Record<string, unknown>, idx: number) => (
                  <TableRow key={String(session.id || idx)}>
                    <TableCell className="font-mono text-xs">{String(session.id || "-")}</TableCell>
                    <TableCell>{String(session.target || "-")}</TableCell>
                    <TableCell className="font-mono text-xs">{String(session.listener || "-")}</TableCell>
                    <TableCell className="text-xs">{String(session.started_at || "-")}</TableCell>
                    <TableCell className="text-right font-mono">{String(session.hashes_captured ?? 0)}</TableCell>
                  </TableRow>
                ))}
              </TableBody>
            </Table>
          )}
        </Card>
      </TabsContent>
      </Tabs>
    </PageContainer>
  );
}


