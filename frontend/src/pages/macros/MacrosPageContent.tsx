import { useCallback, useEffect, useMemo, useState } from "react";
import { registerFragment } from "@/lib/i18n";
import { enAI } from "@/lib/i18n/fragments/en.ai";
registerFragment(enAI);
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import { useI18n } from "@/lib/i18n";
import { errorText } from "@/lib/errors";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { useWS } from "@/lib/wsContext";
import { PageContainer } from "@/components/ui/page-container";
import { SectionCard } from "@/components/ui/section-card";
import { Button } from "@/components/ui/button";
import { Badge } from "@/components/ui/badge";
import { Input } from "@/components/ui/input";
import { Label } from "@/components/ui/label";
import { Switch } from "@/components/ui/switch";
import { Spinner } from "@/components/ui/spinner";
import { EmptyState } from "@/components/ui/empty-state";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter,
} from "@/components/ui/dialog";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { AIPlaybookDialog } from "./components/AIPlaybookDialog";
import type { Agent } from "@/types/agent";
import {
  AlertTriangle, ArrowDown, ArrowUp, Copy, ListOrdered, Play, Plus, Square, Terminal, Trash2,
} from "lucide-react";

interface MacroStep {
  command: string;
  delay_ms: number;
  wait: boolean;
  timeout_s: number;
  stop_on_error?: boolean;
}

// Client-only stable identity for editor step rows. Persisted macros stay
// uid-free; it only keeps row keys stable across reorder/delete so the
// per-row controlled inputs don't lose focus or bleed values after a move.
type EditorStep = MacroStep & { uid: string };

let stepUidSeq = 0;
const newStepUid = (): string => `step-${++stepUidSeq}`;

interface Macro {
  id?: number;
  name: string;
  description?: string;
  steps?: string;
  created_by?: string;
  created_at?: string;
}

interface MacroRunLogEntry {
  step: number;
  command: string;
  status: string;
  task_id?: number;
  output?: string;
  error?: string;
  timestamp?: string;
}

interface MacroRun {
  id: number;
  macro_id: number;
  macro_name: string;
  agent_id: string;
  status: string;
  current_step: number;
  total_steps: number;
  log: string;
  created_by?: string;
  started_at: string;
  finished_at?: string | null;
}

function parseSteps(raw: string | undefined): MacroStep[] {
  if (!raw) return [];
  try {
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? (arr as MacroStep[]) : [];
  } catch {
    return [];
  }
}

function runLog(raw: string): MacroRunLogEntry[] {
  try {
    const arr = JSON.parse(raw);
    return Array.isArray(arr) ? (arr as MacroRunLogEntry[]) : [];
  } catch {
    return [];
  }
}

const emptyStep = (): EditorStep => ({ command: "", delay_ms: 500, wait: false, timeout_s: 120, uid: newStepUid() });

const RUN_STATUS_VARIANT: Record<string, "success" | "destructive" | "warning" | "secondary"> = {
  completed: "success",
  failed: "destructive",
  running: "warning",
  stopped: "secondary",
};

