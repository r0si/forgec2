
import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { useI18n } from "@/lib/i18n";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { PageSpinner, Spinner } from "@/components/ui/spinner";
import { Card } from "@/components/ui/card";
import { toast } from "sonner";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Textarea } from "@/components/ui/textarea";
import { Label } from "@/components/ui/label";
import { Select, SelectTrigger, SelectValue, SelectContent, SelectItem } from "@/components/ui/select";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { Badge } from "@/components/ui/badge";
import { AlertTriangle, BookOpen, Code, Eraser, History, Layers, Play, Save, Terminal, Trash2, X } from "lucide-react";

import type { Agent } from "@/types/agent";
import {
  extractRunHistory,
  extractSavedScripts,
  normalizeScriptOutput,
  scriptRunError,
  type SavedScript,
  type ScriptRun,
} from "./components/script-run";

interface ScriptTemplate {
  labelKey: string;
  descKey: string;
  code: string;
}

const defaultTemplates: ScriptTemplate[] = [
  {
    labelKey: "scripting.tpl_agent_enum",
    descKey: "scripting.tpl_agent_enum_desc",
    code: `// Enumerate every agent your permissions can see
var agents = listAgents();
log("Agents: " + agents.length);
for (var i = 0; i < agents.length; i++) {
  log(agents[i].hostname + " @ " + agents[i].ip + " [" + agents[i].status + "]");
}
"Found " + agents.length + " agents";`,
  },
  {
    labelKey: "scripting.tpl_cred_harvest",
    descKey: "scripting.tpl_cred_harvest_desc",
    code: `// Read the credential vault (requires credentials.read)
var creds = query("credentials", {});
log("Credentials: " + creds.length);
for (var i = 0; i < creds.length; i++) {
  log(creds[i].username + "@" + creds[i].domain);
}
"Found " + creds.length + " credentials";`,
  },
  {
    labelKey: "scripting.tpl_bulk_task",
    descKey: "scripting.tpl_bulk_task_desc",
    code: `// Queue a task on every visible agent (requires agents.write)
var agents = listAgents();
var queued = 0;
for (var i = 0; i < agents.length; i++) {
  sendTask(agents[i].id, "shell", "whoami /all");
  queued++;
}
"Queued " + queued + " tasks";`,
  },
  {
    labelKey: "scripting.tpl_sleep_check",
    descKey: "scripting.tpl_sleep_check_desc",
    code: `// Stay resident briefly, then report the fleet
sleep(5000);
var agents = listAgents();
"Still tracking " + agents.length + " agents after 5s";`,
  },
  {
    labelKey: "scripting.tpl_net_discovery",
    descKey: "scripting.tpl_net_discovery_desc",
    code: `// Probe port 445 from every visible agent
var agents = listAgents();
var queued = 0;
for (var i = 0; i < agents.length; i++) {
  sendTask(agents[i].id, "shell", "powershell -c Test-NetConnection -ComputerName 10.0.0.1 -Port 445");
  queued++;
}
"Discovery queued on " + queued + " agents";`,
  },
];

const starterCode = `// JavaScript sandbox. Context globals: agents, tasks, credentials
log("Script started");
var agents = listAgents();
"Tracking " + agents.length + " agents";`;

const apiRefs = [
  "agents / tasks / credentials",
  "log(...)",
  "listAgents()",
  "getAgent(agentId)",
  "sendTask(agentId, type, params)",
  "query('agents'|'tasks'|'credentials'|'count_agents', args)",
  "httpRequest(method, { url, headers, body })",
  "sleep(ms)",
  "on(event, fn) / off(event)",
];

