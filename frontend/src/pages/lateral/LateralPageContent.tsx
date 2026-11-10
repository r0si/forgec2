
import { registerFragment } from "@/lib/i18n";
import { enLateral } from "@/lib/i18n/fragments/en.lateral";
registerFragment(enLateral);

import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useMutation } from "@/lib/hooks/useMutation";
import { toast } from "sonner";
import { PageContainer } from "@/components/ui/page-container";
import { PageSpinner, Spinner } from "@/components/ui/spinner";
import { useI18n } from "@/lib/i18n";
import { Card } from "@/components/ui/card";
import { Banner } from "@/components/ui/banner";
import { IconBadge } from "@/components/ui/icon-badge";
import { StatCard } from "@/components/ui/animated-stat-card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";
import { Bot, Code, Crosshair, FolderTree, GitBranch, History, IdCard, Inbox, Key, Lock, Network, Rocket, Route, Share2, Terminal, Ticket, Wallet, AlertTriangle } from "lucide-react";

interface LateralStats {
  online_agents?: number;
  total_creds?: number;
  total_tasks?: number;
}

interface Credential {
  id?: string;
  username?: string;
  domain?: string;
  target?: string;
}

import type { Agent } from "@/types/agent";

interface MovementHistory {
  id?: string;
  source?: string;
  target?: string;
  method?: string;
  status?: string;
  output?: string;
  created_at?: string;
  pivot?: string;
}

interface FormData {
  source: string;
  target: string;
  pivot: string;
  username: string;
  password: string;
  hash: string;
  port: string;
  share: string;
  namespace: string;
  command: string;
  key_path: string;
  credential: string;
}

const defaultForm: FormData = {
  source: "",
  target: "",
  pivot: "",
  username: "",
  password: "",
  hash: "",
  port: "",
  share: "ADMIN$",
  namespace: "root\\cimv2",
  command: "whoami /all",
  key_path: "",
  credential: "",
};

const methods = [
  { key: "smb", label: "SMB", icon: <Share2 className="size-4" /> },
  { key: "winrm", label: "WinRM", icon: <Terminal className="size-4" /> },
  { key: "wmi", label: "WMI", icon: <Network className="size-4" /> },
  { key: "ssh", label: "SSH", icon: <Key className="size-4" /> },
  { key: "pth", label: "Pass-the-Hash", icon: <Ticket className="size-4" /> },
];

