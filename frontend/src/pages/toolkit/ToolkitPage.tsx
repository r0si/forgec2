import { registerFragment } from "@/lib/i18n";
import { enAgents } from "@/lib/i18n/fragments/en.agents";
registerFragment(enAgents);

import { useEffect, useState } from "react";
import { StatusBadge } from "@/components/ui/status-indicator";
import { PageContainer } from "@/components/ui/page-container";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { useI18n } from "@/lib/i18n";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { toast } from "sonner";
import { Skeleton } from "@/components/ui/skeleton";
import { DataError } from "@/components/ui/data-state";
import TemplatesPanel from "./components/TemplatesPanel";
import PasswordSprayPanel from "./components/PasswordSprayPanel";

interface ToolkitAgent {
  id?: string;
  hostname?: string;
  ip?: string;
  os?: string;
}

interface RecentTask {
  ID?: string;
  id?: string;
  AgentID?: string;
  agent_id?: string;
  Type?: string;
  type?: string;
  Command?: string;
  command?: string;
  Status?: string;
  status?: string;
  CreatedAt?: string;
  created_at?: string;
}

export default function ToolkitPage() {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const [selectedAgent, setSelectedAgent] = useState("");
  const [activeTab, setActiveTab] = useState<"commands" | "templates" | "spray">("commands");
  const [agentInfo, setAgentInfo] = useState<Record<string, unknown> | null>(null);
  const runAction = async (action: string, param = "") => {
    if (!selectedAgent) {
      toast.error(t("toolkit.toast.select_agent_first"));
      return;
    }
    try {
      const data = await api.post(paths.toolkit.action(selectedAgent), { action, param });
      if (data.success) {
        toast.success(t("toolkit.toast.action_dispatched", { action: action, task_id: String(data.task_id) }));
        loadData();
      } else {
        toast.error((data.error as string) || t("toolkit.toast.action_failed"));
      }
    } catch (e) {
      toastError(e, "toolkit.toast.action_failed");
    }
  };

  const loadData = () => {
    void refresh();
  };

  const { data, loading, error, refresh } = useApiResource<{ toolkitAgents: ToolkitAgent[]; recentTasks: RecentTask[] }>({
    fetcher: async () => {
      const [agentsList, tasksData] = await Promise.all([
        fetchAgentListCached(),
        api.get(paths.toolkit.results),
      ]);
      return {
        toolkitAgents: agentsList as ToolkitAgent[],
        recentTasks: (tasksData.tasks || tasksData.results || tasksData.data || []) as RecentTask[],
      };
    },
    toastThrottleMs: 0,
    errorMessage: t("toolkit.toast.load_failed"),
  });
  const toolkitAgents = data?.toolkitAgents ?? [];
  const recentTasks = data?.recentTasks ?? [];

  useEffect(() => {
    if (!selectedAgent) { setAgentInfo(null); return; }
    const controller = new AbortController();
    setAgentInfo(null);
    api.get<{ agent?: Record<string, unknown> }>(`/toolkit/agents/${selectedAgent}/info`, { signal: controller.signal })
      .then((d) => {
        if (!controller.signal.aborted) setAgentInfo(d.agent || null);
      })
      .catch(() => {
        if (!controller.signal.aborted) setAgentInfo(null);
      });
    return () => controller.abort();
  }, [selectedAgent]);

  const quickActions = [
    { label: "whoami", value: "whoami" },
    { label: "hostname", value: "hostname" },
    { label: "ipconfig", value: "ipconfig" },
    { label: "systeminfo", value: "systeminfo" },
    { label: "ps", value: "ps" },
    { label: "screenshot", value: "screenshot" },
    { label: "beacon_now", value: "beacon_now" },
    { label: "mimikatz", value: "mimikatz" },
    { label: "creds", value: "creds" },
    { label: "elevate", value: "elevate" },
  ];

  const categories = [
    { name: t("toolkit.cat_system_recon"), color: "cyan", commands: [
      { cmd: "whoami", desc: t("toolkit.desc_whoami") },
      { cmd: "hostname", desc: t("toolkit.desc_hostname") },
      { cmd: "ipconfig", desc: t("toolkit.desc_ipconfig") },
      { cmd: "systeminfo", desc: t("toolkit.desc_systeminfo") },
      { cmd: "env", desc: t("toolkit.desc_env") },
      { cmd: "uptime", desc: t("toolkit.desc_uptime") },
      { cmd: "session_recon", desc: t("toolkit.desc_session_recon") },
      { cmd: "file_hunt", desc: t("toolkit.desc_file_hunt") },
      { cmd: "usb_enum", desc: t("toolkit.desc_usb_enum") },
      { cmd: "browser_history", desc: t("toolkit.desc_browser_history") },
    ]},
    { name: t("toolkit.cat_process_service"), color: "emerald", commands: [
      { cmd: "ps", desc: t("toolkit.desc_ps") },
      { cmd: "tasklist", desc: t("toolkit.desc_tasklist") },
      { cmd: "services", desc: t("toolkit.desc_services") },
      { cmd: "schtasks", desc: t("toolkit.desc_schtasks") },
      { cmd: "drivers", desc: t("toolkit.desc_drivers") },
    ]},
    { name: t("toolkit.cat_network_recon"), color: "blue", commands: [
      { cmd: "netstat", desc: t("toolkit.desc_netstat") },
      { cmd: "netstat -an", desc: t("toolkit.desc_netstat_all") },
      { cmd: "arp -a", desc: t("toolkit.desc_arp") },
      { cmd: "route print", desc: t("toolkit.desc_route") },
      { cmd: "net user", desc: t("toolkit.desc_net_user") },
      { cmd: "net localgroup", desc: t("toolkit.desc_net_localgroup") },
      { cmd: "av", desc: t("toolkit.desc_av") },
      { cmd: "tun_start", desc: t("toolkit.desc_tun_start") },
      { cmd: "tun_stop", desc: t("toolkit.desc_tun_stop") },
    ]},
    { name: t("toolkit.cat_credential_access"), color: "rose", commands: [
      { cmd: "mimikatz", desc: t("toolkit.desc_mimikatz") },
      { cmd: "creds_dump", desc: t("toolkit.desc_creds_dump") },
      { cmd: "browser_steal", desc: t("toolkit.desc_browser_steal") },
      { cmd: "cookie_export", desc: t("toolkit.desc_cookie_export") },
      { cmd: "entra_prt", desc: t("toolkit.desc_entra_prt") },
      { cmd: "sccm_recon", desc: t("toolkit.desc_sccm_recon") },
      { cmd: "vpn_creds", desc: t("toolkit.desc_vpn_creds") },
      { cmd: "wifi_creds", desc: t("toolkit.desc_wifi_creds") },
      { cmd: "kerberoast", desc: t("toolkit.desc_kerberoast") },
      { cmd: "cloud_steal", desc: t("toolkit.desc_cloud_steal") },
    ]},
    { name: t("toolkit.cat_privesc_bypass"), color: "amber", commands: [
      { cmd: "privesc_check", desc: t("toolkit.desc_privesc_check") },
      { cmd: "elevate", desc: t("toolkit.desc_elevate") },
      { cmd: "uac_bypass", desc: t("toolkit.desc_uac_bypass") },
      { cmd: "amsi_bypass", desc: t("toolkit.desc_amsi_bypass") },
      { cmd: "etw_bypass", desc: t("toolkit.desc_etw_bypass") },
    ]},
    { name: t("toolkit.cat_screen_monitor"), color: "purple", commands: [
      { cmd: "screenshot", desc: t("toolkit.desc_screenshot") },
      { cmd: "keylogger_start", desc: t("toolkit.desc_keylogger_start") },
      { cmd: "keylogger_stop", desc: t("toolkit.desc_keylogger_stop") },
      { cmd: "keylogger_dump", desc: t("toolkit.desc_keylogger_dump") },
      { cmd: "clipboard_get", desc: t("toolkit.desc_clipboard_get") },
      { cmd: "screen_trigger_start", desc: t("toolkit.desc_screen_trigger_start") },
      { cmd: "screen_trigger_stop", desc: t("toolkit.desc_screen_trigger_stop") },
    ]},
    { name: t("toolkit.cat_lateral_persist"), color: "teal", commands: [
      { cmd: "lateral", desc: t("toolkit.desc_lateral") },
      { cmd: "persistence", desc: t("toolkit.desc_persistence") },
      { cmd: "usb_drop", desc: t("toolkit.desc_usb_drop") },
    ]},
    // Absorbed from the former standalone Container page: the same four
    // one-shot agent commands, dispatched through the same runAction path.
    { name: t("toolkit.cat_container"), color: "orange", commands: [
      { cmd: "container_detect", desc: t("toolkit.desc_container_detect") },
      { cmd: "container_escape", desc: t("toolkit.desc_container_escape") },
      { cmd: "container_docker", desc: t("toolkit.desc_container_docker") },
      { cmd: "container_k8s", desc: t("toolkit.desc_container_k8s") },
    ]},
  ];

  const colorMap: Record<string, string> = {
    cyan: "bg-chart-2/10 text-chart-2 border-chart-2/30",
    emerald: "bg-success/10 text-chart-1 border-success/30",
    blue: "bg-info/10 text-info-ink border-info/30",
    rose: "bg-chart-5/10 text-chart-5 border-chart-5/30",
    amber: "bg-warning/10 text-warning-ink border-warning/30",
    purple: "bg-chart-6/purple text-chart-6 border-chart-6/purple",
    teal: "bg-chart-2/10 text-chart-2 border-chart-2/30",
    orange: "bg-warning/10 text-warning-ink border-warning/30",
  };

  return (
    <PageContainer title={t("toolkit.title")} subtitle={t("toolkit.subtitle")} actions={<>
        <div className="flex items-center gap-2">
          <span className="text-sm text-muted-foreground">{t("toolkit.target_agent")}</span>
          <Select value={selectedAgent} onValueChange={v => setSelectedAgent(v ?? "")}>
            <SelectTrigger className="w-full"><SelectValue placeholder={t("toolkit.select_agent_placeholder")} /></SelectTrigger>
            <SelectContent>
              <SelectItem value="">{t("toolkit.select_agent_placeholder")}</SelectItem>
              {toolkitAgents.map((a) => (
                <SelectItem key={a.id} value={a.id || ""}>
                  {(a.hostname || "unknown")} ({a.ip || "-"})
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
        </div>
      </>}>

      <Tabs value={activeTab} onValueChange={(v) => setActiveTab(v === "templates" ? "templates" : v === "spray" ? "spray" : "commands")}>
        <TabsList>
          <TabsTrigger value="commands">{t("toolkit.tab_commands")}</TabsTrigger>
          <TabsTrigger value="templates">{t("toolkit.tab_templates")}</TabsTrigger>
          <TabsTrigger value="spray">{t("toolkit.tab_spray")}</TabsTrigger>
        </TabsList>

        <TabsContent value="commands">
      <div className="grid grid-cols-1 xl:grid-cols-4 gap-4">
        <div className="xl:col-span-3 flex flex-col gap-(--page-gap)">
          <Card className="p-(--card-spacing)">
            <div className="flex items-center gap-2 mb-3">
              <span className="text-warning-ink text-sm font-bold">!</span>
              <span className="text-xs font-semibold text-muted-foreground">{t("toolkit.quick_actions")}</span>
            </div>
            <div className="flex flex-wrap gap-2">
              {quickActions.map((qa) => (
                <Button key={qa.value} variant="outline" size="sm" onClick={() => runAction(qa.value)} className="rounded-lg text-xs hover:border-primary/30 hover:text-primary transition-all">
                  {qa.label}
                </Button>
              ))}
            </div>
          </Card>

          {loading ? (
            <div className="flex flex-col gap-(--page-gap)">
              {[1,2,3].map(i => <Skeleton key={i} className="h-32 rounded-lg" />)}
            </div>
          ) : (
            categories.map((cat) => (
              <Card key={cat.name} className="overflow-hidden">
                <div className="flex items-center justify-between px-5 py-3.5 cursor-pointer hover:bg-muted transition-colors">
                  <div className="flex items-center gap-3">
                    <div className={`size-8 rounded-lg flex items-center justify-center border ${colorMap[cat.color]}`}>
                      <span className="text-xs font-bold">{cat.name[0]}</span>
                    </div>
                    <span className="font-semibold text-sm text-foreground">{cat.name}</span>
                    <Badge variant="secondary" className="text-micro-sm px-2 py-0.5">{cat.commands.length}</Badge>
                  </div>
                </div>
                <div className="px-5 pb-4 space-y-1">
                  {cat.commands.map((c) => (
                    <Button key={c.cmd} variant="ghost" onClick={() => runAction(c.cmd)} className="w-full flex items-center gap-3 px-3 py-2 rounded-lg justify-start text-left border border-transparent hover:border-border transition-all">
                      {/* Task-type ids are unbounded in length
                          (`screen_trigger_start` clipped at a fixed w-28),
                          so the label column sizes to its content and the
                          description takes the remaining width instead. */}
                      <span className={`shrink-0 whitespace-nowrap font-mono text-xs font-medium ${colorMap[cat.color]?.split(" ")[1] || "text-info-ink"}`}>{c.cmd}</span>
                      <span className="min-w-0 flex-1 truncate text-xs text-muted-foreground">{c.desc}</span>
                      <span className="ml-auto shrink-0 text-micro-sm text-muted-foreground">{t("toolkit.run")}</span>
                    </Button>
                  ))}
                </div>
              </Card>
            ))
          )}
        </div>

        <div className="flex flex-col gap-(--page-gap)">
          {agentInfo && (
            <Card className="p-(--card-spacing) text-xs space-y-1">
              <div className="font-semibold text-muted-foreground mb-2">{t("toolkit.agent_info")}</div>
              <div>Host: {String(agentInfo.hostname || "-")}</div>
              <div>IP: {String(agentInfo.ip || "-")}</div>
              <div>Integrity: {String(agentInfo.integrity || "-")}</div>
              <div>Interval: {String(agentInfo.current_interval ?? "-")}s</div>
            </Card>
          )}
          <Card className="overflow-hidden">
            <CardHeaderRow accent={false} title={t("toolkit.recent_results")} action={<Badge variant="secondary" className="text-micro-sm px-1.5 py-0.5">{error ? "—" : recentTasks.length}</Badge>} />
            <div className="max-h-[600px] overflow-y-auto">
              {loading ? (
                <div className="space-y-2 p-(--card-spacing)">
                  {[1, 2, 3, 4].map((i) => (
                    <Skeleton key={i} className="h-10 w-full" />
                  ))}
                </div>
              ) : error ? (
                <DataError message={t("toolkit.results_unreadable", { message: error })} onRetry={refresh} className="py-10" />
              ) : recentTasks.length === 0 ? (
                <div className="p-(--card-spacing) text-center text-muted-foreground text-xs">{t("toolkit.no_results")}</div>
              ) : (
                recentTasks.map((t, i) => (
                  <div key={i} className="px-4 py-3 border-b border-border last:border-0 hover:bg-muted transition-colors">
                    <div className="flex items-center justify-between mb-1">
                      <span className="text-micro-sm font-mono text-muted-foreground">{(t.agent_id || "").toString().slice(0, 8)}</span>
                      <StatusBadge status={t.status || ""} />
                    </div>
                    <div className="text-xs font-medium text-muted-foreground truncate">{t.type}: {t.command}</div>
                  </div>
                ))
              )}
            </div>
          </Card>
        </div>
      </div>
        </TabsContent>

        <TabsContent value="templates">
          <TemplatesPanel
            selectedAgent={selectedAgent}
            onRun={(command) => {
              if (!selectedAgent) {
                toast.error(t("toolkit.toast.select_agent_first"));
                return;
              }
              void (async () => {
                try {
                  await api.postJson(paths.agents.command(selectedAgent), { command });
                  toast.success(t("templates.toast.run_dispatched", { name: command.slice(0, 40) }));
                  loadData();
                } catch (e) {
                  toastError(e, "templates.toast.run_failed");
                }
              })();
            }}
          />
        </TabsContent>

        <TabsContent value="spray">
          {!selectedAgent ? (
            <Card className="p-(--card-spacing) text-center text-muted-foreground text-xs">{t("toolkit.toast.select_agent_first")}</Card>
          ) : (
            <PasswordSprayPanel selectedAgent={selectedAgent} />
          )}
        </TabsContent>
      </Tabs>
    </PageContainer>
  );
}