export default function MacrosPageContent() {
  const { t } = useI18n();
  const { subscribe } = useWS();
  const { confirm, modal } = useConfirm();
  const { toastError } = useErrorToast();

  const [macros, setMacros] = useState<Macro[]>([]);
  const [runs, setRuns] = useState<MacroRun[]>([]);
  const [loading, setLoading] = useState(true);
  const [runsLoading, setRunsLoading] = useState(true);
  const [agents, setAgents] = useState<Agent[]>([]);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [runsError, setRunsError] = useState<string | null>(null);
  const [agentsError, setAgentsError] = useState<string | null>(null);

  // Editor dialog state
  const [editorOpen, setEditorOpen] = useState(false);
  const [editingId, setEditingId] = useState<number | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [steps, setSteps] = useState<EditorStep[]>([emptyStep()]);
  const [saving, setSaving] = useState(false);

  // Run dialog state
  const [runTarget, setRunTarget] = useState<Macro | null>(null);
  const [runSelected, setRunSelected] = useState<Set<string>>(new Set());
  const [stopOnError, setStopOnError] = useState(true);
  const [running, setRunning] = useState(false);

  // Detail dialog
  const [detailRun, setDetailRun] = useState<MacroRun | null>(null);

  const loadMacros = useCallback(async () => {
    setLoadError(null);
    try {
      const d = await api.get<{ macros?: Macro[] }>(paths.macros.list);
      setMacros(d.macros || []);
    } catch (e) {
      // Retain the last good list: wiping it to [] meant an API blip read
      // to the operator as "no macros exist".
      setLoadError(errorText(t, e, () => t("common.load_failed")));
    } finally {
      setLoading(false);
    }
  }, [t]);

  const loadRuns = useCallback(async () => {
    setRunsLoading(true);
    setRunsError(null);
    try {
      const d = await api.get<{ runs?: MacroRun[] }>(paths.macros.runs());
      setRuns(d.runs || []);
    } catch (e) {
      // Retain the previous run list; only report the failure so a blip
      // cannot masquerade as "no runs".
      setRunsError(errorText(t, e, () => t("common.load_failed")));
    } finally {
      setRunsLoading(false);
    }
  }, [t]);

  const loadAgents = useCallback(async () => {
    setAgentsError(null);
    try {
      setAgents(await fetchAgentListCached());
    } catch (e) {
      // Same: a wiped agent list read as "no agents on the network".
      setAgentsError(errorText(t, e, () => t("common.load_failed")));
    }
  }, [t]);

  useEffect(() => {
    void loadMacros();
    void loadRuns();
    void loadAgents();
  }, [loadMacros, loadRuns, loadAgents]);

  // Live progress: refresh the runs table whenever a macro_update arrives.
  useEffect(() => {
    const unsub = subscribe((msg) => {
      if ((msg as { type?: string }).type === "macro_update") void loadRuns();
    });
    return unsub;
  }, [subscribe, loadRuns]);

  const openCreate = () => {
    setEditingId(null);
    setName("");
    setDescription("");
    setSteps([emptyStep()]);
    setEditorOpen(true);
  };

  const openEdit = (m: Macro) => {
    setEditingId(m.id ?? null);
    setName(m.name);
    setDescription(m.description || "");
    const parsed = parseSteps(m.steps);
    setSteps(parsed.length > 0 ? parsed.map((s) => ({ ...emptyStep(), ...s, uid: newStepUid() })) : [emptyStep()]);
    setEditorOpen(true);
  };

  const updateStep = (idx: number, patch: Partial<MacroStep>) => {
    setSteps(prev => prev.map((s, i) => (i === idx ? { ...s, ...patch } : s)));
  };

  const moveStep = (idx: number, dir: -1 | 1) => {
    setSteps(prev => {
      const next = [...prev];
      const j = idx + dir;
      if (j < 0 || j >= next.length) return prev;
      [next[idx], next[j]] = [next[j], next[idx]];
      return next;
    });
  };

  const handleSave = async () => {
    if (!name.trim()) { toast.error(t("macros.toast.name_required")); return; }
    // Project back to the persisted MacroStep shape — the client-only uid
    // stays out of the request body.
    const valid: MacroStep[] = steps
      .filter((s) => s.command.trim() !== "")
      .map((s) => ({ command: s.command, delay_ms: s.delay_ms, wait: s.wait, timeout_s: s.timeout_s, stop_on_error: s.stop_on_error }));
    if (valid.length === 0) { toast.error(t("macros.toast.step_required")); return; }
    setSaving(true);
    try {
      const body = { name: name.trim(), description, steps: valid };
      if (editingId != null) await api.putJson(paths.macros.one(editingId), body);
      else await api.postJson(paths.macros.list, body);
      toast.success(t("macros.toast.saved"));
      setEditorOpen(false);
      void loadMacros();
    } catch (e) {
      toastError(e, "macros.toast.save_failed");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (m: Macro) => {
    if (!(await confirm({ message: t("macros.confirm_delete", { name: m.name }) }))) return;
    try {
      await api.del(paths.macros.one(m.id!));
      toast.success(t("macros.toast.deleted"));
      void loadMacros();
    } catch (e) {
      toastError(e, "macros.toast.delete_failed");
    }
  };

  const openRun = (m: Macro) => {
    setRunTarget(m);
    setRunSelected(new Set());
  };

  const toggleAgent = (id: string) => {
    setRunSelected(prev => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id); else next.add(id);
      return next;
    });
  };

  const handleRun = async () => {
    if (!runTarget || runSelected.size === 0) return;
    setRunning(true);
    try {
      await api.postJson(paths.macros.run(runTarget.id!), {
        agent_ids: [...runSelected],
        stop_on_error: stopOnError,
      });
      toast.success(t("macros.toast.dispatched", { count: runSelected.size }));
      setRunTarget(null);
      void loadRuns();
    } catch (e) {
      toastError(e, "macros.toast.run_failed");
    } finally {
      setRunning(false);
    }
  };

  const handleStopRun = async (id: number) => {
    try {
      await api.postJson(paths.macros.stopRun(id), {});
      toast.success(t("macros.toast.stopped"));
      void loadRuns();
    } catch (e) {
      toastError(e, "macros.toast.stop_failed");
    }
  };

  const macroCount = useMemo(() => macros.length, [macros]);

  return (
    <PageContainer
      title={t("macros.title")}
      icon={<ListOrdered className="size-4" />}
      subtitle={t("macros.subtitle")}
      actions={
        <>
          <AIPlaybookDialog onSaved={loadMacros} />
          <Button onClick={openCreate}>
            <Plus className="size-4" /> {t("macros.create")}
          </Button>
        </>
      }
    >
      {modal}
      {/* ── Macro library ─────────────────────────────────────────── */}
      <SectionCard
        title={t("macros.library")}
        icon={ListOrdered}
        tone="primary"
        count={macroCount}
        headerAccent
      >
        {loading ? (
          <div className="py-12 text-center"><Spinner /></div>
        ) : macros.length === 0 ? (
          loadError ? (
            <div role="alert" className="m-5 flex flex-wrap items-center gap-2 border border-warning/30 bg-warning/10 px-3 py-2 rounded-lg text-xs text-warning-ink">
              <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 flex-1">{t("macros.library_unreadable", { message: loadError })}</span>
              <Button onClick={() => void loadMacros()} size="xs" variant="outline">{t("common.try_again")}</Button>
            </div>
          ) : (
            <EmptyState
              icon={ListOrdered}
              title={t("macros.empty_title")}
              message={t("macros.empty_message")}
            />
          )
        ) : (
          <div className="divide-y divide-border">
            {macros.map((m) => {
              const parsed = parseSteps(m.steps);
              return (
                <div key={m.id} className="px-5 py-3.5 flex items-center gap-4 hover:bg-muted/40 transition-colors">
                  <div className="size-9 rounded-lg bg-primary/10 dark:bg-primary/20 flex items-center justify-center shrink-0">
                    <Terminal className="size-4 text-primary" />
                  </div>
                  <div className="flex-1 min-w-0">
                    <div className="font-medium text-sm text-foreground truncate">{m.name}</div>
                    <div className="text-xs text-muted-foreground truncate">
                      {parsed.length} {t("macros.steps_count")} · {m.description || m.created_by || ""}
                    </div>
                  </div>
                  <div className="flex items-center gap-1 shrink-0">
                    <Button size="sm" onClick={() => openRun(m)} aria-label={t("macros.run")}>
                      <Play className="size-4" /> {t("macros.run")}
                    </Button>
                    <Button variant="ghost" size="icon-sm" onClick={() => openEdit(m)} aria-label={t("common.edit")}>
                      <Copy className="size-4" />
                    </Button>
                    <Button variant="ghost" size="icon-sm" onClick={() => void handleDelete(m)} aria-label={t("common.delete")}
                      className="text-muted-foreground hover:text-destructive-ink">
                      <Trash2 className="size-4" />
                    </Button>
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </SectionCard>

      {/* ── Run history ───────────────────────────────────────────── */}
      <SectionCard
        title={t("macros.runs_title")}
        icon={Play}
        tone="success"
        headerAccent
        action={
          <Button variant="ghost" size="xs" onClick={() => void loadRuns()}>{t("common.refresh")}</Button>
        }
      >
        {runsLoading ? (
          <div className="space-y-2 p-5">
            {[1, 2, 3].map((i) => (
              <Skeleton key={i} className="h-8 w-full" />
            ))}
          </div>
        ) : runs.length === 0 ? (
          runsError ? (
            <div role="alert" className="m-5 flex flex-wrap items-center gap-2 border border-warning/30 bg-warning/10 px-3 py-2 rounded-lg text-xs text-warning-ink">
              <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
              <span className="min-w-0 flex-1">{t("macros.runs_unreadable", { message: runsError })}</span>
              <Button onClick={() => void loadRuns()} size="xs" variant="outline">{t("common.try_again")}</Button>
            </div>
          ) : (
            <EmptyState icon={Play} title={t("macros.runs_empty")} />
          )
        ) : (
          <div className="divide-y divide-border max-h-80 overflow-y-auto">
            {runs.map((r) => (
              <div key={r.id} className="px-5 py-2.5 flex items-center gap-3 text-sm">
                <Badge variant={RUN_STATUS_VARIANT[r.status] || "secondary"} className="shrink-0">{r.status}</Badge>
                <span className="font-medium text-foreground truncate max-w-[180px]">{r.macro_name}</span>
                <span className="font-mono text-xs text-muted-foreground truncate">{r.agent_id}</span>
                <span className="ml-auto text-xs text-muted-foreground shrink-0">
                  {r.current_step}/{r.total_steps}
                </span>
                <button
                  className="text-xs text-primary hover:underline shrink-0"
                  onClick={() => setDetailRun(r)}
                >
                  {t("macros.view_log")}
                </button>
                {r.status === "running" && (
                  <Button variant="ghost" size="icon-xs" onClick={() => void handleStopRun(r.id)} aria-label={t("macros.stop")}
                    className="text-destructive-ink">
                    <Square className="size-3.5" />
                  </Button>
                )}
              </div>
            ))}
          </div>
        )}
      </SectionCard>

      {/* ── Editor dialog ─────────────────────────────────────────── */}
      <Dialog open={editorOpen} onOpenChange={setEditorOpen}>
        <DialogContent size="2xl" className="max-h-[85vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>{editingId != null ? t("macros.edit_title") : t("macros.create_title")}</DialogTitle>
          </DialogHeader>
          <div className="space-y-4">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <div>
                <Label htmlFor="macros-field-name">{t("macros.field_name")}</Label>
                <Input id="macros-field-name" value={name} onChange={(e) => setName(e.target.value)} placeholder="recon-sweep" />
              </div>
              <div>
                <Label htmlFor="macros-field-desc">{t("macros.field_desc")}</Label>
                <Input id="macros-field-desc" value={description} onChange={(e) => setDescription(e.target.value)} />
              </div>
            </div>

            <div className="space-y-2">
              <Label>{t("macros.field_steps")}</Label>
              {steps.map((step, idx) => (
                <div key={step.uid} className="rounded-lg border border-border p-3 space-y-2">
                  <div className="flex items-center gap-2">
                    <Badge variant="outline" className="font-mono">{idx + 1}</Badge>
                    <Input
                      value={step.command}
                      onChange={(e) => updateStep(idx, { command: e.target.value })}
                      placeholder="whoami && hostname"
                      className="font-mono flex-1"
                    />
                    <div className="flex shrink-0">
                      <Button variant="ghost" size="icon-xs" disabled={idx === 0} onClick={() => moveStep(idx, -1)} aria-label={t("macros.move_up")}>
                        <ArrowUp className="size-3.5" />
                      </Button>
                      <Button variant="ghost" size="icon-xs" disabled={idx === steps.length - 1} onClick={() => moveStep(idx, 1)} aria-label={t("macros.move_down")}>
                        <ArrowDown className="size-3.5" />
                      </Button>
                      <Button variant="ghost" size="icon-xs"
                        onClick={() => setSteps(prev => (prev.length > 1 ? prev.filter((_, i) => i !== idx) : prev))}
                        aria-label={t("common.delete")} className="text-muted-foreground hover:text-destructive-ink">
                        <Trash2 className="size-3.5" />
                      </Button>
                    </div>
                  </div>
                  <div className="flex items-center gap-4 flex-wrap text-xs">
                    <label className="flex items-center gap-1.5 cursor-pointer select-none">
                      <Switch checked={step.wait} onCheckedChange={(v) => updateStep(idx, { wait: v === true })} />
                      <span className="text-muted-foreground">{t("macros.step_wait")}</span>
                    </label>
                    {step.wait ? (
                      <label className="flex items-center gap-1.5">
                        <span className="text-muted-foreground">{t("macros.step_timeout")}</span>
                        <Input type="number" min={5} max={1800} value={step.timeout_s}
                          onChange={(e) => updateStep(idx, { timeout_s: Number(e.target.value) || 120 })}
                          className="w-20 h-7 text-xs" />
                        s
                      </label>
                    ) : (
                      <label className="flex items-center gap-1.5">
                        <span className="text-muted-foreground">{t("macros.step_delay")}</span>
                        <Input type="number" min={0} max={3600000} value={step.delay_ms}
                          onChange={(e) => updateStep(idx, { delay_ms: Number(e.target.value) || 0 })}
                          className="w-24 h-7 text-xs" />
                        ms
                      </label>
                    )}
                    <label className="flex items-center gap-1.5 cursor-pointer select-none ml-auto">
                      <Switch checked={step.stop_on_error !== false} onCheckedChange={(v) => updateStep(idx, { stop_on_error: v === true })} />
                      <span className="text-muted-foreground">{t("macros.step_stop_on_error")}</span>
                    </label>
                  </div>
                </div>
              ))}
              <Button variant="outline" size="sm" onClick={() => setSteps(prev => [...prev, emptyStep()])}>
                <Plus className="size-4" /> {t("macros.add_step")}
              </Button>
            </div>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setEditorOpen(false)}>{t("common.cancel")}</Button>
            <Button onClick={handleSave} disabled={saving}>
              {saving ? <Spinner size="sm" /> : t("common.save")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Run dialog ────────────────────────────────────────────── */}
      <Dialog open={!!runTarget} onOpenChange={(open) => { if (!open) setRunTarget(null); }}>
        <DialogContent size="lg">
          <DialogHeader>
            <DialogTitle>{t("macros.run_title", { name: runTarget?.name || "" })}</DialogTitle>
          </DialogHeader>
          <div className="space-y-3">
            <p className="text-xs text-muted-foreground">
              {parseSteps(runTarget?.steps).length} {t("macros.steps_count")} ·{" "}
              {t("macros.select_agents_hint")}
            </p>
            <div className="max-h-56 overflow-y-auto rounded-lg border border-border divide-y divide-border">
              {agents.length === 0 ? (
                agentsError ? (
                  <div role="alert" className="m-3 flex flex-wrap items-center gap-2 border border-warning/30 bg-warning/10 px-3 py-2 rounded-lg text-xs text-warning-ink">
                    <AlertTriangle className="size-3.5 shrink-0" aria-hidden="true" />
                    <span className="min-w-0 flex-1">{t("macros.agents_unreadable", { message: agentsError })}</span>
                    <Button onClick={() => void loadAgents()} size="xs" variant="outline">{t("common.try_again")}</Button>
                  </div>
                ) : (
                  <p className="text-xs text-muted-foreground text-center py-6">{t("macros.no_agents")}</p>
                )
              ) : agents.map((a) => (
                <label key={a.id} className="flex items-center gap-2.5 px-3 py-2 cursor-pointer hover:bg-muted/50 text-sm">
                  <input
                    type="checkbox"
                    checked={runSelected.has(String(a.id))}
                    onChange={() => toggleAgent(String(a.id))}
                    className="accent-current"
                  />
                  <span className="truncate flex-1">{a.hostname || a.id}</span>
                  <span className="font-mono text-xs text-muted-foreground">{a.ip}</span>
                  <Badge variant={a.status === "online" ? "success" : "secondary"} className="text-micro">
                    {a.status}
                  </Badge>
                </label>
              ))}
            </div>
            <label className="flex items-center gap-2 cursor-pointer select-none">
              <Switch checked={stopOnError} onCheckedChange={(v) => setStopOnError(v === true)} />
              <span className="text-sm text-muted-foreground">{t("macros.global_stop_on_error")}</span>
            </label>
          </div>
          <DialogFooter>
            <Button variant="outline" onClick={() => setRunTarget(null)}>{t("common.cancel")}</Button>
            <Button onClick={handleRun} disabled={running || runSelected.size === 0}>
              {running ? <Spinner size="sm" /> : <><Play className="size-4" /> {t("macros.run")}</>}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* ── Run log detail ────────────────────────────────────────── */}
      <Dialog open={!!detailRun} onOpenChange={(open) => { if (!open) setDetailRun(null); }}>
        <DialogContent size="xl" className="max-h-[80vh] overflow-y-auto">
          <DialogHeader>
            <DialogTitle>
              {detailRun?.macro_name} · <span className="font-mono text-sm">{detailRun?.agent_id}</span>
            </DialogTitle>
          </DialogHeader>
          <div className="space-y-2">
            {(detailRun ? runLog(detailRun.log) : []).length === 0 ? (
              <p className="text-xs text-muted-foreground text-center py-6">{t("macros.log_empty")}</p>
            ) : (detailRun ? runLog(detailRun.log) : []).map((entry, i) => (
              <div key={i} className="rounded-lg border border-border p-2.5">
                <div className="flex items-center gap-2 mb-1">
                  <Badge variant={
                    entry.status === "ok" || entry.status === "sent" ? "success"
                      : entry.status === "skipped" ? "secondary"
                        : "destructive"
                  } className="text-micro font-mono">
                    {entry.status}
                  </Badge>
                  <span className="text-xs font-semibold">#{entry.step}</span>
                  <code className="text-xs text-muted-foreground truncate flex-1">{entry.command}</code>
                </div>
                {entry.output && (
                  <pre className="text-xs font-mono bg-muted rounded p-2 mt-1 whitespace-pre-wrap break-all max-h-32 overflow-y-auto">{entry.output}</pre>
                )}
                {entry.error && <p className="text-xs text-destructive-ink mt-1">{entry.error}</p>}
              </div>
            ))}
          </div>
        </DialogContent>
      </Dialog>
    </PageContainer>
  );
}
