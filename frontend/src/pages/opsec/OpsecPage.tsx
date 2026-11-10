
import { registerFragment } from "@/lib/i18n";
import { enOpsec } from "@/lib/i18n/fragments/en.opsec";
registerFragment(enOpsec);

import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { Banner } from "@/components/ui/banner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { EmptyState } from "@/components/ui/empty-state";
import { PageContainer } from "@/components/ui/page-container";
import { StatTile } from "@/components/ui/stat-tile";
import { DataError } from "@/components/ui/data-state";
import { PageSpinner } from "@/components/ui/spinner";
import { toast } from "sonner";
import { formatTime, cn } from "@/lib/utils";
import { Card, CardContent } from "@/components/ui/card";
import { StatusDot } from "@/components/ui/status-dot";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Checkbox } from "@/components/ui/checkbox";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { CheckCircle, CircleAlert, History, Key, Network, Pencil, Plus, RefreshCw, ShieldCheck, Skull, Syringe, Terminal, Trash2, Users } from "lucide-react";

interface OpsecRule {
  name: string;
  description: string;
  risk_level: number;
  default_action: number;
  enabled?: boolean;
}

interface OpsecHistoryItem {
  id: number;
  agent_id: string;
  task_type: string;
  rule_name: string;
  allowed: boolean;
  message: string;
  risk_level: number;
  username: string;
  hostname: string;
  created_at: string;
}

interface TestResult {
  allowed: boolean;
  blocked: boolean;
  messages: string;
  results?: { allowed: boolean; rule_name: string; message: string; risk_level: number; action_taken: number }[];
}

interface RekeyEntry {
  agent_id: string;
  rekey_count: number;
  last_rekey_at?: string;
  message_count: number;
  last_used: string;
}

interface RekeyStats {
  active_sessions: number;
  total_rekeys: number;
  rekeys_by_agent?: RekeyEntry[];
}

/** `failedSources` names the reads that were rejected, so each card can say
 *  which of its numbers is unknown instead of showing a substituted zero. */
interface OpsecPageData {
  rules: OpsecRule[];
  history: OpsecHistoryItem[];
  rekey: RekeyStats;
  failedSources: string[];
}

