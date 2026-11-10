
import { registerFragment } from "@/lib/i18n";
import { enAuto } from "@/lib/i18n/fragments/en.auto";
import { enWorkflows } from "@/lib/i18n/fragments/en.workflows";
registerFragment(enAuto);
registerFragment(enWorkflows);

import { useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useI18n } from "@/lib/i18n";
import { Tooltip, TooltipContent, TooltipTrigger } from "@/components/ui/tooltip";
import { PageContainer } from "@/components/ui/page-container";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { EmptyState } from "@/components/ui/empty-state";
import { StatusBadge } from "@/components/ui/status-indicator";
import { PageSpinner } from "@/components/ui/spinner";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { DataError } from "@/components/ui/data-state";
import { toast } from "sonner";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import { Tabs, TabsList, TabsTrigger, TabsContent } from "@/components/ui/tabs";
import { AlertTriangle, Bell, CalendarClock, FlaskConical, Globe, Link, Plus, Trash2, Workflow, Zap } from "lucide-react";
import { defaultWebhookParams, type AlertRule } from "./components/types";
import { useAutomationData } from "./components/useAutomationData";
import { RuleDialog } from "./components/RuleDialog";
import { WebhookDialog } from "./components/WebhookDialog";
import { AlertRuleDialog } from "./components/AlertRuleDialog";
import { ScheduledRulesCard } from "./components/ScheduledRulesCard";
import { OneShotTasksCard } from "./components/OneShotTasksCard";
import { WorkflowsTab } from "./components/WorkflowsTab";

const TAB_KEYS = ["rules", "scheduled", "workflows", "webhooks", "alerts"] as const;
type TabKey = (typeof TAB_KEYS)[number];