export default function LateralPageContent() {
  const [activeMethod, setActiveMethod] = useState("smb");
  const [form, setForm] = useState<FormData>(defaultForm);
  const { t } = useI18n();

  const { mutate: runLateral, isPending: submitting } = useMutation({
    fn: async () => {
      const payload: Record<string, string> = {
        source: form.source,
        target: form.target,
        method: activeMethod,
      };
      // Port validation: the field is plain text and garbage ("abc", "-1",
      // "99999") used to be posted verbatim, failing only on the agent.
      const usesPort = activeMethod === "winrm" || activeMethod === "ssh";
      let port = "";
      if (usesPort) {
        port = form.port || (activeMethod === "winrm" ? "5985" : "22");
        if (!/^\d{1,5}$/.test(port) || Number(port) < 1 || Number(port) > 65535) {
          toast.error(t("lateral.toast.invalid_port"));
          throw new Error("invalid port");
        }
      }
      // The implant has no pivot slot, so the server rejects the field. Warn
      // instead of firing a request that is guaranteed to 400.
      if (form.pivot) {
        toast.error(t("lateral.pivot_unsupported"));
        throw new Error("pivot unsupported");
      }
      if (form.credential) payload.credential = form.credential;
      if (form.username) payload.username = form.username;
      if (form.command) payload.command = form.command;

      if (activeMethod === "smb") {
        if (form.password) payload.password = form.password;
        payload.share = form.share;
      } else if (activeMethod === "winrm") {
        if (form.password) payload.password = form.password;
        payload.port = port;
      } else if (activeMethod === "wmi") {
        if (form.password) payload.password = form.password;
        payload.namespace = form.namespace;
      } else if (activeMethod === "ssh") {
        if (form.password) payload.password = form.password;
        if (form.key_path) payload.key_path = form.key_path;
        payload.port = port;
      } else if (activeMethod === "pth") {
        if (form.hash) payload.hash = form.hash;
      }

      await api.postJson(paths.lateral.execute, payload);
    },
    onSuccess: () => loadData(),
    onError: () => toast.error(t("lateral.toast.execute_failed")),
  });

  const { data, loading, error, refresh: loadData } = useApiResource<{
    stats: LateralStats;
    agents: Agent[];
    credentials: Credential[];
    history: MovementHistory[];
    /** Per-source read failures: each empty list below must say why it is empty. */
    partialFailures: string[];
  }>({
    fetcher: async () => {
      const partialFailures: string[] = [];
      const [data, agentData, credData, histData] = await Promise.all([
        api.get(paths.lateral.historyAll).catch(() => { partialFailures.push(t("lateral.source_history")); return { lateral: [] }; }),
        api.get(paths.agents.list()).catch(() => { partialFailures.push(t("lateral.source_agents")); return { agents: [] }; }),
        api.get(paths.credentials.list()).catch(() => { partialFailures.push(t("lateral.source_credentials")); return { vault_entries: [] }; }),
        api.get(paths.tasks.list("type=lateral&pageSize=50")).catch(() => { partialFailures.push(t("lateral.source_tasks")); return { tasks: [] }; }),
      ]);
      if (partialFailures.length > 0) toast.error(t("lateral.toast.load_failed"));
      // The history endpoint returns {tasks,total}; the stat cards need
      // online_agents/total_creds/total_tasks, which we derive client-side
      // from the same payloads instead of expecting a dedicated stats shape.
      const agentList = ((agentData as { agents?: Agent[] }).agents || []) as Agent[];
      const credList = ((credData as { vault_entries?: Credential[] }).vault_entries || []) as Credential[];
      const taskList = ((histData as { tasks?: MovementHistory[] }).tasks || []) as MovementHistory[];
      return {
        stats: {
          online_agents: agentList.filter((a) => String((a as { status?: string }).status || "") === "online").length,
          total_creds: credList.length,
          total_tasks: Number((data as { total?: number }).total) || taskList.length,
        },
        agents: agentList,
        credentials: credList,
        history: taskList,
        partialFailures,
      };
    },
    toastThrottleMs: 0,
    errorMessage: t("lateral.toast.load_failed"),
  });
  const partialFailures = data?.partialFailures ?? [];
  const agentsFailed = partialFailures.includes(t("lateral.source_agents"));
  const credsFailed = partialFailures.includes(t("lateral.source_credentials"));
  const historyFailed = partialFailures.includes(t("lateral.source_tasks"));
  const stats = data?.stats ?? {};
  const agents = data?.agents ?? [];
  const credentials = data?.credentials ?? [];
  const history = data?.history ?? [];

  const updateForm = (key: keyof FormData, value: string) => {
    setForm(prev => ({ ...prev, [key]: value }));
  };

  const handleSubmit = async () => {
    if (!form.source || !form.target) return;
    await runLateral();
  };

  const getStatusBadge = (status: string) => {
    const s = status?.toLowerCase() ?? "";
    const variant = s === "success" || s === "completed" ? "success" : s === "failed" ? "destructive" : s === "running" ? "default" : "warning";
    return variant;
  };

  const renderMethodForm = () => {
    switch (activeMethod) {
      case "smb":
        return (
          <>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <FolderTree className="size-4" />{t("lateral.a11y_share")}
                </span>
                <Input aria-label={t("lateral.a11y_share")} name="admin-0" type="text" placeholder="ADMIN$" className="font-mono" value={form.share} onChange={e => updateForm("share", e.target.value)} />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <IdCard className="size-4" />{t("lateral.a11y_username")}
                </span>
                <Input aria-label={t("lateral.a11y_username")} name="domain-username-1" type="text" placeholder="DOMAIN\username" className="font-mono" value={form.username} onChange={e => updateForm("username", e.target.value)} />
              </div>
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Lock className="size-4" />{t("lateral.a11y_password")}
              </span>
              <Input aria-label={t("lateral.a11y_password")} name="password-2" type="password" placeholder={t("lateral.a11y_password")} className="font-mono" value={form.password} onChange={e => updateForm("password", e.target.value)} />
            </div>
          </>
        );
      case "winrm":
        return (
          <>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <Network className="size-4" />{t("lateral.a11y_port")}
                </span>
                <Input aria-label={t("lateral.a11y_port")} name="5985-3" type="text" placeholder="5985" className="font-mono" value={form.port} onChange={e => updateForm("port", e.target.value)} />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <IdCard className="size-4" />{t("lateral.a11y_username")}
                </span>
                <Input aria-label={t("lateral.a11y_username")} name="administrator-4" type="text" placeholder="Administrator" className="font-mono" value={form.username} onChange={e => updateForm("username", e.target.value)} />
              </div>
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Lock className="size-4" />{t("lateral.a11y_password")}
              </span>
              <Input aria-label={t("lateral.a11y_password")} name="password-5" type="password" placeholder={t("lateral.a11y_password")} className="font-mono" value={form.password} onChange={e => updateForm("password", e.target.value)} />
            </div>
          </>
        );
      case "wmi":
        return (
          <>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <Code className="size-4" />{t("lateral.a11y_namespace")}
                </span>
                <Input aria-label={t("lateral.a11y_namespace")} name="root-cimv2-6" type="text" placeholder="root\cimv2" className="font-mono" value={form.namespace} onChange={e => updateForm("namespace", e.target.value)} />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <IdCard className="size-4" />{t("lateral.a11y_username")}
                </span>
                <Input aria-label={t("lateral.a11y_username")} name="domain-username-7" type="text" placeholder="DOMAIN\username" className="font-mono" value={form.username} onChange={e => updateForm("username", e.target.value)} />
              </div>
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Lock className="size-4" />{t("lateral.a11y_password")}
              </span>
              <Input aria-label={t("lateral.a11y_password")} name="password-8" type="password" placeholder={t("lateral.a11y_password")} className="font-mono" value={form.password} onChange={e => updateForm("password", e.target.value)} />
            </div>
          </>
        );
      case "ssh":
        return (
          <>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <Network className="size-4" />{t("lateral.a11y_port")}
                </span>
                <Input aria-label={t("lateral.a11y_port")} name="22-9" type="text" placeholder="22" className="font-mono" value={form.port} onChange={e => updateForm("port", e.target.value)} />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <IdCard className="size-4" />{t("lateral.a11y_username")}
                </span>
                <Input aria-label={t("lateral.a11y_username")} name="root-10" type="text" placeholder="root" className="font-mono" value={form.username} onChange={e => updateForm("username", e.target.value)} />
              </div>
            </div>
            <div className="grid grid-cols-1 md:grid-cols-2 gap-4">
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <Lock className="size-4" />{t("lateral.a11y_password")}
                </span>
                <Input aria-label={t("lateral.a11y_password")} name="password-11" type="password" placeholder={t("lateral.a11y_password")} className="font-mono" value={form.password} onChange={e => updateForm("password", e.target.value)} />
              </div>
              <div>
                <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                  <Key className="size-4" />{t("lateral.a11y_key_path")}
                </span>
                <Input aria-label={t("lateral.a11y_key_path")} name="path-to-key-pem-12" type="text" placeholder="/path/to/key.pem" className="font-mono" value={form.key_path} onChange={e => updateForm("key_path", e.target.value)} />
              </div>
            </div>
          </>
        );
      case "pth":
        return (
          <>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <IdCard className="size-4" />{t("lateral.a11y_username")}
              </span>
              <Input aria-label={t("lateral.a11y_username")} name="administrator-13" type="text" placeholder="Administrator" className="font-mono" value={form.username} onChange={e => updateForm("username", e.target.value)} />
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Ticket className="size-4" />{t("lateral.a11y_hash")}
              </span>
              <Input aria-label={t("lateral.a11y_hash")} name="aad3b435b51404eeaad3b435b51404ee-14" type="text" placeholder="aad3b435b51404eeaad3b435b51404ee:..." className="font-mono" value={form.hash} onChange={e => updateForm("hash", e.target.value)} />
            </div>
          </>
        );
      default:
        return null;
    }
  };

  if (loading)
    return <PageContainer title={t("lateral.title")} subtitle={`SMB/WinRM/WMI/PsExec ${t("lateral.subtitle_exec")} / Pass-the-Hash`}><PageSpinner /></PageContainer>;

  return (
    <PageContainer title={t("lateral.title")} subtitle={`SMB/WinRM/WMI/PsExec ${t("lateral.subtitle_exec")} / Pass-the-Hash`}>

      <Banner tone="warning" className="items-start">
        <div className="font-semibold">{t("lateral.honesty_title")}</div>
        <div className="text-xs text-muted-foreground mt-0.5">{t("lateral.honesty_desc")}</div>
      </Banner>

      {(partialFailures.length > 0 || error) && (
        <div role="alert" className="flex flex-wrap items-center gap-3 rounded-xl border border-warning/30 bg-warning/10 px-4 py-3 text-xs text-warning-ink">
          <AlertTriangle className="size-4 shrink-0" aria-hidden="true" />
          <span className="min-w-0 flex-1">{t("lateral.partial_load_failed", { sources: partialFailures.join(", ") })}</span>
          <Button onClick={loadData} size="sm" variant="outline" className="min-h-11 px-4 sm:min-h-7 sm:px-2.5">{t("common.try_again")}</Button>
        </div>
      )}

      <div className="grid grid-cols-1 sm:grid-cols-3 gap-4">
        <StatCard label={t("lateral.online_implant")} value={agentsFailed ? "—" : stats.online_agents || 0} color="success" icon={<Bot className="size-4" />} sub={agentsFailed ? t("status.unknown") : t("lateral.pivot_available")} />
        <StatCard label={t("lateral.available_creds")} value={credsFailed ? "—" : stats.total_creds || 0} color="warning" icon={<Key className="size-4" />} sub={credsFailed ? t("status.unknown") : t("lateral.cred_vault")} />
        <StatCard label={t("lateral.history_tasks")} value={historyFailed ? "—" : stats.total_tasks || 0} color="primary" icon={<Network className="size-4" />} sub={historyFailed ? t("status.unknown") : t("lateral.lateral_records")} />
      </div>

      <Card className="px-4 shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="flex items-center gap-x-3 mb-5">
          <IconBadge icon={GitBranch} color="primary" size="xl" className="dark:bg-primary/15" />
          <div>
            <div className="text-sm font-semibold text-foreground">{t("lateral.new_task")}</div>
            <div className="text-xs text-muted-foreground">{t("lateral.select_target_method")}</div>
          </div>
        </div>

        <div className="mb-6">
          <div className="flex gap-1 overflow-x-auto pb-1">
            {methods.map((m) => (
              <Button key={m.key} onClick={() => setActiveMethod(m.key)} variant="outline"
                className={`flex items-center gap-2 px-4 py-2.5 rounded-lg text-sm font-medium whitespace-nowrap transition-colors ${
                  activeMethod === m.key
                    ? "bg-primary/10 text-primary border-primary/30 dark:bg-primary/20  dark:border-primary/40"
                    : "text-muted-foreground hover:text-foreground hover:bg-muted dark:hover:text-muted-foreground border-transparent"
                }`}>
                {m.icon}
                {m.label}
              </Button>
            ))}
          </div>
        </div>

        <div className="flex flex-col gap-(--page-gap)">
          <div className="grid grid-cols-1 md:grid-cols-3 gap-4">
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Bot className="size-4" />{t("lateral.source_implant")}
              </span>
              <Select value={form.source || undefined} onValueChange={v => updateForm("source", v ?? "")}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("lateral.select_online_implant")} />
                </SelectTrigger>
                <SelectContent>
                  {agents.map(a => {
                    const aid = a.id || "";
                    const host = a.hostname || "";
                    return <SelectItem key={aid} value={aid}>{host}</SelectItem>;
                  })}
                </SelectContent>
              </Select>
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Crosshair className="size-4" />{t("lateral.target_host")}
              </span>
              <Input aria-label={t("lateral.ip_or_hostname")} name="ip-16" type="text" placeholder={t("lateral.ip_or_hostname")} className="font-mono" value={form.target} onChange={e => updateForm("target", e.target.value)} />
            </div>
            <div>
              <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
                <Route className="size-4" />{t("lateral.pivot_agent")}
              </span>
              <Select value={form.pivot || undefined} onValueChange={v => updateForm("pivot", v ?? "")}>
                <SelectTrigger className="w-full">
                  <SelectValue placeholder={t("lateral.direct_connect")} />
                </SelectTrigger>
                <SelectContent>
                  {agents.map(a => {
                    const aid = a.id || "";
                    const host = a.hostname || "";
                    return <SelectItem key={aid} value={aid}>{t("lateral.via")} {host}</SelectItem>;
                  })}
                </SelectContent>
              </Select>
              <p className="mt-1.5 text-micro-sm text-warning-ink">
                {t("lateral.pivot_unsupported")}
              </p>
            </div>
          </div>

          <div>
            <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
              <Wallet className="size-4" />{t("lateral.credential")}            </span>
            <Select value={form.credential || undefined} onValueChange={v => updateForm("credential", v ?? "")}>
              <SelectTrigger className="w-full">
                <SelectValue placeholder={t("lateral.manual_cred")} />
              </SelectTrigger>
              <SelectContent>
                {credentials.map(c => {
                  const cid = c.id || "";
                  const cuser = c.username || "";
                  const cdomain = c.domain || "";
                  const ctarget = c.target || "";
                  return <SelectItem key={cid} value={cid}>{cdomain ? `${cdomain}\\` : ""}{cuser} ({ctarget})</SelectItem>;
                })}
              </SelectContent>
            </Select>
          </div>

          <div className="border-t border-border pt-4">
            <div className="text-xs font-semibold text-muted-foreground mb-3">
              {(() => { const m = methods.find(m => m.key === activeMethod); return m ? <span className="mr-1.5">{m.icon}</span> : null; })()}
              {methods.find(m => m.key === activeMethod)?.label} {t("lateral.config")}
            </div>
            {renderMethodForm()}
          </div>

          <div>
            <span className="block text-xs font-semibold text-muted-foreground mb-1.5">
              <Terminal className="size-4" />{t("lateral.exec_command")}
            </span>
            <Input aria-label={t("lateral.a11y_command")} name="whoami-all-powershell-enc-19" type="text" placeholder="whoami /all | powershell -enc ..." className="font-mono" value={form.command} onChange={e => updateForm("command", e.target.value)} />
          </div>

          <div className="flex gap-3 pt-4 border-t border-border">
            <Button onClick={handleSubmit} size="lg" disabled={submitting || !form.source || !form.target}
              className="flex-1 disabled:opacity-50 disabled:cursor-not-allowed">
              {submitting ? <Spinner size="xs" /> : <Rocket className="size-4" />}
              <span>{submitting ? t("lateral.executing") : t("lateral.execute_lateral")}</span>
            </Button>
          </div>
        </div>
      </Card>

      <Card className="overflow-hidden shadow-sm hover:shadow-md transition-shadow duration-200">
        <div className="flex items-center justify-between p-5 border-b border-border">
          <div className="flex items-center gap-3">
            <div className="size-8 bg-primary/10 rounded-lg flex items-center justify-center">
              <History className="size-4" />
            </div>
            <span className="text-sm font-semibold text-foreground">{t("lateral.move_history")}</span>
          </div>
          <span className="text-xs text-muted-foreground">{historyFailed ? t("status.unknown") : `${history.length} ${t("lateral.records")}`}</span>
        </div>
        {historyFailed ? (
          <div className="p-(--card-spacing)"><DataError message={t("lateral.history_unreadable")} onRetry={loadData} /></div>
        ) : history.length > 0 ? (
          <Table className="text-sm">
            <TableHeader className="bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 sticky top-0 z-10 border-b border-border">
              <TableRow className="hover:bg-transparent">
                <TableHead className="font-semibold"></TableHead>
                <TableHead className="font-semibold">{t("lateral.target")}</TableHead>
                <TableHead className="font-semibold">{t("lateral.method")}</TableHead>
                <TableHead className="font-semibold">{t("lateral.pivot")}</TableHead>
                <TableHead className="font-semibold">{t("lateral.status")}</TableHead>
                <TableHead className="font-semibold">{t("lateral.time")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {history.map((h, i) => (
                <TableRow key={h.id || i}>
                  <TableCell className="font-mono text-muted-foreground">{h.source || "-"}</TableCell>
                  <TableCell className="font-mono text-muted-foreground">{h.target || "-"}</TableCell>
                  <TableCell>
                    <Badge variant="outline" className="text-micro-sm">{h.method || "-"}</Badge>
                  </TableCell>
                  <TableCell className="text-muted-foreground text-xs">{h.pivot || t("lateral.direct")}</TableCell>
                  <TableCell>
                    <Badge variant={getStatusBadge(h.status || "")}>{h.status || "-"}</Badge>
                  </TableCell>
                  <TableCell className="text-xs text-muted-foreground">{h.created_at || "-"}</TableCell>
                </TableRow>
              ))}
            </TableBody>
          </Table>
        ) : (
          <EmptyState icon={Inbox} title={t("lateral.empty")} />
        )}
      </Card>
    </PageContainer>
  );
}