export default function OpsecPage() {
  const { t } = useI18n();
  const [testResult, setTestResult] = useState<TestResult | null>(null);
  const [showRuleModal, setShowRuleModal] = useState(false);
  const [editingRule, setEditingRule] = useState<OpsecRule | null>(null);
  const { confirm, modal } = useConfirm();
  const [ruleForm, setRuleForm] = useState({
    name: "",
    description: "",
    risk_level: 2,
    default_action: 1,
    enabled: true,
  });
  const [testForm, setTestForm] = useState({
    agent_id: "test-agent",
    task_type: "mimikatz",
    username: "Administrator",
    hostname: "DC-01",
    ip: "10.0.1.5",
    domain: "corp.local",
    is_da: false,
    processes: "explorer.exe, svchost.exe",
  });

  const riskLabels: Record<number, { label: string; cls: string }> = {
    1: { label: t("opsec.risk_low"), cls: "bg-secondary text-muted-foreground" },
    2: { label: t("opsec.risk_medium"), cls: "bg-warning/15 dark:bg-chart-4/20 text-warning-ink" },
    3: { label: t("opsec.risk_high"), cls: "bg-warning/15 text-warning-ink" },
    4: { label: t("opsec.risk_critical"), cls: "bg-destructive/15 text-destructive-ink" },
  };

  const actionLabels: Record<number, { label: string; cls: string }> = {
    0: { label: t("opsec.action_block"), cls: "bg-destructive/15 text-destructive-ink" },
    1: { label: t("opsec.action_warn"), cls: "bg-warning/15 text-warning-ink" },
    2: { label: t("opsec.action_bypass"), cls: "bg-success/15 text-success-ink" },
  };

  const testTypes = [
    { id: "mimikatz", label: "Mimikatz", icon: <Skull className="size-4" />, danger: true },
    { id: "creds", label: t("opsec.tool_creds"), icon: <Key className="size-4" />, danger: false },
    { id: "inject", label: t("opsec.tool_inject"), icon: <Syringe className="size-4" />, danger: false },
    { id: "shell", label: t("opsec.tool_shell"), icon: <Terminal className="size-4" />, danger: false },
    { id: "ldap_users", label: t("opsec.tool_ldap_users"), icon: <Users className="size-4" />, danger: false },
    { id: "portscan", label: t("opsec.tool_portscan"), icon: <Network className="size-4" />, danger: false },
  ];

  const { data, loading, refresh: loadData } = useApiResource<OpsecPageData>({
    fetcher: async () => {
      // Settle independently: a single Promise.all with per-source catch used
      // to swap the failed source for a zero, so an unreadable rekey source
      // rendered as "0 active sessions" and the reassuring "crypto sessions
      // are stable" empty state below it.
      const [rulesRes, histRes, rekeyRes] = await Promise.allSettled([
        api.get<{ rules: OpsecRule[] }>(paths.opsec.rulesApi),
        api.get<{ history?: OpsecHistoryItem[] }>(paths.opsec.history),
        api.get<RekeyStats>(paths.opsec.rekey),
      ]);
      const failedSources: string[] = [];
      if (rulesRes.status === "rejected") failedSources.push(t("opsec.source_rules"));
      if (histRes.status === "rejected") failedSources.push(t("opsec.source_history"));
      if (rekeyRes.status === "rejected") failedSources.push(t("opsec.source_rekey"));
      if (failedSources.length > 0) toast.error(t("opsec.toast.load_failed"));
      return {
        rules: rulesRes.status === "fulfilled" ? (rulesRes.value.rules || []) : [],
        history: histRes.status === "fulfilled" ? (histRes.value.history || []) : [],
        rekey: rekeyRes.status === "fulfilled"
          ? (rekeyRes.value || { active_sessions: 0, total_rekeys: 0 })
          : { active_sessions: 0, total_rekeys: 0 },
        failedSources,
      };
    },
    toastThrottleMs: 0,
    errorMessage: t("opsec.toast.load_failed"),
  });
  const rules = data?.rules ?? [];
  const history = data?.history ?? [];
  const rekeyStats = data?.rekey ?? { active_sessions: 0, total_rekeys: 0 };
  const rulesUnread = (data?.failedSources ?? []).includes(t("opsec.source_rules"));
  const historyUnread = (data?.failedSources ?? []).includes(t("opsec.source_history"));
  const rekeyUnread = (data?.failedSources ?? []).includes(t("opsec.source_rekey"));

  const handleRunTest = async (taskType: string) => {
    try {
      const processes = testForm.processes.split(",").map(s => s.trim()).filter(Boolean);
      const data = await api.postJson<TestResult>(paths.opsec.check, {
        agent_id: testForm.agent_id,
        task_type: taskType,
        username: testForm.username,
        hostname: testForm.hostname,
        ip: testForm.ip,
        domain: testForm.domain,
        is_da: testForm.is_da,
        processes,
      });
      setTestResult(data);
    } catch {
      setTestResult({ allowed: false, blocked: true, messages: t("opsec.test_failed") });
    }
  };

  const handleSaveRule = async () => {
    try {
      await api.postJson(paths.opsec.rules, ruleForm);
      setShowRuleModal(false);
      setEditingRule(null);
      setRuleForm({ name: "", description: "", risk_level: 1, default_action: 1, enabled: true });
      toast.success(t("opsec.save_rule"));
      loadData();
    } catch {
      toast.error(t("opsec.toast.save_failed"));
    }
  };

  const handleDeleteRule = async (name: string) => {
    if (!(await confirm({ message: t("opsec.delete") + ` "${name}"?` }))) return;
    try {
      await api.del(paths.opsec.rule(name));
      toast.success(t("opsec.toast.deleted"));
      loadData();
    } catch { toast.error(t("opsec.toast.delete_failed")); }
  };

  const openEditRule = (rule: OpsecRule) => {
    setEditingRule(rule);
    setRuleForm({
      name: rule.name,
      description: rule.description,
      risk_level: rule.risk_level,
      default_action: rule.default_action,
      enabled: rule.enabled !== undefined ? rule.enabled : true,
    });
    setShowRuleModal(true);
  };

  if (loading) return <PageContainer title={t("opsec.title")} subtitle={t("opsec.subtitle")}><PageSpinner /></PageContainer>;

  return (
    <>
      <PageContainer title={t("opsec.title")} subtitle={t("opsec.subtitle")} actions={<>
        <div className="flex items-center gap-2">
          <span className={cn("size-2 rounded-full animate-pulse", rulesUnread ? "bg-warning" : "bg-success")}></span>
          <span className={cn("text-xs font-medium", rulesUnread ? "text-warning-ink" : "text-success-ink")}>
            {rulesUnread ? "?" : rules.length} {t("opsec.active_rules")}
          </span>
        </div>
      </>}>

        <Card>
          <CardContent>
            <div className="flex items-center justify-between mb-4">
              <span className="text-sm font-semibold text-foreground">{t("opsec.col_actions")}</span>
              <Button onClick={() => { setEditingRule(null); setRuleForm({ name: "", description: "", risk_level: 1, default_action: 1, enabled: true }); setShowRuleModal(true); }}>
                <Plus className="size-4" />{t("opsec.new_rule")}
              </Button>
            </div>
            <Table className="w-full text-sm">
            <TableHeader>
              <TableRow className="text-left text-xs text-muted-foreground border-b border-border">
                <TableHead className="px-4 py-3 font-medium">{t("opsec.col_name")}</TableHead>
                <TableHead className="max-sm:hidden px-4 py-3 font-medium">{t("opsec.col_desc")}</TableHead>
                <TableHead className="px-4 py-3 font-medium">{t("opsec.col_risk")}</TableHead>
                <TableHead className="px-4 py-3 font-medium">{t("opsec.col_action")}</TableHead>
                <TableHead className="px-4 py-3 font-medium text-right">{t("opsec.col_actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody>
              {rulesUnread ? (
              <TableRow>
                <TableCell colSpan={5}><DataError message={t("opsec.rules_unreadable")} onRetry={loadData} /></TableCell>
              </TableRow>
              ) : rules.length === 0 ? (
              <TableRow>
                <TableCell colSpan={5}><EmptyState icon={ShieldCheck} title={t("opsec.empty")} message={t("opsec.empty_desc")} /></TableCell>
              </TableRow>
              ) : rules.map((rule) => {
                const risk = riskLabels[rule.risk_level] || riskLabels[1];
                const action = actionLabels[rule.default_action] || actionLabels[2];
                return (
                  <TableRow key={rule.name} className="border-b border-border hover:bg-muted transition-colors">
                    <TableCell className="px-4 py-3">
                      <code className="font-semibold text-foreground">{rule.name}</code>
                    </TableCell>
                    <TableCell className="max-sm:hidden px-4 py-3 text-muted-foreground">{rule.description}</TableCell>
                    <TableCell className="px-4 py-3">
                      <span className={cn("inline-flex px-2 py-0.5 rounded-lg text-xs font-medium", risk.cls)}>{risk.label}</span>
                    </TableCell>
                    <TableCell className="px-4 py-3">
                      <span className={cn("inline-flex px-2 py-0.5 rounded-lg text-xs font-medium", action.cls)}>{action.label}</span>
                    </TableCell>
                    <TableCell className="px-4 py-3 text-right">
                      <div className="flex items-center justify-end gap-1">
                        <Button onClick={() => openEditRule(rule)} variant="ghost" size="icon" title={t("opsec.dialog_edit")} aria-label={t("opsec.dialog_edit")}>
                          <Pencil className="size-4" />
                        </Button>
                        <Button onClick={() => handleDeleteRule(rule.name)} variant="ghost" size="icon" className="text-destructive-ink" title={t("opsec.delete")} aria-label={t("opsec.delete")}>
                          <Trash2 className="size-4" />
                        </Button>
                      </div>
                    </TableCell>
                  </TableRow>
                );
              })}
            </TableBody>
          </Table>
          </CardContent>
        </Card>

        <Card className="p-(--card-spacing)">
          <h2 className="text-sm font-semibold text-foreground mb-4">{t("opsec.quick_test")}</h2>
          <p className="text-xs text-muted-foreground mb-4">{t("opsec.test_desc")}</p>
          <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-4">
            <div>
              <Label htmlFor="opsec-field-agent">{t("opsec.field_agent")}</Label>
              <Input id="opsec-field-agent" name="input-0" className="w-full mt-1" value={testForm.agent_id} onChange={(e) => setTestForm({ ...testForm, agent_id: e.target.value })} />
            </div>
            <div>
              <Label htmlFor="opsec-field-user">{t("opsec.field_user")}</Label>
              <Input id="opsec-field-user" name="input-1" className="w-full mt-1" value={testForm.username} onChange={(e) => setTestForm({ ...testForm, username: e.target.value })} />
            </div>
            <div>
              <Label htmlFor="opsec-field-hostname">{t("opsec.field_hostname")}</Label>
              <Input id="opsec-field-hostname" name="input-2" className="w-full mt-1" value={testForm.hostname} onChange={(e) => setTestForm({ ...testForm, hostname: e.target.value })} />
            </div>
            <div>
              <Label htmlFor="opsec-field-domain">{t("opsec.field_domain")}</Label>
              <Input id="opsec-field-domain" name="input-3" className="w-full mt-1" value={testForm.domain} onChange={(e) => setTestForm({ ...testForm, domain: e.target.value })} />
            </div>
            <div>
              <Label htmlFor="opsec-field-ip">{t("opsec.field_ip")}</Label>
              <Input id="opsec-field-ip" name="input-4" className="w-full mt-1" value={testForm.ip} onChange={(e) => setTestForm({ ...testForm, ip: e.target.value })} />
            </div>
            <div>
              <Label htmlFor="opsec-field-procs">{t("opsec.field_procs")}</Label>
              <Input id="opsec-field-procs" name="input-5" className="w-full mt-1" value={testForm.processes} onChange={(e) => setTestForm({ ...testForm, processes: e.target.value })} placeholder="explorer.exe, svchost.exe" />
            </div>
          </div>
          <div className="flex items-center gap-3 mb-4">
            <div className="flex items-center gap-2">
              <Checkbox aria-label={t("opsec.domain_admin")} id="is-da" checked={testForm.is_da} onCheckedChange={(checked) => setTestForm({ ...testForm, is_da: checked === true })} />
              <Label htmlFor="is-da" >{t("opsec.domain_admin")}</Label>
            </div>
          </div>
          <div className="flex flex-wrap gap-2">
            {testTypes.map((tt) => (
              <Button
                key={tt.id}
                onClick={() => handleRunTest(tt.id)}
                variant={tt.danger ? "destructive" : "secondary"}
                className="rounded-lg"
              >
                {tt.icon}
                {tt.label}
              </Button>
            ))}
          </div>
          {testResult && (
            <Banner tone={testResult.allowed ? "success" : "destructive"} icon={testResult.allowed ? <CheckCircle className="size-4" /> : <CircleAlert className="size-4" />} className="mt-4">
              <div className="flex items-center gap-2 mb-2">
                <span className="font-semibold">{testResult.allowed ? t("opsec.allowed") : t("opsec.blocked")}</span>
              </div>
              <p className="text-xs text-muted-foreground mb-2">{testResult.messages || t("opsec.no_rule")}</p>
              {testResult.results && testResult.results.length > 0 && (
                <div className="space-y-1">
                  {testResult.results.map((r) => (
                    <div key={r.rule_name} className="flex items-center gap-2 text-xs">
                       <StatusDot tone={r.allowed ? "success" : "destructive"} size="sm" />
                      <code className="text-muted-foreground">{r.rule_name}</code>
                      <span className="opacity-60">- {r.message}</span>
                    </div>
                  ))}
                </div>
              )}
            </Banner>
          )}
        </Card>

        <Card className="p-(--card-spacing)">
          <h2 className="text-sm font-semibold text-foreground mb-4">
            <RefreshCw className="size-4" />
            {t("opsec.rekey_title")}
          </h2>
          <div className="grid grid-cols-1 sm:grid-cols-3 gap-3 mb-4">
            <div className="p-3 bg-muted border border-border rounded-lg">
              <StatTile label={t("opsec.rekey_active_sessions")} value={rekeyUnread ? "?" : rekeyStats.active_sessions} />
            </div>
            <div className="p-3 bg-muted border border-border rounded-lg">
              <StatTile label={t("opsec.rekey_total")} value={rekeyUnread ? "?" : rekeyStats.total_rekeys} />
            </div>
            <div className="p-3 bg-muted border border-border rounded-lg">
              <StatTile label={t("opsec.rekey_agents")} value={rekeyUnread ? "?" : rekeyStats.rekeys_by_agent?.length ?? 0} />
            </div>
          </div>
          {rekeyUnread ? (
            <DataError message={t("opsec.rekey_unreadable")} onRetry={loadData} />
          ) : rekeyStats.rekeys_by_agent && rekeyStats.rekeys_by_agent.length > 0 ? (
            <div className="space-y-2 max-h-48 overflow-y-auto">
              {rekeyStats.rekeys_by_agent.map((entry) => (
                <div key={entry.agent_id} className="flex items-center justify-between gap-3 p-3 bg-muted border border-border rounded-lg text-xs">
                  <div className="min-w-0">
                    <code className="font-semibold text-foreground break-all">{entry.agent_id}</code>
                    <p className="text-muted-foreground mt-0.5">
                      {t("opsec.rekey_count")}: {entry.rekey_count} · {t("opsec.rekey_messages")}: {entry.message_count}
                    </p>
                  </div>
                  <span className="text-muted-foreground shrink-0">
                    {entry.last_rekey_at ? formatTime(entry.last_rekey_at) : "—"}
                  </span>
                </div>
              ))}
            </div>
          ) : (
            <EmptyState icon={RefreshCw} title={t("opsec.rekey_empty")} message={t("opsec.rekey_empty_desc")} />
          )}
        </Card>

        <Card className="p-(--card-spacing)">
          <h2 className="text-sm font-semibold text-foreground mb-4">
            <History className="size-4" />
            {t("opsec.history")}
          </h2>
          {historyUnread ? (
            <DataError message={t("opsec.history_unreadable")} onRetry={loadData} />
          ) : history.length === 0 ? (
            <EmptyState icon={History} title={t("opsec.history_empty")} />
          ) : (
            <div className="space-y-2 max-h-64 overflow-y-auto">
              {history.map((h) => (
                <div key={h.id} className="flex items-start gap-3 p-3 bg-muted border border-border rounded-lg">
                  <StatusDot tone={h.allowed ? "success" : "destructive"} size="sm" className="mt-1 shrink-0" />
                  <div className="min-w-0 flex-1">
                    <div className="flex items-center gap-2 text-xs">
                      <code className="font-semibold text-foreground">{h.rule_name}</code>
                      <span className={cn(
                        "px-1.5 py-0.5 rounded text-micro-sm font-medium",
                        h.risk_level >= 4 ? "bg-destructive/15 text-destructive-ink" :
                        h.risk_level >= 3 ? "bg-warning/15 text-warning-ink dark:bg-warning/20 dark:text-warning-ink" :
                        "bg-warning/15 text-warning-ink"
                      )}>
                        L{h.risk_level}
                      </span>
                      <span className="text-muted-foreground">{h.hostname}{"\\"}{h.username}</span>
                      <span className="text-muted-foreground">task: {h.task_type}</span>
                    </div>
                    <p className="text-xs text-muted-foreground mt-0.5">{h.message}</p>
                    <p className="text-micro-sm text-muted-foreground mt-0.5">{formatTime(h.created_at)}</p>
                  </div>
                </div>
              ))}
             </div>
          )}
        </Card>
      </PageContainer>

      <Dialog open={showRuleModal} onOpenChange={setShowRuleModal}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{editingRule ? t("opsec.dialog_edit") : t("opsec.dialog_create")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div>
              <Label htmlFor="opsec-field-rule-name">{t("opsec.field_rule_name")}</Label>
              <Input id="opsec-field-rule-name" name="input-7" className="w-full mt-1" value={ruleForm.name} onChange={(e) => setRuleForm({ ...ruleForm, name: e.target.value })} placeholder="e.g. block_mimikatz_da" />
            </div>
            <div>
              <Label htmlFor="opsec-col-desc">{t("opsec.col_desc")}</Label>
              <Input id="opsec-col-desc" name="input-8" className="w-full mt-1" value={ruleForm.description} onChange={(e) => setRuleForm({ ...ruleForm, description: e.target.value })} placeholder="e.g. Block mimikatz when Domain Admin" />
            </div>
            <div>
              <Label htmlFor="opsec-field-risk">{t("opsec.field_risk")}</Label>
              <Select id="opsec-field-risk"
                value={String(ruleForm.risk_level)}
                onValueChange={(v) => v !== null && setRuleForm({ ...ruleForm, risk_level: parseInt(v) })}
              >
                <SelectTrigger className="w-full mt-1" aria-label={t("opsec.field_risk")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="1">{t("opsec.risk_low")}</SelectItem>
                  <SelectItem value="2">{t("opsec.risk_medium")}</SelectItem>
                  <SelectItem value="3">{t("opsec.risk_high")}</SelectItem>
                  <SelectItem value="4">{t("opsec.risk_critical")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div>
              <Label htmlFor="opsec-field-default-action">{t("opsec.field_default_action")}</Label>
              <Select id="opsec-field-default-action"
                value={String(ruleForm.default_action)}
                onValueChange={(v) => v !== null && setRuleForm({ ...ruleForm, default_action: parseInt(v) })}
              >
                <SelectTrigger className="w-full mt-1" aria-label={t("opsec.field_default_action")}>
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="0">{t("opsec.action_block")}</SelectItem>
                  <SelectItem value="1">{t("opsec.action_warn")}</SelectItem>
                  <SelectItem value="2">{t("opsec.action_bypass")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex items-center gap-2">
              <Checkbox aria-label={t("opsec.field_enabled")} id="rule-enabled" checked={ruleForm.enabled} onCheckedChange={(checked) => setRuleForm({ ...ruleForm, enabled: checked === true })} />
              <Label htmlFor="rule-enabled" >{t("opsec.field_enabled")}</Label>
            </div>
          </div>
          <DialogFooter className="gap-2">
            <Button variant="ghost" onClick={() => setShowRuleModal(false)}>{t("opsec.cancel")}</Button>
            <Button onClick={handleSaveRule} disabled={!ruleForm.name}>{t("opsec.save_rule")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {modal}
    </>
  );
}
