
import { useCallback, useEffect, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { EmptyState } from "@/components/ui/empty-state";
import { DataError } from "@/components/ui/data-state";
import { FieldError } from "@/components/ui/field-error";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useMutation } from "@/lib/hooks/useMutation";
import { useApiResource } from "@/lib/hooks/useApiResource";
import { Button } from "@/components/ui/button";
import type { Agent } from "@/types/agent";
import { formatTime } from "@/lib/utils";
import { Card } from "@/components/ui/card";
import { CardHeaderRow } from "@/components/ui/card-header-row";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Textarea } from "@/components/ui/textarea";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { useI18n } from "@/lib/i18n";
import { toast } from "sonner";
import { fetchTaskTypes, type TaskTypeInfo } from "@/lib/taskTypes";
import { AlertTriangle, Bug, Calendar, CalendarClock, Clock, Pencil, Plus, Trash2, X } from "lucide-react";

interface ScheduledRule {
  id: string;
  name: string;
  event_type?: string;
  enabled: boolean;
  agent_id: string;
  task_type: string;
  command: string;
  params: string;
  schedule: string;
  last_run: string;
  next_run: string;
  run_count: number;
  created_by: string;
  created_at: string;
}

export function ScheduledRulesCard({ onChanged }: { onChanged?: () => void }) {
  const { t } = useI18n();
  const [showForm, setShowForm] = useState(false);
  const [editingId, setEditingId] = useState<string | null>(null);
  const [name, setName] = useState("");
  const [agentId, setAgentId] = useState("");
  const [taskType, setTaskType] = useState("shell");
  const [command, setCommand] = useState("");
  const [params, setParams] = useState("");
  const [schedule, setSchedule] = useState("");
  const [taskTypes, setTaskTypes] = useState<TaskTypeInfo[]>([]);
  const [taskTypesError, setTaskTypesError] = useState<string | null>(null);
  const { confirm, modal } = useConfirm();
  const [formErrors, setFormErrors] = useState<{ name?: string; agentId?: string; schedule?: string; command?: string }>({});

  const { mutate: runSave, isPending: saving } = useMutation({
    fn: async () => {
      const payload = {
        name,
        event_type: "schedule",
        conditions: [] as unknown[],
        actions: buildAction(),
        schedule,
        agent_id: agentId,
        task_type: taskType,
        command,
        params,
        enabled: true,
      };
      if (editingId) {
        await api.putJson(paths.automation.rule(editingId), payload);
      } else {
        await api.postJson(paths.automation.rules, payload);
      }
    },
    onSuccess: () => {
      resetForm();
      setShowForm(false);
      toast.success(t("scheduler.saved"));
      fetchData();
      onChanged?.();
    },
    onError: () => toast.error(t("scheduler.save_failed")),
  });

  const { data, loading, error, refresh: fetchData } = useApiResource<{ tasks: ScheduledRule[]; agents: Agent[] }>({
    fetcher: async () => {
      const [rules, agentList] = await Promise.all([
        api.get<ScheduledRule[]>(paths.automation.rules),
        fetchAgentListCached(),
      ]);
      return {
        tasks: (Array.isArray(rules) ? rules : []).filter((r) => r.event_type === "schedule"),
        agents: agentList,
      };
    },
    errorMessage: t("scheduler.load_failed"),
  });
  const tasks = data?.tasks ?? [];
  const agents = data?.agents ?? [];

  const loadTaskTypes = useCallback(async () => {
    setTaskTypesError(null);
    try {
      setTaskTypes(await fetchTaskTypes());
    } catch (e) {
      // Retain the last known task-type list on failure so the bare
      // dropdown is never mistaken for "the server has no task types".
      setTaskTypesError(e instanceof Error ? e.message : String(e));
    }
  }, []);

  useEffect(() => {
    void loadTaskTypes();
  }, [loadTaskTypes]);

  function resetForm() {
    setName(""); setAgentId(""); setTaskType("shell"); setCommand(""); setParams(""); setSchedule("");
    setEditingId(null);
    setFormErrors({});
  }

  const buildAction = () => {
    return [{ type: "create_task", params: { agent_id: agentId, type: taskType, command } }];
  };

  async function handleSave() {
    const errors: { name?: string; agentId?: string; schedule?: string; command?: string } = {};
    if (!name.trim()) errors.name = t("scheduler.err_name_required");
    if (!agentId) errors.agentId = t("scheduler.err_agent_required");
    if (!schedule.trim()) errors.schedule = t("scheduler.err_schedule_required");
    if (taskType === "shell" && !command.trim()) errors.command = t("scheduler.err_command_required");
    setFormErrors(errors);
    if (Object.keys(errors).length > 0) return;
    await runSave();
  }

  async function handleToggle(id: string) {
    try {
      await api.postJson(paths.automation.ruleToggle(id), {});
      fetchData();
      onChanged?.();
    } catch {
      toast.error(t("scheduler.toggle_failed"));
    }
  }

  async function handleDelete(id: string) {
    if (!(await confirm({ message: t("scheduler.delete_confirm") }))) return;
    try {
      await api.del(paths.automation.rule(id));
      fetchData();
      onChanged?.();
    } catch {
      toast.error(t("scheduler.delete_failed"));
    }
  }

  function editTask(task: ScheduledRule) {
    setEditingId(task.id);
    setName(task.name);
    setAgentId(task.agent_id);
    setTaskType(task.task_type);
    setCommand(task.command);
    setParams(task.params);
    setSchedule(task.schedule);
    setShowForm(true);
  }

  return (
    <Card id="scheduled" className="overflow-hidden scroll-mt-20">
      <CardHeaderRow accent={false} icon={CalendarClock} tone="warning" title={t("auto.scheduled_tasks")} description={t("auto.scheduled_tasks_desc")} action={<>        {showForm && (
          <Button variant="outline" size="sm" onClick={() => { setShowForm(false); resetForm(); }}>
            <X className="size-4" /> {t("common.cancel")}
          </Button>
        )}
        <Button size="sm" onClick={() => { resetForm(); setShowForm(true); }} className="gap-2">
          <Plus className="size-4" /> {t("scheduler.new_schedule")}
        </Button>
      </>} />

      <div className="p-(--card-spacing)">
        {showForm && (
          <Card className="mb-4 border-border/60">
            <div className="p-4">
              <h3 className="text-sm font-semibold mb-4">
                {editingId ? t("scheduler.edit_schedule") : t("scheduler.new_schedule")}
              </h3>
              <div className="grid grid-cols-1 sm:grid-cols-2 gap-4 mb-4">
                <div>
                  <Label className="mb-1.5">{t("scheduler.name")}</Label>
                  <Input id="sched-name" value={name} onChange={e => { setName(e.target.value); if (formErrors.name) setFormErrors({ ...formErrors, name: undefined }); }} aria-label={t("scheduler.a11y_name")} aria-invalid={!!formErrors.name} aria-describedby={formErrors.name ? "sched-name-error" : undefined} />
                  <FieldError id="sched-name-error">{formErrors.name}</FieldError>
                </div>
                <div>
                  <Label className="mb-1.5">{t("scheduler.agent")}</Label>
                  <Select value={agentId} onValueChange={(v) => { setAgentId(v ?? ""); if (formErrors.agentId) setFormErrors({ ...formErrors, agentId: undefined }); }}>
                    <SelectTrigger id="sched-agent" aria-label={t("scheduler.a11y_agent")} aria-invalid={!!formErrors.agentId} aria-describedby={formErrors.agentId ? "sched-agent-error" : undefined}>
                      <SelectValue placeholder={t("scheduler.select_agent")} />
                    </SelectTrigger>
                    <SelectContent>
                      {agents.map(a => (
                        <SelectItem key={a.id} value={a.id}>{a.hostname} ({a.ip})</SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                  <FieldError id="sched-agent-error">{formErrors.agentId}</FieldError>
                </div>
                <div>
                  <Label className="mb-1.5">{t("scheduler.task_type")}</Label>
                  <Select value={taskType} onValueChange={(v) => setTaskType(v ?? "shell")}>
                    <SelectTrigger aria-label={t("scheduler.a11y_type")}>
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {taskTypes.map(tt => <SelectItem key={tt.type} value={tt.type}>{tt.name || tt.type}</SelectItem>)}
                    </SelectContent>
                  </Select>
                  {taskTypesError && taskTypes.length === 0 && (
                    <div className="mt-1.5 flex flex-wrap items-center gap-2 rounded-lg border border-warning/30 bg-warning/10 px-2 py-1.5 text-xs text-warning-ink" role="alert">
                      <AlertTriangle className="size-3.5 shrink-0" />
                      <span className="flex-1 min-w-[140px]">{t("scheduler.tasktypes_unreadable", { message: taskTypesError })}</span>
                      <button type="button" className="shrink-0 underline" onClick={() => void loadTaskTypes()}>{t("common.try_again")}</button>
                    </div>
                  )}
                </div>
                <div>
                  <Label className="mb-1.5">{t("scheduler.schedule")}</Label>
                  <Input id="sched-schedule" value={schedule} onChange={e => { setSchedule(e.target.value); if (formErrors.schedule) setFormErrors({ ...formErrors, schedule: undefined }); }} placeholder={t("scheduler.schedule_ph")} aria-label={t("scheduler.a11y_expr")} aria-invalid={!!formErrors.schedule} aria-describedby={formErrors.schedule ? "sched-schedule-error" : undefined} />
                  <p className="text-xs-sm text-muted-foreground mt-1 space-y-0.5">
                    <code className="px-1 bg-muted rounded text-micro-sm">every N minutes</code> ·{" "}
                    <code className="px-1 bg-muted rounded text-micro-sm">hourly</code> ·{" "}
                    <code className="px-1 bg-muted rounded text-micro-sm">daily HH:MM</code> ·{" "}
                    <code className="px-1 bg-muted rounded text-micro-sm">* * * * *</code>
                  </p>
                  <FieldError id="sched-schedule-error">{formErrors.schedule}</FieldError>
                </div>
              </div>
              <div className="mb-4">
                <Label className="mb-1.5">{t("scheduler.command")}</Label>
                  <Input id="sched-command" value={command} onChange={e => { setCommand(e.target.value); if (formErrors.command) setFormErrors({ ...formErrors, command: undefined }); }} placeholder={t("scheduler.cmd_ph")} aria-label={t("scheduler.command")} aria-invalid={!!formErrors.command} aria-describedby={formErrors.command ? "sched-command-error" : undefined} />
                  <FieldError id="sched-command-error">{formErrors.command}</FieldError>
              </div>
              <div className="mb-4">
                <Label className="mb-1.5">{t("scheduler.params")}</Label>
                <Textarea value={params} onChange={e => setParams(e.target.value)} rows={2} aria-label={t("scheduler.a11y_params")} />
              </div>
              <div className="flex gap-2">
                <Button onClick={handleSave} disabled={saving}>{editingId ? t("autotag.update") : t("autotag.create")}</Button>
                <Button variant="outline" onClick={() => { setShowForm(false); resetForm(); }}>{t("common.cancel")}</Button>
              </div>
            </div>
          </Card>
        )}

        {loading ? (
          <div className="space-y-2">
            {[1, 2].map(i => (
              <Card key={i} className="p-(--card-spacing)"><Skeleton className="h-12 w-full" /></Card>
            ))}
          </div>
        ) : tasks.length === 0 ? (
          error ? (
            <DataError message={t("scheduler.rules_unreadable", { message: error })} onRetry={() => void fetchData()} />
          ) : (
            <EmptyState icon={Clock} title={t("scheduler.empty_title")} />
          )
        ) : (
          <div className="space-y-2">
            {tasks.map(task => (
              <div key={task.id} className="flex items-center gap-4 p-3 bg-secondary border border-border rounded-lg">
                <Switch checked={task.enabled} onCheckedChange={() => handleToggle(task.id)} aria-label={task.enabled ? t("scheduler.a11y_disable") : t("scheduler.a11y_enable")} />
                <div className="flex-1 min-w-0">
                  <div className="flex items-center gap-2 flex-wrap">
                    <span className="font-medium text-sm">{task.name}</span>
                    <Badge variant="secondary">{task.task_type}</Badge>
                    <span className="text-xs-sm text-muted-foreground">{t("scheduler.run_count")} {task.run_count}x</span>
                  </div>
                  <div className="flex flex-wrap gap-x-4 gap-y-0.5 text-compact text-muted-foreground mt-0.5">
                    <span className="inline-flex items-center gap-1"><Bug className="size-3.5" />{agents.find(a => a.id === task.agent_id)?.hostname || task.agent_id?.slice(0, 8)}</span>
                    <span className="inline-flex items-center gap-1"><Calendar className="size-3.5" />{task.schedule}</span>
                    {task.next_run && <span className="inline-flex items-center gap-1"><Clock className="size-3.5" />{t("scheduler.next")}: {formatTime(task.next_run)}</span>}
                    {task.last_run && <span className="text-muted-foreground">{t("scheduler.last")}: {formatTime(task.last_run)}</span>}
                    {task.created_by && <span className="text-muted-foreground">{t("groups.created_by")} {task.created_by}</span>}
                  </div>
                </div>
                <div className="flex gap-1 shrink-0">
                  <Button variant="outline" size="sm" onClick={() => editTask(task)} className="size-8 p-0" aria-label={t("scheduler.a11y_edit")}>
                    <Pencil className="size-4" />
                  </Button>
                  <Button variant="outline" size="sm" onClick={() => handleDelete(task.id)} className="size-8 p-0 border-destructive/20 text-destructive-ink hover:bg-destructive/10" aria-label={t("scheduler.a11y_delete")}>
                    <Trash2 className="size-4" />
                  </Button>
                </div>
              </div>
            ))}
          </div>
        )}
      </div>

      {modal}
    </Card>
  );
}