export default function ScriptingPage() {
  const { t } = useI18n();
  const { role } = usePermissions();
  const { confirm, modal } = useConfirm();
  const canRun = role === "admin";
  const [selectedAgent, setSelectedAgent] = useState("");
  const [scriptId, setScriptId] = useState<string | null>(null);
  const [scriptName, setScriptName] = useState("");
  const [scriptCode, setScriptCode] = useState(starterCode);
  const [scriptOutput, setScriptOutput] = useState(t("scripting.waiting"));
  const [running, setRunning] = useState(false);
  const [showTemplates, setShowTemplates] = useState(false);

  const { data, loading, error, refresh: loadData } = useApiResource<{ agents: Agent[]; savedScripts: SavedScript[]; runHistory: ScriptRun[] }>({
    fetcher: async () => {
      // Let any source failure reject: swallowing them and returning fake
      // empty arrays replaced the last good snapshot with "no agents / no
      // scripts / no history" on every blip, defeating the hook's
      // retain-on-error behaviour.
      const [agentData, scriptData, historyData] = await Promise.all([
        fetchAgentListCached(),
        api.get<unknown>(paths.scripts.list),
        api.get<unknown>(paths.scripts.history),
      ]);
      return {
        agents: agentData,
        savedScripts: extractSavedScripts(scriptData),
        runHistory: extractRunHistory(historyData),
      };
    },
    errorMessage: t("scripting.toast.load_failed"),
  });
  const agents = data?.agents ?? [];
  const savedScripts = data?.savedScripts ?? [];
  const runHistory = data?.runHistory ?? [];

  const handleSaveScript = async () => {
    if (!scriptName.trim() || !scriptCode.trim()) return;
    try {
      const saved = await api.postJson<{ id?: string | number }>(paths.scripts.list, {
        ...(scriptId ? { id: scriptId } : {}),
        name: scriptName,
        code: scriptCode,
      });
      if (saved?.id !== undefined && saved?.id !== null) setScriptId(String(saved.id));
      loadData();
    } catch { toast.error(t("scripting.toast.save_failed")); }
  };

  const handleDeleteScript = async (target: SavedScript) => {
    const id = target.id;
    if (!id) return;
    const ok = await confirm({
      title: t("scripting.delete_title"),
      message: t("scripting.delete_message", { name: target.name || t("scripting.untitled") }),
      danger: true,
    });
    if (!ok) return;
    try {
      await api.del(paths.scripts.one(id));
      if (scriptId === id) setScriptId(null);
      loadData();
    } catch { toast.error(t("scripting.toast.delete_failed")); }
  };

  const handleLoadScript = (script: SavedScript) => {
    setScriptId(script.id ?? null);
    setScriptName(script.name || "");
    setScriptCode(script.code || "");
  };

  const handleRunScript = async () => {
    if (!canRun || !scriptCode.trim()) return;
    setRunning(true);
    setScriptOutput(t("scripting.executing"));
    try {
      const payload = await api.postJson<unknown>(paths.scripts.execute, {
        ...(scriptId ? { script_id: scriptId } : { code: scriptCode }),
        ...(selectedAgent ? { agent_id: selectedAgent } : {}),
        name: scriptName,
      });
      const failure = scriptRunError(payload);
      setScriptOutput(normalizeScriptOutput(payload, t("scripting.no_output")));
      if (failure) toast.error(t("scripting.exec_failed"));
    } catch {
      setScriptOutput(t("scripting.exec_failed"));
    }
    setRunning(false);
    loadData();
  };

  const handleApplyTemplate = (template: ScriptTemplate) => {
    setScriptId(null);
    setScriptName(t(template.labelKey));
    setScriptCode(template.code);
    setShowTemplates(false);
  };

  const lineNumbers = scriptCode.split("\n").length;

  if (loading) {
    return <PageContainer title={t("scripting.title")} subtitle={t("scripting.subtitle")}><PageSpinner /></PageContainer>;
  }

  return (
    <PageContainer title={t("scripting.title")} subtitle={t("scripting.subtitle")} actions={<>
        <Button onClick={() => setShowTemplates(!showTemplates)}
          className="bg-primary hover:bg-primary/90 text-primary-foreground">
          <BookOpen className="size-4" />
          <span className="hidden sm:inline">{t("scripting.templates")}</span>
        </Button>
      </>}>

      {showTemplates && (
        <Card className="mb-6 p-(--card-spacing)">
          <div className="flex items-center justify-between mb-4">
            <div className="flex items-center gap-2">
              <Layers className="size-4" />
              <span className="text-sm font-semibold text-foreground">{t("scripting.script_library")}</span>
            </div>
            <Button variant="ghost" size="sm" onClick={() => setShowTemplates(false)} className="text-muted-foreground hover:text-foreground" aria-label={t("common.close")}>
              <X className="size-4" />
            </Button>
          </div>
          <div className="grid grid-cols-1 sm:grid-cols-2 lg:grid-cols-3 gap-3">
            {defaultTemplates.map((tpl) => (
              <Button key={tpl.labelKey} variant="outline" onClick={() => handleApplyTemplate(tpl)}
                className="text-left p-4 h-auto bg-muted hover:border-chart-6 dark:hover:border-chart-6 transition-colors">
                <div className="text-sm font-medium text-foreground">{t(tpl.labelKey)}</div>
                <div className="text-xs text-muted-foreground mt-1">{t(tpl.descKey)}</div>
              </Button>
            ))}
          </div>
        </Card>
      )}

      <div className="flex flex-col gap-(--page-gap)">
        {error && !data && (
          // First-load failure: no snapshot to retain yet, so the "no scripts /
          // no history" empties below must not stand in for the failure.
          <div role="alert" className="flex flex-wrap items-center gap-2 border border-warning/30 bg-warning/10 px-3 py-2 rounded-lg text-xs text-warning-ink">
            <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
            <span className="min-w-0 flex-1">{t("scripting.data_unreadable", { message: error })}</span>
            <Button onClick={() => void loadData()} size="xs" variant="outline">{t("common.try_again")}</Button>
          </div>
        )}
        <Tabs defaultValue="editor">
        <TabsList>
          <TabsTrigger value="editor" className="gap-1.5">
            <Code className="size-4" />{t("scripting.editor")}
          </TabsTrigger>
          <TabsTrigger value="history" className="gap-1.5">
            <History className="size-4" />{t("scripting.run_history", { count: String(runHistory.length) })}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="editor">
          <div className="grid grid-cols-1 lg:grid-cols-4 gap-4">
            <div className="lg:col-span-1 space-y-4">
              <Card className="overflow-hidden">
                <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                  <div className="text-sm font-semibold text-foreground">{t("scripting.target_agent")}</div>
                </div>
                <div className="p-4">
                  <Label htmlFor="agent-select" className="sr-only">{t("scripting.select_agent")}</Label>
                  <Select value={selectedAgent} onValueChange={v => setSelectedAgent(v ?? "")}>
                    <SelectTrigger className="w-full"><SelectValue placeholder={t("scripting.select_agent_placeholder")} /></SelectTrigger>
                    <SelectContent>
                      <SelectItem value="">{t("scripting.select_agent_placeholder")}</SelectItem>
                      {agents.map(a => {
                        const aid = a.id || "";
                        const host = a.hostname || "";
                        const ip = a.ip || "";
                        return <SelectItem key={aid} value={aid}>{host} ({ip})</SelectItem>;
                      })}
                    </SelectContent>
                  </Select>
                  <p className="text-xs text-muted-foreground mt-2">{t("scripting.target_agent_hint")}</p>
                  {!canRun && (
                    <p className="text-xs text-destructive-ink mt-2">{t("scripting.admin_only")}</p>
                  )}
                </div>
              </Card>

              <Card className="overflow-hidden">
                <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                  <span className="text-sm font-semibold text-foreground">{t("scripting.saved_scripts")}</span>
                </div>
                <div className="max-h-60 overflow-y-auto">
                  {!error && savedScripts.length === 0 ? (
                    <div className="p-4 text-center text-muted-foreground text-sm">{t("scripting.no_scripts")}</div>
                  ) : (
                    savedScripts.map((s) => (
                      <div key={s.id} className="flex items-center justify-between px-4 py-2.5 border-b border-border hover:bg-muted transition-colors">
                        <div className="flex-1 min-w-0">
                          <Button variant="ghost" size="sm" onClick={() => handleLoadScript(s)} className="text-sm text-muted-foreground hover:text-primary truncate text-left justify-start w-full p-0">
                            {s.name || t("scripting.untitled")}
                          </Button>
                          {s.created_by && <p className="text-xs-sm text-muted-foreground">{t("groups.created_by")} {s.created_by}</p>}
                        </div>
                        <Button variant="ghost" size="sm" onClick={() => handleDeleteScript(s)} className="text-muted-foreground hover:text-destructive-ink ml-2 shrink-0" aria-label={t("scripting.delete_title")}>
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    ))
                  )}
                </div>
              </Card>

              <Card className="overflow-hidden">
                <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border">
                  <span className="text-sm font-semibold text-foreground">{t("scripting.api_reference")}</span>
                </div>
                <div className="p-3 text-xs font-mono text-info-ink space-y-1">
                  {apiRefs.map((ref) => (
                    // API signatures are long and unbounded ("query('agents'|
                    // 'tasks'|…)" measured 374px); let them wrap instead of
                    // overflowing the card at both 1280 and 390.
                    <div key={ref} className="py-0.5 break-words whitespace-pre-wrap">{ref}</div>
                  ))}
                </div>
              </Card>
            </div>

            <div className="lg:col-span-3">
              <Card className="overflow-hidden">
                <div className="px-4 py-3 sm:px-5 sm:py-3.5 border-b border-border flex flex-wrap items-center justify-between gap-2">
                  <div className="flex items-center gap-3">
                    <Input id="script-name" placeholder={t("scripting.script_name_ph")} value={scriptName} onChange={e => setScriptName(e.target.value)}
                      className="bg-transparent border-none text-sm font-semibold focus:outline-none w-48 h-auto p-0" />
                  </div>
                  <div className="flex items-center gap-2">
                    <Button variant="outline" size="sm" onClick={handleSaveScript} disabled={!scriptName.trim()}>
                      <Save className="size-4" />{t("scripting.save")}
                    </Button>
                    <Button size="sm" onClick={handleRunScript} disabled={running || !canRun || !scriptCode.trim()}
                      title={canRun ? undefined : t("scripting.admin_only")}>
                      {running ? <Spinner size="xs" className="mr-1" /> : <Play className="size-4" />}
                      {running ? t("scripting.running") : t("scripting.run")}
                    </Button>
                  </div>
                </div>
                <div className="flex">
                  <div className="bg-muted text-muted-foreground text-xs font-mono py-4 px-2 text-right select-none min-w-[3rem] overflow-hidden">
                    {Array.from({ length: lineNumbers }, (_, i) => (
                      <div key={i + 1} className="leading-5 h-5">{i + 1}</div>
                    ))}
                  </div>
                  <Textarea
                    value={scriptCode}
                    onChange={e => { setScriptCode(e.target.value); setScriptId(null); }}
                    className="flex-1 h-72 p-4 font-mono text-sm bg-background text-chart-1 resize-none focus:outline-none leading-5 border-none rounded-none"
                    placeholder={t("scripting.code_ph")}
                    spellCheck={false}
                  />
                </div>
                <div className="border-t border-border">
                  <div className="p-3 px-4 text-xs font-semibold text-muted-foreground flex items-center justify-between">
                    <span>
                      <Terminal className="size-4" />{t("scripting.output")}
                    </span>
                    <Button variant="ghost" size="sm" onClick={() => setScriptOutput(t("scripting.waiting"))} className="text-xs text-muted-foreground hover:text-foreground h-auto p-0">
                      <Eraser className="size-4" />{t("scripting.clear")}
                    </Button>
                  </div>
                  <pre className="p-4 text-xs font-mono text-chart-1 bg-background m-2 rounded-lg max-h-48 overflow-y-auto whitespace-pre-wrap border border-border">
                    {scriptOutput}
                  </pre>
                </div>
              </Card>
            </div>
          </div>
        </TabsContent>

        <TabsContent value="history">
          <Card className="overflow-hidden">
            {!error && runHistory.length === 0 ? (
              <div className="text-center py-16 sm:py-20">
                <EmptyState icon={History} title={t("scripting.no_history")} />
              </div>
            ) : (
              <div className="divide-y divide-border">
                {runHistory.map((run, i) => (
                  <div key={run.id || i} className="p-4">
                    <div className="flex items-center justify-between gap-3 mb-2">
                      <div className="flex items-center gap-3 min-w-0">
                        <span className="text-sm font-medium text-foreground truncate">{run.script_name || t("scripting.untitled")}</span>
                        {run.user && <span className="text-xs text-muted-foreground">{t("scripting.history_user", { user: run.user })}</span>}
                        {run.agent_id && <span className="text-xs text-muted-foreground font-mono truncate">{run.agent_id}</span>}
                      </div>
                      <div className="flex items-center gap-2 shrink-0">
                        <Badge variant={run.status === "failed" ? "destructive" : "success"} className="text-micro-sm px-2 py-0.5 rounded-full">
                          {run.status === "failed" ? t("scripting.status_failed") : t("scripting.status_success")}
                        </Badge>
                        <span className="text-xs text-muted-foreground">{run.created_at || ""}</span>
                      </div>
                    </div>
                    {run.error && (
                      <pre className="text-xs font-mono text-destructive-ink bg-background rounded-lg p-3 mt-2 max-h-32 overflow-y-auto whitespace-pre-wrap">{run.error}</pre>
                    )}
                  </div>
                ))}
              </div>
            )}
          </Card>
        </TabsContent>
      </Tabs>
      </div>
      {modal}
    </PageContainer>
  );
}