function initialTab(): TabKey {
  if (typeof window !== "undefined") {
    const m = window.location.hash.match(/^#tab=(\w+)$/);
    if (m && (TAB_KEYS as readonly string[]).includes(m[1])) return m[1] as TabKey;
  }
  return "rules";
}

export default function AutomationPage() {
  const { t } = useI18n();
  const {
    rules,
    webhooks,
    alertRules,
    alerts,
    loading, error, loadData,
  } = useAutomationData();
  const [tab, setTab] = useState<TabKey>(initialTab);
  const [showAlertRuleModal, setShowAlertRuleModal] = useState(false);
  const [alertRuleForm, setAlertRuleForm] = useState({ name: "", type: "agent_offline", threshold: 300, description: "" });
  const [showRuleModal, setShowRuleModal] = useState(false);
  const [showWebhookModal, setShowWebhookModal] = useState(false);
  const [sendingTest, setSendingTest] = useState(false);
  const { confirm, modal } = useConfirm();

  const eventRules = rules.filter((r) => (r.event_type || r.EventType) !== "schedule");
  const [ruleForm, setRuleForm] = useState({
    name: "",
    event_type: "agent.checkin",
    action_type: "command",
    action_config: "",
    webhook: { ...defaultWebhookParams },
    macro_id: null as number | null,
    macro_stop_on_error: true,
  });
  const [webhookForm, setWebhookForm] = useState({ name: "", url: "", event_type: "agent.checkin", method: "POST" });

  const onTabChange = (v: string) => {
    setTab(v as TabKey);
    if (typeof window !== "undefined") {
      window.history.replaceState(null, "", `#tab=${v}`);
    }
  };

  const buildAction = () => {
    if (ruleForm.action_type === "webhook") {
      const wh = ruleForm.webhook;
      const params: Record<string, unknown> = {
        type: wh.type,
        url: wh.url,
        secret: wh.secret,
      };
      if (wh.type === "email") {
        params.to = wh.to;
        params.smtp_host = wh.smtp_host;
        params.smtp_port = wh.smtp_port;
        params.smtp_user = wh.smtp_user;
        params.smtp_pass = wh.smtp_pass;
        params.from = wh.from;
      }
      return { type: "webhook", params };
    }
    if (ruleForm.action_type === "command") {
      return { type: "command", params: { command: ruleForm.action_config } };
    }
    if (ruleForm.action_type === "notify") {
      return { type: "notify", params: { message: ruleForm.action_config, channel: "all" } };
    }
    if (ruleForm.action_type === "run_macro") {
      if (ruleForm.macro_id == null) return null;
      return { type: "run_macro", params: { macro_id: ruleForm.macro_id, stop_on_error: ruleForm.macro_stop_on_error } };
    }
    return { type: ruleForm.action_type, params: {} };
  };

  const handleSaveRule = async () => {
    try {
      const action = buildAction();
      if (!action) { toast.error(t("auto.macro_required")); return; }
      const payload = {
        name: ruleForm.name,
        event_type: ruleForm.event_type,
        actions: [action],
        conditions: [] as unknown[],
        enabled: true,
      };
      await api.postJson(paths.automation.rules, payload);
      setShowRuleModal(false);
      setRuleForm({ name: "", event_type: "agent.checkin", action_type: "command", action_config: "", webhook: { ...defaultWebhookParams }, macro_id: null, macro_stop_on_error: true });
      loadData();
    } catch { toast.error(t("automation.toast.save_rule_failed")); }
  };

  const handleSaveWebhook = async () => {
    try {
      await api.postJson(paths.automation.webhooks, webhookForm);
      setShowWebhookModal(false);
      setWebhookForm({ name: "", url: "", event_type: "agent.checkin", method: "POST" });
      loadData();
    } catch { toast.error(t("automation.toast.save_webhook_failed")); }
  };

  const handleTestWebhookAction = async () => {
    const wh = ruleForm.webhook;
    setSendingTest(true);
    try {
      const d = await api.postJson<{ success?: boolean; error?: string }>(paths.automation.webhookTest, {
        type: wh.type,
        url: wh.url,
        secret: wh.secret,
        to: wh.to,
        smtp_host: wh.smtp_host,
        smtp_port: wh.smtp_port,
        smtp_user: wh.smtp_user,
        smtp_pass: wh.smtp_pass,
        from: wh.from,
      });
      if (d.success) { toast.success(t("automation.toast.test_sent")); } else { toast.error((d.error as string) || t("automation.toast.test_error")); }
    } catch {
      toast.error(t("automation.toast.test_failed"));
    } finally {
      setSendingTest(false);
    }
  };

  const handleDeleteRule = async (id: string) => {
    if (!(await confirm({ message: t("automation.confirm_delete_rule") }))) return;
    try {
      await api.del(paths.automation.rule(id));
      loadData();
    } catch { toast.error(t("automation.toast.delete_rule_failed")); }
  };

  const handleDeleteWebhook = async (id: number) => {
    if (!(await confirm({ message: t("automation.confirm_delete_webhook") }))) return;
    try {
      await api.del(paths.automation.webhook(id));
      loadData();
    } catch { toast.error(t("automation.toast.delete_webhook_failed")); }
  };

  const handleSaveAlertRule = async () => {
    try {
      await api.postJson(paths.automation.alertRules, alertRuleForm);
      setShowAlertRuleModal(false);
      setAlertRuleForm({ name: "", type: "agent_offline", threshold: 300, description: "" });
      loadData();
    } catch { toast.error(t("automation.toast.save_alert_rule_failed")); }
  };

  const handleDeleteAlertRule = async (id: number) => {
    if (!(await confirm({ message: t("automation.confirm_delete_alert_rule") }))) return;
    try {
      await api.del(paths.automation.alertRule(id));
      loadData();
    } catch { toast.error(t("automation.toast.delete_alert_rule_failed")); }
  };

  const handleToggleAlertRule = async (rule: AlertRule) => {
    if (!rule.id) return;
    try {
      await api.putJson(paths.automation.alertRule(rule.id), { enabled: !rule.enabled, name: rule.name, threshold: rule.threshold, description: rule.description });
      loadData();
    } catch { toast.error(t("automation.toast.toggle_alert_rule_failed")); }
  };

  const handleAckAlert = async (id: number) => {
    try {
      await api.post(paths.automation.alertAck(id));
      loadData();
    } catch { toast.error(t("automation.toast.acknowledge_alert_failed")); }
  };

  const handleResolveAlert = async (id: number) => {
    try {
      await api.post(paths.automation.alertResolve(id));
      loadData();
    } catch { toast.error(t("automation.toast.resolve_alert_failed")); }
  };

  const handleToggleRule = async (id: string) => {
    try {
      await api.post(paths.automation.ruleToggle(id));
      loadData();
    } catch { toast.error(t("automation.toast.toggle_rule_failed")); }
  };

  const events = [
    { type: "agent.checkin", desc: t("auto.event_agent_online"), color: "bg-success" },
    { type: "agent.disconnect", desc: t("auto.event_agent_offline"), color: "bg-destructive" },
    { type: "task.complete", desc: t("auto.event_task_complete"), color: "bg-info" },
    { type: "task.fail", desc: t("auto.event_task_fail"), color: "bg-chart-5" },
    { type: "credential.found", desc: t("auto.event_credential_found"), color: "bg-chart-6" },
  ];

  if (loading)
    return (
      <PageSpinner />
    );

  if (error && rules.length === 0 && webhooks.length === 0) {
    return (
      <PageContainer title={t("auto.title")} subtitle={t("auto.subtitle")}>
        <DataError message={error} onRetry={() => loadData()} />
      </PageContainer>
    );
  }

  const tabItems: { key: TabKey; icon: typeof Zap; label: string }[] = [
    { key: "rules", icon: Zap, label: t("auto.tab_rules") },
    { key: "scheduled", icon: CalendarClock, label: t("auto.tab_scheduled") },
    { key: "workflows", icon: Workflow, label: t("auto.tab_workflows") },
    { key: "webhooks", icon: Globe, label: t("auto.tab_webhooks") },
    { key: "alerts", icon: Bell, label: t("auto.tab_alerts") },
  ];

  return (
    <PageContainer title={t("auto.title")} subtitle={t("auto.subtitle")}>

      <Tabs value={tab} onValueChange={onTabChange}>
        <TabsList className="h-auto gap-1 overflow-x-auto bg-transparent p-0">
          {tabItems.map((item) => {
            const Icon = item.icon;
            return (
              <TabsTrigger key={item.key} value={item.key}
                className="data-[state=active]:bg-secondary data-[state=active]:text-foreground gap-1.5">
                <Icon className="size-3.5" /> {item.label}
              </TabsTrigger>
            );
          })}
        </TabsList>

        <TabsContent value="rules" className="mt-0">
          <div className="flex gap-4 flex-wrap">
            <Card className="overflow-hidden flex-1 min-w-0 shadow-sm hover:shadow-md transition-shadow duration-200">
              <CardHeaderRow accent={false} icon={Zap} tone="warning" title={t("auto.event_listeners")} description={t("auto.event_listeners_desc")} />
              <div className="p-(--card-spacing) space-y-3">
                {events.map((e) => (
                  <div key={e.type} className="flex items-center gap-3 p-3 bg-secondary border border-border rounded-lg hover:bg-secondary/80 transition-colors">
                    <span className={`size-2 ${e.color} rounded-full`}></span>
                    <span className="font-mono text-xs text-foreground">{e.type}</span>
                    <span className="text-muted-foreground">-</span>
                    <span className="text-muted-foreground">{e.desc}</span>
                  </div>
                ))}
              </div>
            </Card>

            <Card className="overflow-hidden flex-1 min-w-0 shadow-sm hover:shadow-md transition-shadow duration-200">
              <CardHeaderRow accent={false} icon={Link} tone="success" title={t("auto.task_chain_rules")} description={t("auto.task_chain_desc")} />
              <div className="p-(--card-spacing)">
                <div className="flex items-center justify-between mb-4">
                  <span className="text-xs text-muted-foreground">{t("auto.rules_count", { count: eventRules.length })}</span>
                  <Button onClick={() => setShowRuleModal(true)} size="sm">
                    <Plus className="size-4" /> {t("auto.new_rule")}
                  </Button>
                </div>
                <div className="space-y-2">
                  {eventRules.length === 0 ? (
                    <div className="text-center text-xs text-muted-foreground py-8">
                      <EmptyState icon={Zap} title={t("automation.empty_rules")} message={t("automation.empty_rules_hint")} />
                    </div>
                  ) : (
                    eventRules.map((r, i) => {
                      const rid = r.id || String(i);
                      const name = r.name || "-";
                      const eventType = r.event_type || "-";
                      const enabled = r.enabled !== undefined ? r.enabled : r.Enabled;
                      const conditionsLen = r.conditions ? r.conditions.length : (r.Conditions ? r.Conditions.length : 0);
                      const actionsLen = r.actions ? r.actions.length : (r.Actions ? r.Actions.length : 0);
                      return (
                        <div key={rid} className="flex items-center justify-between p-3 bg-secondary border border-border rounded-lg">
                          <div className="flex items-center gap-3">
                            <StatusBadge status={enabled ? "online" : "offline"} />
                            <div>
                              <div className="text-sm font-medium text-foreground">{name}</div>
                              <div className="text-xs text-muted-foreground">{eventType} {t("auto.rule_conditions_actions", { conditions: conditionsLen, actions: actionsLen })}</div>
                            </div>
                          </div>
                          <div className="flex items-center gap-2">
                            <Button onClick={() => handleToggleRule(rid)} variant="ghost" size="sm">
                              {enabled ? t("auto.disable") : t("auto.enable")}
                            </Button>
                            <Button onClick={() => handleDeleteRule(rid)} variant="destructive" size="icon-sm" aria-label={t("automation.delete_rule")}>
                              <Trash2 className="size-4" />
                            </Button>
                          </div>
                        </div>
                      );
                    })
                  )}
                </div>
              </div>
            </Card>
          </div>
        </TabsContent>

        <TabsContent value="scheduled" className="mt-0">
          <ScheduledRulesCard onChanged={loadData} />
          <OneShotTasksCard />
        </TabsContent>

        <TabsContent value="workflows" className="mt-0">
          <Card className="overflow-hidden">
            <div className="p-(--card-spacing)">
              <WorkflowsTab />
            </div>
          </Card>
        </TabsContent>

        <TabsContent value="webhooks" className="mt-0">
          <Card className="overflow-hidden">
            <CardHeaderRow accent={false} icon={Globe} tone="info" title={t("auto.webhooks")} description={t("auto.webhooks_desc")} />
            <div className="p-(--card-spacing)">
              <div className="flex items-center justify-between mb-4">
                <span className="text-xs text-muted-foreground">{t("auto.webhooks_count", { count: webhooks.length })}</span>
                <Button onClick={() => setShowWebhookModal(true)} size="sm">
                  <Plus className="size-4" /> {t("auto.new_webhook")}
                </Button>
              </div>
              <div className="space-y-2">
                {webhooks.length === 0 ? (
                  <div className="text-center text-xs text-muted-foreground py-8">
                    <EmptyState icon={Bell} title={t("automation.empty_webhooks")} />
                  </div>
                ) : (
                  webhooks.map((w, i) => {
                    const wid = w.id || i;
                    const name = w.name || "-";
                    const url = w.url || "-";
                    const eventType = w.event_type || "-";
                    const enabled = w.enabled !== undefined ? w.enabled : w.Enabled;
                    return (
                      <div key={String(wid)} className="flex items-center justify-between p-3 bg-secondary border border-border rounded-lg">
                        <div className="flex items-center gap-3">
                          <StatusBadge status={enabled ? "online" : "offline"} />
                          <div>
                            <div className="text-sm font-medium text-foreground">{name}</div>
                            <div className="text-xs text-muted-foreground font-mono">{url}</div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          <span className="text-xs text-muted-foreground">{eventType}</span>
                          <Button
                            onClick={async () => {
                              try {
                                const d = await api.postJson<{ success?: boolean; error?: string }>(paths.automation.webhookTest, { url, method: w.method || "POST" });
                                 if (d.success) { toast.success(t("automation.toast.webhook_test_sent")); } else { toast.error((d.error as string) || t("automation.toast.webhook_test_failed")); }
                              } catch { toast.error(t("automation.toast.webhook_test_failed")); }
                            }}
                            variant="ghost"
                            size="icon-sm"
                            aria-label={t("automation.test_webhook")}
                          >
                            <Tooltip>
                              <TooltipTrigger>
                                <span><FlaskConical className="size-4" /></span>
                              </TooltipTrigger>
                              <TooltipContent>{t("auto.test_webhook")}</TooltipContent>
                            </Tooltip>
                          </Button>
                          <Button onClick={() => handleDeleteWebhook(Number(wid))} variant="destructive" size="icon-sm" aria-label={t("automation.delete_webhook")}>
                            <Trash2 className="size-4" />
                          </Button>
                        </div>
                      </div>
                    );
                  })
                )}
              </div>
            </div>
          </Card>
        </TabsContent>

        <TabsContent value="alerts" className="mt-0">
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-4">
            <Card className="overflow-hidden">
              <CardHeaderRow accent={false} icon={Bell} tone="destructive" title={t("auto.alert_rules")} description={t("auto.alert_rules_desc")} action={
                <Button onClick={() => setShowAlertRuleModal(true)} size="sm">
                  <Plus className="size-4" />{t("auto.new")}
                </Button>
              } />
              <div className="p-(--card-spacing) space-y-2">
                {alertRules.length === 0 ? (
                  <p className="text-xs text-muted-foreground text-center py-6">{t("auto.no_alert_rules")}</p>
                ) : alertRules.map((r) => (
                  <div key={r.id} className="flex items-center justify-between p-3 bg-secondary border border-border rounded-lg">
                    <div>
                      <div className="text-sm font-medium text-foreground">{r.name}</div>
                      <div className="text-xs text-muted-foreground">{t(`auto.type_${r.type}`)} {r.threshold}s</div>
                    </div>
                    <div className="flex gap-2">
                      <Button onClick={() => handleToggleAlertRule(r)} variant="ghost" size="sm">
                        {r.enabled ? t("auto.on") : t("auto.off")}
                      </Button>
                      <Button onClick={() => r.id && handleDeleteAlertRule(r.id)} variant="destructive" size="icon-sm" aria-label={t("automation.delete_alert_rule")}><Trash2 className="size-4" /></Button>
                    </div>
                  </div>
                ))}
              </div>
            </Card>

            <Card className="overflow-hidden">
              <CardHeaderRow accent={false} icon={AlertTriangle} tone="warning" title={t("auto.active_alerts")} description={t("auto.active_alerts_desc")} />
              <div className="p-(--card-spacing) space-y-2 max-h-80 overflow-y-auto">
                {alerts.length === 0 ? (
                  <p className="text-xs text-muted-foreground text-center py-6">{t("auto.no_alerts")}</p>
                ) : alerts.map((a) => (
                  <div key={a.id} className="p-3 bg-secondary border border-border rounded-lg">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0">
                        <div className="text-sm font-medium text-foreground truncate">{a.title || t("auto.alert")}</div>
                        <div className="text-xs text-muted-foreground truncate">{a.message}</div>
                        <div className="text-xs text-muted-foreground mt-1">{a.severity} {a.status} {a.source_name || ""}</div>
                      </div>
                      {a.status === "active" && a.id != null && (
                        <div className="flex gap-1 shrink-0">
                          <Button onClick={() => handleAckAlert(Number(a.id))} variant="ghost" size="sm">{t("auto.ack")}</Button>
                          <Button onClick={() => handleResolveAlert(Number(a.id))} variant="ghost" size="sm">{t("auto.resolve")}</Button>
                        </div>
                      )}
                    </div>
                  </div>
                ))}
              </div>
            </Card>
          </div>
        </TabsContent>
      </Tabs>

      <RuleDialog
        open={showRuleModal}
        onOpenChange={setShowRuleModal}
        ruleForm={ruleForm}
        setRuleForm={setRuleForm}
        sendingTest={sendingTest}
        onTestWebhook={handleTestWebhookAction}
        onSave={handleSaveRule}
      />

      <WebhookDialog
        open={showWebhookModal}
        onOpenChange={setShowWebhookModal}
        webhookForm={webhookForm}
        setWebhookForm={setWebhookForm}
        onSave={handleSaveWebhook}
      />

      <AlertRuleDialog
        open={showAlertRuleModal}
        onOpenChange={setShowAlertRuleModal}
        alertRuleForm={alertRuleForm}
        setAlertRuleForm={setAlertRuleForm}
        onSave={handleSaveAlertRule}
      />

      {modal}
    </PageContainer>
  );
}
