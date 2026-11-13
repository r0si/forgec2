import { registerFragment } from "@/lib/i18n";
import { enAI } from "@/lib/i18n/fragments/en.ai";
import { enTasks } from "@/lib/i18n/fragments/en.tasks";
registerFragment(enAI);
registerFragment(enTasks);

import { PageContainer } from "@/components/ui/page-container";
import { AgentLoadError } from "@/components/AgentLoadError";
import { PageToolbar } from "@/components/ui/page-toolbar";
import { ErrorState } from "@/components/ui/error-state";

import { Suspense, useState, useCallback, useEffect, memo, useMemo, useRef, createElement, type SyntheticEvent } from "react";
import { useSearchParams } from "react-router-dom";
import { api } from "@/lib/api";
import { downloadText } from "@/lib/download";
import { toast } from "sonner";
import { useI18n } from "@/lib/i18n";
import { Pagination } from "@/components/ui/pagination";
import { Spinner } from "@/components/ui/spinner";
import { StatusBadge } from "@/components/ui/status-indicator";
import { Badge } from "@/components/ui/badge";
import { DataState } from "@/components/ui/data-state";
import { Checkbox } from "@/components/ui/checkbox";
import OperatorBadge from "@/components/OperatorBadge";
import { useAppStore } from "@/lib/store";
import { paths } from "@/lib/api-paths";
import { firstArray, firstNumber } from "@/lib/envelope";
import { errorText } from "@/lib/errors";
import { csvCell } from "@/lib/csv";
import { enumLabel, formatTime } from "@/lib/utils";
import { useVirtualWindow } from "@/lib/hooks/useVirtualWindow";
import { useVisibleInterval } from "@/lib/hooks/useVisibleInterval";
import { useDebounce } from "@/lib/hooks/useDebounce";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { POLL } from "@/lib/polling";
import { useAgentList } from "@/lib/hooks/useAgentList";
import { useWS } from "@/lib/wsContext";
import { SearchInput } from "@/components/SearchInput";
import SavedViewPicker from "@/components/SavedViewPicker";
import { CopyButton } from "@/components/ui/copy-button";
import { Card } from "@/components/ui/card";
import { Button } from "@/components/ui/button";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { Dialog, DialogContent, DialogHeader } from "@/components/ui/dialog";
import { Skeleton } from "@/components/ui/skeleton";
import { Ban, Check, ChevronDown, ChevronUp, Download, FileSpreadsheet, Hand, Inbox, Maximize2, RotateCw, X, ArrowUpDown, ArrowUp, ArrowDown } from "lucide-react";
import type { Task } from "@/types/task";
import { getTaskRenderer } from "@/lib/taskRenderers";
import AIAnalysisButton from "./components/AIAnalysisPanel";
import NLQueryDialog from "./components/NLQueryDialog";

type SortKey = "created_at" | "type" | "command" | "status";

/** Tailwind sm = 640px; cards replace the table below that breakpoint. */
const NARROW_MQ = "(max-width: 639px)";

function useIsNarrow() {
  const [narrow, setNarrow] = useState(
    () => typeof window !== "undefined" && window.matchMedia(NARROW_MQ).matches,
  );
  useEffect(() => {
    const mq = window.matchMedia(NARROW_MQ);
    const onChange = () => setNarrow(mq.matches);
    onChange();
    mq.addEventListener("change", onChange);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

/** True when a click bubbled up from a checkbox (Base UI re-dispatches on a sibling input). */
function isCheckboxOrigin(e: SyntheticEvent): boolean {
  const target = e.target as HTMLElement | null;
  return !!target?.closest?.('[role="checkbox"], input[type="checkbox"]');
}

function TasksPage({ embedded = false }: { embedded?: boolean }) {
  const { t } = useI18n();
  const [searchParams] = useSearchParams();
  const [tasks, setTasks] = useState<Task[]>([]);
  const { agents, error: agentListError } = useAgentList();
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState("");
  const [agentFilter, setAgentFilter] = useState(searchParams.get("agent_id") || "");
  const [typeFilter, setTypeFilter] = useState("");
  const [searchInput, setSearchInput] = useState("");
  const searchQuery = useDebounce(searchInput, 300);
  const [claimedFilter, setClaimedFilter] = useState("");
  const [selectedIds, setSelectedIds] = useState<Set<number>>(new Set());
  const [expandedRows, setExpandedRows] = useState<Set<number>>(new Set());
  const [detailTask, setDetailTask] = useState<Task | null>(null);
  const [sortKey, setSortKey] = useState<SortKey>("created_at");
  const [sortDir, setSortDir] = useState<"asc" | "desc">("desc");
  const isNarrow = useIsNarrow();

  const toggleSort = useCallback((key: SortKey) => {
    if (sortKey === key) setSortDir((d) => (d === "asc" ? "desc" : "asc"));
    else { setSortKey(key); setSortDir(key === "created_at" ? "desc" : "asc"); }
  }, [sortKey]);

  const sortIcon = (col: SortKey) => {
    if (sortKey !== col) return <ArrowUpDown className="size-3 inline-block ml-1 text-muted-foreground" />;
    return sortDir === "asc"
      ? <ArrowUp className="size-3 inline-block ml-1 text-primary" />
      : <ArrowDown className="size-3 inline-block ml-1 text-primary" />;
  };

  const handleSortKeyDown = useCallback(
    (field: SortKey) => (e: React.KeyboardEvent) => {
      if (e.key === "Enter" || e.key === " ") {
        e.preventDefault();
        toggleSort(field);
      }
    },
    [toggleSort],
  );

  const getAgentName = useCallback((agentId: string) => {
    return (agents || []).find((a) => a.id === agentId)?.hostname;
  }, [agents]);

  const loadTasksAbortRef = useRef<AbortController | null>(null);

  const loadTasks = useCallback((background = false) => {
    loadTasksAbortRef.current?.abort();
    const ac = new AbortController();
    loadTasksAbortRef.current = ac;
    if (!background) {
      setLoading(true);
      setError(null);
    }
    const params = new URLSearchParams({ page: String(page), pageSize: "50" });
    if (statusFilter) params.set("status", statusFilter);
    if (agentFilter) params.set("agent", agentFilter);
    if (typeFilter) params.set("type", typeFilter);
    if (searchQuery) params.set("q", searchQuery);
    if (claimedFilter) params.set("claimed_by", claimedFilter);
    api.get(paths.tasks.list(params.toString()), { signal: ac.signal })
      .then((data: Record<string, unknown>) => {
        if (ac.signal.aborted) return;
        const list = firstArray(data, ["tasks", "data", "Tasks"]) as Task[];
        setTasks(list);
        setTotal(firstNumber(data, ["total", "Total"], 0));
        // Drop selections that scrolled off / no longer exist after a reload.
        setSelectedIds((prev) => {
          if (prev.size === 0) return prev;
          const live = new Set(list.map((tk) => tk.id));
          const next = new Set([...prev].filter((id) => live.has(id)));
          return next.size === prev.size ? prev : next;
        });
      })
      .catch((e) => {
        if (ac.signal.aborted) return;
        if (background) {
          // Keep existing data on background poll failure �?don't blank the table or spam toasts.
          return;
        }
        setTasks([]);
        setTotal(0);
        const msg = errorText(t, e, () => t("tasks.toast_load_failed"));
        setError(msg);
        toast.error(msg);
      })
      .finally(() => {
        if (!ac.signal.aborted) setLoading(false);
      });
  }, [page, statusFilter, agentFilter, typeFilter, searchQuery, claimedFilter, t]);

  useEffect(() => {
    loadTasks();
    // Cancel the active request when filters change or the page unmounts so
    // a late response cannot update a stale task list.
    return () => loadTasksAbortRef.current?.abort();
  }, [loadTasks]);

  const loadTasksRef = useRef(loadTasks);
  loadTasksRef.current = loadTasks;
  const pollTasks = useCallback(() => { loadTasksRef.current(true); }, []);

  const { subscribe, connected } = useWS();
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | null = null;
    const unsub = subscribe((msg) => {
      if (msg.type === "task_update" || msg.type === "task_created" || msg.type === "task_deleted") {
        if (timer) clearTimeout(timer);
        timer = setTimeout(() => { loadTasksRef.current(true); }, 500);
      }
    });
    return () => {
      if (timer) clearTimeout(timer);
      unsub();
    };
  }, [subscribe]);

  // The task_update / task_created / task_deleted WS events above already
  // refresh the grid live; the tight poll only earns its keep while the
  // socket is down, so connected sessions fall back to the drift cadence.
  useVisibleInterval(pollTasks, connected ? POLL.wsDrift : POLL.tasks);

  const { confirm, modal: confirmModal } = useConfirm();
  // In-flight mutation guard: without it a double-click fires cancel/rerun
  // twice, and the row buttons give no busy signal while the request flies.
  const [mutatingIds, setMutatingIds] = useState<Set<number>>(new Set());
  const markMutating = useCallback((id: number, on: boolean) => {
    setMutatingIds((prev) => {
      const next = new Set(prev);
      if (on) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);
  // Prefer the server's message so the operator can diagnose (409 locked by
  // whom, 404, validation) instead of a bare "failed" toast. The fallback is
  // translated at the call site so check:i18n still sees the t("…") key.
  const mutationError = useCallback((e: unknown, fallback: string) => {
    // Normalize first: a status-carrying rejection maps to the shared copy,
    // and the server's own message stays preferred for 409/404/validation.
    toast.error(errorText(t, e, () => fallback));
  }, [t]);

  const handleExportCSV = () => {
    const headers = ["Time", "Agent", "Type", "Command", "Status", "Result", "Duration"].map(csvCell);
    const rows = tasks.map((t) => [
      t.created_at || "",
      getAgentName(t.agent_id) || t.agent_id || String(t.id).substring(0, 8) || "",
      t.type || "",
      t.command || "",
      t.status || "",
      (t.result || "").replace(/\n/g, " "),
      calcDuration(t.created_at, t.updated_at),
    ].map(csvCell));
    const csv = [headers.join(","), ...rows.map((r) => r.join(","))].join("\n");
    downloadText(csv, `tasks_export_${Date.now()}.csv`, "text/csv");
  };

  const toggleRow = useCallback((id: number) => {
    setExpandedRows((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const handleCancel = useCallback(async (task: Task) => {
    if (mutatingIds.has(task.id)) return;
    markMutating(task.id, true);
    try {
      await api.post(paths.agents.cancelTask(task.agent_id, task.id));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_cancel_failed")); }
    finally { markMutating(task.id, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const handleRerun = useCallback(async (task: Task) => {
    if (mutatingIds.has(task.id)) return;
    markMutating(task.id, true);
    try {
      await api.post(paths.agents.rerunTask(task.agent_id, task.id));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_rerun_failed")); }
    finally { markMutating(task.id, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const handleApprove = useCallback(async (task: Task) => {
    if (mutatingIds.has(task.id)) return;
    markMutating(task.id, true);
    try {
      await api.post(paths.tasksCollab.approve(task.id));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_approve_failed")); }
    finally { markMutating(task.id, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const handleReject = useCallback(async (task: Task) => {
    if (mutatingIds.has(task.id)) return;
    markMutating(task.id, true);
    try {
      await api.post(paths.tasksCollab.reject(task.id));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_reject_failed")); }
    finally { markMutating(task.id, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const handleClaim = useCallback(async (taskId: number) => {
    if (mutatingIds.has(taskId)) return;
    markMutating(taskId, true);
    try {
      await api.post(paths.tasksCollab.claim(taskId));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_claim_failed")); }
    finally { markMutating(taskId, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const handleRelease = useCallback(async (taskId: number) => {
    if (mutatingIds.has(taskId)) return;
    markMutating(taskId, true);
    try {
      await api.post(paths.tasksCollab.release(taskId));
      loadTasks();
    } catch (e) { mutationError(e, t("tasks.toast_release_failed")); }
    finally { markMutating(taskId, false); }
  }, [loadTasks, markMutating, mutatingIds, mutationError, t]);

  const toggleSelect = useCallback((id: number, checked: boolean) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(id);
      else next.delete(id);
      return next;
    });
  }, []);

  const allOnPageSelected = tasks.length > 0 && tasks.every((tk) => selectedIds.has(tk.id));
  const toggleSelectAll = useCallback((checked: boolean) => {
    setSelectedIds(checked ? new Set(tasks.map((tk) => tk.id)) : new Set());
  }, [tasks]);

  const clearSelection = useCallback(() => setSelectedIds(new Set()), []);

  /** Cancel every selected still-cancellable task; failures are aggregated. */
  const handleBulkCancel = useCallback(async () => {
    const targets = tasks.filter(
      (tk) => selectedIds.has(tk.id)
        && (tk.status === "pending" || tk.status === "running" || tk.status === "sent" || tk.status === "pending_approval"),
    );
    if (targets.length === 0) {
      toast.info(t("tasks.bulk_no_cancellable"));
      return;
    }
    // Bulk cancel destroys queued work irreversibly: confirm with the count.
    if (!(await confirm({ message: t("tasks.bulk_cancel_confirm", { count: targets.length }), danger: true }))) return;
    let ok = 0;
    let fail = 0;
    await Promise.all(targets.map((tk) =>
      api.post(paths.agents.cancelTask(tk.agent_id, tk.id))
        .then(() => { ok += 1; })
        .catch(() => { fail += 1; }),
    ));
    if (fail > 0) toast.error(t("tasks.bulk_cancel_partial", { ok, fail }));
    else toast.success(t("tasks.bulk_cancel_done", { ok }));
    clearSelection();
    loadTasks();
  }, [tasks, selectedIds, loadTasks, clearSelection, confirm, t]);

  /** Rerun every selected terminal task (completed/failed/cancelled). */
  const handleBulkRerun = useCallback(async () => {
    const targets = tasks.filter(
      (tk) => selectedIds.has(tk.id)
        && (tk.status === "completed" || tk.status === "failed" || tk.status === "cancelled"),
    );
    if (targets.length === 0) {
      toast.info(t("tasks.bulk_no_rerunnable"));
      return;
    }
    if (!(await confirm({ message: t("tasks.bulk_rerun_confirm", { count: targets.length }), danger: true }))) return;
    let ok = 0;
    let fail = 0;
    await Promise.all(targets.map((tk) =>
      api.post(paths.agents.rerunTask(tk.agent_id, tk.id))
        .then(() => { ok += 1; })
        .catch(() => { fail += 1; }),
    ));
    if (fail > 0) toast.error(t("tasks.bulk_rerun_partial", { ok, fail }));
    else toast.success(t("tasks.bulk_rerun_done", { ok }));
    clearSelection();
    loadTasks();
  }, [tasks, selectedIds, loadTasks, clearSelection, confirm, t]);

  const applySavedState = useCallback((state: Record<string, unknown>) => {
    if (typeof state.status === "string") setStatusFilter(state.status);
    if (typeof state.agent === "string") setAgentFilter(state.agent);
    if (typeof state.type === "string") setTypeFilter(state.type);
    if (typeof state.q === "string") setSearchInput(state.q);
    if (typeof state.claimed === "string") setClaimedFilter(state.claimed);
    if (typeof state.sortKey === "string" && (state.sortKey === "created_at" || state.sortKey === "type" || state.sortKey === "command" || state.sortKey === "status")) {
      setSortKey(state.sortKey);
    }
    if (state.sortDir === "asc" || state.sortDir === "desc") setSortDir(state.sortDir);
    setPage(1);
  }, []);

  const getSavedState = useCallback(() => ({
    status: statusFilter,
    agent: agentFilter,
    type: typeFilter,
    q: searchInput,
    claimed: claimedFilter,
    sortKey,
    sortDir,
  }), [statusFilter, agentFilter, typeFilter, searchInput, claimedFilter, sortKey, sortDir]);

  const TASK_ROW_H = 52;
  const {
    scrollRef,
    onScroll,
    virtualized,
    start: virtStart,
    end: virtEnd,
    offsetTop,
    totalHeight,
  } = useVirtualWindow({ count: tasks.length, rowHeight: TASK_ROW_H, threshold: 25 });
  // Windowing assumes fixed-height table rows; expanded detail rows and the
  // variable-height mobile card list both break scrollTop→index math.
  const windowingActive = !isNarrow && virtualized && expandedRows.size === 0;

  const sortedTasks = useMemo(() => {
    const list = [...tasks];
    const dir = sortDir === "asc" ? 1 : -1;
    list.sort((a, b) => {
      if (sortKey === "created_at") {
        const at = a.created_at ? new Date(a.created_at).getTime() : 0;
        const bt = b.created_at ? new Date(b.created_at).getTime() : 0;
        return (at - bt) * dir;
      }
      const av = String(a[sortKey as keyof Task] || "");
      const bv = String(b[sortKey as keyof Task] || "");
      return av.localeCompare(bv) * dir;
    });
    return list;
  }, [tasks, sortKey, sortDir]);

  const visibleTasks = useMemo(
    () => (windowingActive ? sortedTasks.slice(virtStart, virtEnd) : sortedTasks),
    [sortedTasks, windowingActive, virtStart, virtEnd],
  );

  const getStatusBadge = useCallback((status: string): React.ReactNode => {
    return <StatusBadge status={status} />;
  }, []);

  const getTypeBadge = useCallback((type: string): React.ReactNode => {
    const variantMap: Record<string, "warning" | "info" | "destructive" | "secondary"> = {
      shell: "warning",
      screenshot: "info",
      kill: "destructive",
    };
    return <Badge variant={variantMap[type] || "secondary"}>{type}</Badge>;
  }, []);

  return (
    <PageContainer embedded={embedded} title={!embedded ? t("tasks.title") : undefined} subtitle={!embedded ? `${t("tasks.subtitle_prefix")} \u00b7 ${total} total` : undefined} actions={<>
        <NLQueryDialog />
        <Button onClick={handleExportCSV} variant="secondary">
          <FileSpreadsheet className="size-4" /> {t("tasks.export_csv")}
        </Button>
      </>}>
      {embedded && (
        <div className="mb-4 flex justify-end">
          <Button onClick={handleExportCSV} variant="secondary" size="sm">
            <FileSpreadsheet className="size-4" /> {t("tasks.export_csv")}
          </Button>
        </div>
      )}

      {/* Embedded in the Events page, whose tab rail is already sticky: a
          second sticky layer would slide under the rail. */}
      <PageToolbar sticky={!embedded}>
        <div className="flex flex-col sm:flex-row gap-3 sm:flex-wrap sm:items-center">
          <SearchInput
            id="task-search"
            value={searchInput}
            onChange={setSearchInput}
            onClear={() => { setSearchInput(""); setPage(1); }}
            placeholder={t("tasks.search_placeholder")}
            className="w-full sm:w-56"
            label={t("tasks.search_placeholder")}
          />
          <Select value={statusFilter || "all"} onValueChange={(val) => { setStatusFilter(val === "all" ? "" : val ?? ""); setPage(1); }}>
            <SelectTrigger aria-label={t("tasks.status_filter")}>
              <SelectValue>
                {(value) => {
                  if (value === "all") return t("tasks.all_status");
                  if (value === "pending_approval") return t("tasks.approval_pending");
                  return enumLabel(t, "tasks", String(value ?? ""));
                }}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("tasks.all_status")}</SelectItem>
              <SelectItem value="pending_approval">{t("tasks.approval_pending")}</SelectItem>
              <SelectItem value="completed">{t("tasks.completed")}</SelectItem>
              <SelectItem value="pending">{t("tasks.pending")}</SelectItem>
              <SelectItem value="failed">{t("tasks.failed")}</SelectItem>
              <SelectItem value="cancelled">{t("tasks.cancelled")}</SelectItem>
            </SelectContent>
          </Select>
          <Select value={agentFilter || "all"} onValueChange={(val) => { setAgentFilter(val === "all" ? "" : val ?? ""); setPage(1); }}>
            <SelectTrigger aria-label={t("tasks.agent_filter")} className="min-w-[150px]">
              <SelectValue>
                {(value) => (value === "all" ? t("tasks.all_agents") : String(value ?? ""))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("tasks.all_agents")}</SelectItem>
              {(agents || []).filter((a) => a.id).map((a) => (
                <SelectItem key={a.id} value={a.id!}>{a.hostname || a.id!.substring(0, 8)}</SelectItem>
              ))}
            </SelectContent>
          </Select>
          <AgentLoadError message={agentListError} />
          <Select value={typeFilter || "all"} onValueChange={(val) => { setTypeFilter(val === "all" ? "" : val ?? ""); setPage(1); }}>
            <SelectTrigger aria-label={t("tasks.type_filter")}>
              <SelectValue>
                {(value) => (value === "all" ? t("tasks.all_types") : String(value ?? ""))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("tasks.all_types")}</SelectItem>
              <SelectItem value="shell">shell</SelectItem>
              <SelectItem value="screenshot">screenshot</SelectItem>
              <SelectItem value="file">file</SelectItem>
              <SelectItem value="download">download</SelectItem>
              <SelectItem value="upload">upload</SelectItem>
              <SelectItem value="ps">ps</SelectItem>
              <SelectItem value="creds">creds</SelectItem>
            </SelectContent>
          </Select>
          <Select value={claimedFilter || "all"} onValueChange={(val) => { setClaimedFilter(val === "all" ? "" : val ?? ""); setPage(1); }}>
            <SelectTrigger aria-label={t("tasks.claimed_filter")} className="min-w-[140px]">
              <SelectValue>
                {(value) => (value === "all" ? t("tasks.claimed_all") : t("tasks.claimed_mine"))}
              </SelectValue>
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("tasks.claimed_all")}</SelectItem>
              <SelectItem value="me">{t("tasks.claimed_mine")}</SelectItem>
            </SelectContent>
          </Select>
          <SavedViewPicker page="tasks" getState={getSavedState} applyState={applySavedState} />
        </div>
        {selectedIds.size > 0 && (
          <div className="flex flex-wrap items-center gap-2 rounded-lg border border-primary/30 bg-primary/5 px-3 py-2">
            <span className="text-xs font-medium text-primary">{t("tasks.selected_count", { n: selectedIds.size })}</span>
            <Button size="sm" variant="secondary" onClick={() => void handleBulkCancel()}>
              <Ban className="size-3.5" /> {t("tasks.bulk_cancel")}
            </Button>
            <Button size="sm" variant="secondary" onClick={() => void handleBulkRerun()}>
              <RotateCw className="size-3.5" /> {t("tasks.bulk_rerun")}
            </Button>
            <Button size="sm" variant="ghost" onClick={clearSelection}>
              <X className="size-3.5" /> {t("tasks.clear_selection")}
            </Button>
          </div>
        )}
      </PageToolbar>

      <Card className="overflow-hidden">
        <DataState
          loading={loading}
          error={error}
          empty={!loading && !error && tasks.length === 0}
          emptyIcon={Inbox}
          emptyTitle={t("tasks.empty_title")}
          emptyMessage={t("tasks.empty_message")}
          onRetry={loadTasks}
          loadingSkeleton={
            <div className="overflow-x-auto p-4 space-y-3">
              {Array.from({ length: 5 }).map((_, i) => (
                <Skeleton key={i} className="h-8 w-full" />
              ))}
            </div>
          }
        >
          <div
            ref={scrollRef}
            onScroll={onScroll}
            role={isNarrow ? undefined : "region"}
            aria-label={isNarrow ? undefined : t("tasks.title")}
            tabIndex={isNarrow ? undefined : 0}
            className={
              isNarrow
                ? ""
                : windowingActive
                  ? "content-auto overscroll-contain overflow-auto max-h-[min(70vh,720px)]"
                  : "content-auto overscroll-contain overflow-x-auto"
            }
          >
          {isNarrow ? (
            <div className="divide-y divide-border" data-testid="tasks-mobile-cards">
              <div className="flex items-center justify-between gap-2 px-3 py-2 bg-muted/40 border-b border-border">
                <span className="text-xs text-muted-foreground">
                  {selectedIds.size > 0 ? t("tasks.selected_count", { n: selectedIds.size }) : t("tasks.select_all")}
                </span>
                <Checkbox
                  aria-label={t("tasks.select_all")}
                  checked={allOnPageSelected}
                  indeterminate={selectedIds.size > 0 && !allOnPageSelected}
                  onCheckedChange={(v) => toggleSelectAll(v !== false)}
                />
              </div>
              {sortedTasks.map((task) => (
                <TaskCard
                  key={task.id}
                  task={task}
                  expanded={expandedRows.has(task.id)}
                  selected={selectedIds.has(task.id)}
                  onToggleSelect={toggleSelect}
                  onToggle={toggleRow}
                  onDetail={setDetailTask}
                  onCancel={handleCancel}
                  onRerun={handleRerun}
                  onApprove={handleApprove}
                  onReject={handleReject}
                  onClaim={handleClaim}
                  onRelease={handleRelease}
                  disabledIds={mutatingIds}
                  getAgentName={getAgentName}
                  getTypeBadge={getTypeBadge}
                  getStatusBadge={getStatusBadge}
                />
              ))}
            </div>
          ) : (
          <Table className="text-sm">
            <TableHeader className="sticky top-0 z-10 bg-card/95 backdrop-blur supports-[backdrop-filter]:bg-card/90 border-b border-border">
              <TableRow>
                <TableHead className="w-10 py-3 px-3">
                  <Checkbox
                    aria-label={t("tasks.select_all")}
                    checked={allOnPageSelected}
                    indeterminate={selectedIds.size > 0 && !allOnPageSelected}
                    onCheckedChange={(v) => toggleSelectAll(v !== false)}
                  />
                </TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[140px] cursor-pointer select-none" tabIndex={0} role="columnheader" aria-sort={sortKey === "created_at" ? (sortDir === "asc" ? "ascending" : "descending") : "none"} onClick={() => toggleSort("created_at")} onKeyDown={handleSortKeyDown("created_at")}>
                  {t("tasks.col_time")} {sortIcon("created_at")}
                </TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[120px]">{t("tasks.col_agent")}</TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[100px] cursor-pointer select-none" tabIndex={0} role="columnheader" aria-sort={sortKey === "type" ? (sortDir === "asc" ? "ascending" : "descending") : "none"} onClick={() => toggleSort("type")} onKeyDown={handleSortKeyDown("type")}>
                  {t("tasks.col_type")} {sortIcon("type")}
                </TableHead>
                <TableHead className="text-left py-3 px-4 font-normal min-w-[180px] cursor-pointer select-none" tabIndex={0} role="columnheader" aria-sort={sortKey === "command" ? (sortDir === "asc" ? "ascending" : "descending") : "none"} onClick={() => toggleSort("command")} onKeyDown={handleSortKeyDown("command")}>
                  {t("tasks.col_command")} {sortIcon("command")}
                </TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[200px]">{t("tasks.col_result")}</TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[80px]">{t("tasks.col_claimed_by")}</TableHead>
                <TableHead className="max-sm:hidden text-left py-3 px-4 font-normal min-w-[90px]">{t("tasks.col_duration")}</TableHead>
                <TableHead className="text-center py-3 px-4 font-normal min-w-[90px] cursor-pointer select-none" tabIndex={0} role="columnheader" aria-sort={sortKey === "status" ? (sortDir === "asc" ? "ascending" : "descending") : "none"} onClick={() => toggleSort("status")} onKeyDown={handleSortKeyDown("status")}>
                  {t("tasks.col_status")} {sortIcon("status")}
                </TableHead>
                <TableHead className="text-center py-3 px-4 font-normal min-w-[160px]">{t("tasks.col_actions")}</TableHead>
              </TableRow>
            </TableHeader>
            <TableBody className="divide-y divide-border">
              {windowingActive && offsetTop > 0 && (
                <TableRow aria-hidden className="hover:bg-transparent">
                  <TableCell colSpan={10} style={{ height: offsetTop, padding: 0, border: 0 }} />
                </TableRow>
              )}
              {visibleTasks.map((task) => (
                <TaskRow
                  key={task.id}
                  task={task}
                  expanded={expandedRows.has(task.id)}
                  selected={selectedIds.has(task.id)}
                  onToggleSelect={toggleSelect}
                  onToggle={toggleRow}
                  onDetail={setDetailTask}
                  onCancel={handleCancel}
                  onRerun={handleRerun}
                  onApprove={handleApprove}
                  onReject={handleReject}
                  onClaim={handleClaim}
                  onRelease={handleRelease}
                  disabledIds={mutatingIds}
                  getAgentName={getAgentName}
                  getTypeBadge={getTypeBadge}
                  getStatusBadge={getStatusBadge}
                />
              ))}
              {windowingActive && totalHeight - offsetTop - visibleTasks.length * TASK_ROW_H > 0 && (
                <TableRow aria-hidden className="hover:bg-transparent">
                  <TableCell colSpan={10} style={{ height: totalHeight - offsetTop - visibleTasks.length * TASK_ROW_H, padding: 0, border: 0 }} />
                </TableRow>
              )}
            </TableBody>
          </Table>
          )}
          </div>
        </DataState>
      </Card>

      <Pagination page={page} pageSize={50} total={total} onPageChange={setPage} />

      {detailTask && <TaskDetailModal task={detailTask} onClose={() => setDetailTask(null)} getAgentName={getAgentName} getStatusBadge={getStatusBadge} getTypeBadge={getTypeBadge} />}
      {confirmModal}
    </PageContainer>
  );
}

export default function TasksPageWrapper({ embedded = false }: { embedded?: boolean }) {
  const { t } = useI18n();
  return (
    <Suspense fallback={<div className="flex items-center justify-center py-20 text-muted-foreground"><Spinner size="sm" /> {t("tasks.loading")}</div>}>
      <TasksPage embedded={embedded} />
    </Suspense>
  );
}

const TaskRow = memo(function TaskRow({ task, expanded, selected, onToggleSelect, onToggle, onDetail, onCancel, onRerun, onApprove, onReject, onClaim, onRelease, disabledIds, getAgentName, getTypeBadge, getStatusBadge }: {
  task: Task;
  expanded: boolean;
  selected: boolean;
  onToggleSelect: (id: number, checked: boolean) => void;
  onToggle: (id: number) => void;
  onDetail: (task: Task) => void;
  onCancel: (task: Task) => void;
  onRerun: (task: Task) => void;
  onApprove: (task: Task) => void;
  onReject: (task: Task) => void;
  onClaim: (taskId: number) => void;
  onRelease: (taskId: number) => void;
  disabledIds: Set<number>;
  getAgentName: (id: string) => string | undefined;
  getTypeBadge: (t: string) => React.ReactNode;
  getStatusBadge: (s: string) => React.ReactNode;
}) {
  const currentUsername = useAppStore((s) => s.currentUsername);
  const { t } = useI18n();
  return (
    <>
      <TableRow
        className="hover:bg-muted/50 transition-colors cursor-pointer"
        onClick={() => onDetail(task)}
        aria-selected={selected}
      >
        <TableCell className="py-3 px-3" onClick={(e) => e.stopPropagation()}>
          <Checkbox
            aria-label={t("tasks.select_item")}
            checked={selected}
            onCheckedChange={(v) => onToggleSelect(task.id, v === true)}
          />
        </TableCell>
        <TableCell className="max-sm:hidden py-3 px-4 font-mono text-xs text-muted-foreground whitespace-nowrap">{task.created_at ? formatTime(task.created_at) : "-"}</TableCell>
        <TableCell className="max-sm:hidden py-3 px-4"><div className="font-medium text-foreground text-sm">{getAgentName(task.agent_id) || task.agent_id?.substring(0, 8)}</div></TableCell>
        <TableCell className="max-sm:hidden py-3 px-4">{getTypeBadge(task.type)}</TableCell>
        <TableCell className="py-3 px-4 font-mono text-xs text-muted-foreground max-w-xs truncate">
          <Button
            variant="link"
            size="sm"
            onClick={(e) => { e.stopPropagation(); onDetail(task); }}
            className="font-mono text-xs text-muted-foreground hover:text-foreground p-0 h-auto justify-start max-w-full truncate"
          >
            {task.command || "-"}
          </Button>
        </TableCell>
        <TableCell className="max-sm:hidden py-3 px-4 max-w-sm">
          {task.result ? (
            <Button
              variant="link"
              size="sm"
              onClick={(e) => { e.stopPropagation(); onToggle(task.id); }}
              aria-expanded={expanded}
              aria-label={expanded ? t("tasks.collapse") : t("tasks.expand")}
              className="text-xs text-primary hover:underline flex items-center gap-1 p-0 h-auto justify-start"
            >
              {expanded ? <ChevronUp className="size-2.5" /> : <ChevronDown className="size-2.5" />} {t("tasks.expand")}
            </Button>
          ) : "-"}
        </TableCell>
        <TableCell className="max-sm:hidden py-3 px-4">
          {task.operator_claimed_by ? (
            <OperatorBadge username={task.operator_claimed_by} isCurrentUser={task.operator_claimed_by === currentUsername} size="sm" />
          ) : (
            <span className="text-muted-foreground text-xs">-</span>
          )}
        </TableCell>
        <TableCell className="max-sm:hidden py-3 px-4 text-xs text-muted-foreground font-mono">{calcDuration(task.created_at, task.updated_at)}</TableCell>
        <TableCell className="py-3 px-4 text-center">{getStatusBadge(task.status)}{task.late_result && <Badge variant="warning" className="ml-1 px-1.5 py-0 text-micro-sm">{t("tasks.late_result_badge")}</Badge>}</TableCell>
        <TableCell className="py-3 px-4 text-center" onClick={(e) => e.stopPropagation()}>
          <TaskActions
            task={task}
            onCancel={onCancel}
            onRerun={onRerun}
            onApprove={onApprove}
            onReject={onReject}
            onClaim={onClaim}
            onRelease={onRelease}
            disabledIds={disabledIds}
          />
        </TableCell>
      </TableRow>
      {expanded && task.result && (
        <TableRow>
          <TableCell colSpan={10} className="px-4 py-3 bg-card">
            <ExpandedResult task={task} onDetail={onDetail} />
          </TableCell>
        </TableRow>
      )}
    </>
  );
});

type TaskCardProps = {
  task: Task;
  expanded: boolean;
  selected: boolean;
  onToggleSelect: (id: number, checked: boolean) => void;
  onToggle: (id: number) => void;
  onDetail: (task: Task) => void;
  onCancel: (task: Task) => void;
  onRerun: (task: Task) => void;
  onApprove: (task: Task) => void;
  onReject: (task: Task) => void;
  onClaim: (taskId: number) => void;
  onRelease: (taskId: number) => void;
  disabledIds: Set<number>;
  getAgentName: (id: string) => string | undefined;
  getTypeBadge: (t: string) => React.ReactNode;
  getStatusBadge: (s: string) => React.ReactNode;
};

/** Narrow-screen list item: keeps every column the desktop table shows. */
const TaskCard = memo(function TaskCard({ task, expanded, selected, onToggleSelect, onToggle, onDetail, onCancel, onRerun, onApprove, onReject, onClaim, onRelease, disabledIds, getAgentName, getTypeBadge, getStatusBadge }: TaskCardProps) {
  const currentUsername = useAppStore((s) => s.currentUsername);
  const { t } = useI18n();
  return (
    <article
      role="button"
      tabIndex={0}
      aria-label={`${getAgentName(task.agent_id) || task.agent_id || ""} ${task.type} ${task.status}`}
      onClick={(e) => { if (isCheckboxOrigin(e)) return; onDetail(task); }}
      onKeyDown={(e) => {
        if (isCheckboxOrigin(e as unknown as SyntheticEvent)) return;
        if (e.key === "Enter" || e.key === " ") { e.preventDefault(); onDetail(task); }
      }}
      className={`p-3 cursor-pointer hover:bg-muted/50 transition-colors focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-primary/70 ${selected ? "bg-primary/5" : ""}`}
      data-testid="task-card"
      aria-selected={selected}
    >
      <div className="flex items-start gap-2.5">
        <Checkbox
          aria-label={t("tasks.select_item")}
          checked={selected}
          onCheckedChange={(v) => onToggleSelect(task.id, v === true)}
          onClick={(e) => e.stopPropagation()}
          onKeyDown={(e) => e.stopPropagation()}
          className="mt-0.5 shrink-0"
        />
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-1.5">
            {getStatusBadge(task.status)}
            {task.late_result && <Badge variant="warning" className="px-1.5 py-0 text-micro-sm">{t("tasks.late_result_badge")}</Badge>}
            {getTypeBadge(task.type)}
            <span className="font-medium text-sm text-foreground truncate min-w-0">
              {getAgentName(task.agent_id) || task.agent_id?.substring(0, 8)}
            </span>
            <span className="ml-auto text-xs font-mono text-muted-foreground whitespace-nowrap shrink-0">
              {task.created_at ? formatTime(task.created_at) : "-"}
            </span>
          </div>
          <div className="mt-1.5 font-mono text-xs text-muted-foreground break-all">
            {task.command || "-"}
          </div>
          <div className="mt-1.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-xs text-muted-foreground">
            <span className="font-mono">{calcDuration(task.created_at, task.updated_at)}</span>
            {task.operator_claimed_by ? (
              <OperatorBadge username={task.operator_claimed_by} isCurrentUser={task.operator_claimed_by === currentUsername} size="sm" />
            ) : (
              <span>{t("tasks.col_claimed_by")}: -</span>
            )}
            {task.result && (
              <Button
                variant="link"
                size="sm"
                onClick={(e) => { e.stopPropagation(); onToggle(task.id); }}
                aria-expanded={expanded}
                aria-label={expanded ? t("tasks.collapse") : t("tasks.expand")}
                className="text-xs text-primary hover:underline flex items-center gap-1 p-0 h-auto"
              >
                {expanded ? <ChevronUp className="size-2.5" /> : <ChevronDown className="size-2.5" />}
                {expanded ? t("tasks.collapse") : t("tasks.expand")}
              </Button>
            )}
          </div>
          <div className="mt-2 flex items-center justify-end" onClick={(e) => e.stopPropagation()}>
            <TaskActions
              task={task}
              onCancel={onCancel}
              onRerun={onRerun}
              onApprove={onApprove}
              onReject={onReject}
              onClaim={onClaim}
              onRelease={onRelease}
              disabledIds={disabledIds}
            />
          </div>
          {expanded && task.result && (
            <div className="mt-2 border-t border-border pt-2" onClick={(e) => e.stopPropagation()}>
              <ExpandedResult task={task} onDetail={onDetail} />
            </div>
          )}
        </div>
      </div>
    </article>
  );
});

/** Shared claim/cancel/rerun/approve/reject icon buttons (row + card). */
function TaskActions({ task, onCancel, onRerun, onApprove, onReject, onClaim, onRelease, disabledIds }: {
  task: Task;
  onCancel: (task: Task) => void;
  onRerun: (task: Task) => void;
  onApprove: (task: Task) => void;
  onReject: (task: Task) => void;
  onClaim: (taskId: number) => void;
  onRelease: (taskId: number) => void;
  disabledIds: Set<number>;
}) {
  const currentUsername = useAppStore((s) => s.currentUsername);
  const { t } = useI18n();
  const busy = disabledIds.has(task.id);
  return (
    <div className="flex items-center justify-end gap-1">
      {(!task.operator_claimed_by || task.operator_claimed_by === currentUsername) && (task.status === "pending" || task.status === "running") && (
        <>
          {task.operator_claimed_by !== currentUsername ? (
            <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onClaim(task.id)} className="text-muted-foreground hover:text-primary hover:bg-primary/10 dark:hover:bg-chart-3/20" title={t("tasks.claim")} aria-label={t("tasks.claim")}>
              <Hand className="size-4" />
            </Button>
          ) : (
            <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onRelease(task.id)} className="text-muted-foreground hover:text-warning-ink hover:bg-warning/15" title={t("tasks.release")} aria-label={t("tasks.release")}>
              <Hand className="size-4" />
            </Button>
          )}
        </>
      )}
      {(task.status === "pending" || task.status === "running") && !task.operator_claimed_by && (
        <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onCancel(task)} className="text-muted-foreground hover:text-destructive-ink hover:bg-destructive/10" title={t("tasks.cancel")} aria-label={t("tasks.cancel")}>
          <Ban className="size-3" />
        </Button>
      )}
      {(task.status === "completed" || task.status === "failed" || task.status === "cancelled") && (
        <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onRerun(task)} className="text-muted-foreground hover:text-primary hover:bg-primary/10 dark:hover:bg-chart-3/20" title={t("tasks.rerun")} aria-label={t("tasks.rerun")}>
          <RotateCw className="size-4" />
        </Button>
      )}
      {task.status === "pending_approval" && (
        <>
          <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onApprove(task)} className="text-muted-foreground hover:text-success-ink hover:bg-success/15" title={t("tasks.approve")} aria-label={t("tasks.approve")}>
            <Check className="size-4" />
          </Button>
          <Button variant="ghost" size="icon-xs" disabled={busy} aria-busy={busy} onClick={() => onReject(task)} className="text-muted-foreground hover:text-destructive-ink hover:bg-destructive/10" title={t("tasks.reject")} aria-label={t("tasks.reject")}>
            <X className="size-4" />
          </Button>
        </>
      )}
    </div>
  );
}

/** Inline expanded result with copy / download / full-view toolbar. */
function ExpandedResult({ task, onDetail }: { task: Task; onDetail: (task: Task) => void }) {
  const { t } = useI18n();
  if (!task.result) return null;
  return (
    <div className="relative">
      <div className="absolute top-2 right-2 flex items-center gap-1">
        <CopyButton text={task.result} label={t("tasks.detail_output")} size="xs" title={t("common.copy")} className="bg-secondary text-muted-foreground hover:text-foreground px-2 py-1 rounded" />
        <Button variant="ghost" size="xs" onClick={() => downloadText(task.result, `task_${task.id}_result.txt`)} className="text-xs text-muted-foreground hover:text-foreground bg-secondary px-2 py-1 rounded" aria-label={t("common.download")}>
          <Download className="size-3.5" />
        </Button>
        <Button variant="ghost" size="xs" onClick={() => onDetail(task)} className="text-xs text-muted-foreground hover:text-foreground bg-secondary px-2 py-1 rounded">
          <Maximize2 className="size-4" />{t("tasks.full_view")}
        </Button>
      </div>
      <TaskResultView type={task.type} result={task.result} />
      <AIAnalysisButton taskId={Number(task.id) || 0} />
    </div>
  );
}

// TaskResultView routes structured payloads through the pluggable renderer
// registry and keeps the classic text view for everything unregistered.
function TaskResultView({ type, result }: { type: string; result: string }) {
  const renderer = getTaskRenderer(type);
  if (renderer) return createElement(renderer, { result, taskType: type });
  return <pre className="text-xs text-success-ink font-mono overflow-x-auto max-h-60 p-2 whitespace-pre-wrap break-all">{result}</pre>;
}

function TaskDetailModal({ task, onClose, getAgentName, getStatusBadge, getTypeBadge }: {
  task: Task;
  onClose: () => void;
  getAgentName: (id: string) => string | undefined;
  getStatusBadge: (s: string) => React.ReactNode;
  getTypeBadge: (t: string) => React.ReactNode;
}) {
  const { t } = useI18n();
  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent size="2xl" className="max-h-[85vh] flex flex-col">
        <DialogHeader>
          <div className="flex items-center gap-3">
            {getTypeBadge(task.type)}
            {getStatusBadge(task.status)}
            <span className="text-xs font-mono text-muted-foreground">{String(task.id).substring(0, 12)}</span>
          </div>
        </DialogHeader>
        <div className="border-b border-border grid grid-cols-2 sm:grid-cols-4 gap-4 text-xs">
          <div><span className="text-muted-foreground">{t("tasks.detail_agent")}</span><p className="font-medium text-foreground mt-0.5">{getAgentName(task.agent_id) || task.agent_id?.substring(0, 8)}</p></div>
          <div><span className="text-muted-foreground">{t("tasks.detail_created")}</span><p className="font-medium text-foreground mt-0.5">{task.created_at ? formatTime(task.created_at) : "-"}</p></div>
          <div><span className="text-muted-foreground">{t("tasks.detail_duration")}</span><p className="font-medium text-foreground mt-0.5">{calcDuration(task.created_at, task.updated_at)}</p></div>
          <div><span className="text-muted-foreground">{t("tasks.detail_by")}</span><p className="font-medium text-foreground mt-0.5">{task.created_by || t("tasks.detail_system")}</p></div>
          {task.approved_by && (
            <div><span className="text-muted-foreground">{t("tasks.detail_approved_by")}</span><p className="font-medium text-success-ink mt-0.5">{task.approved_by}</p></div>
          )}
        </div>
        <div className="overflow-y-auto flex-1">
          <div className="mb-4">
            <h3 className="text-xs font-semibold text-muted-foreground mb-2 uppercase tracking-wider">{t("tasks.detail_command")}</h3>
            <code className="block bg-muted border border-border rounded-lg p-3 text-sm font-mono text-foreground">{task.command || "-"}</code>
          </div>
          {task.late_result && (
            <div className="mb-4 rounded-lg border border-warning/40 bg-warning/10 p-3" role="status">
              <p className="text-sm font-medium text-warning-ink">{t("tasks.late_result_title")}</p>
              <p className="mt-1 text-xs text-muted-foreground">{t("tasks.late_result_body")}</p>
              {task.late_result_at && <p className="mt-1 text-micro text-muted-foreground">{formatTime(task.late_result_at)}</p>}
            </div>
          )}
          {task.result && (
            <div>
              <div className="flex items-center justify-between mb-2">
                <h3 className="text-xs font-semibold text-muted-foreground uppercase tracking-wider">{t("tasks.detail_output")}</h3>
                <div className="flex items-center gap-1">
                  <CopyButton text={task.result} label={t("tasks.detail_output")} size="xs" title={t("common.copy")} className="text-muted-foreground hover:text-foreground" />
                  <Button variant="ghost" size="xs" onClick={() => downloadText(task.result, `task_${task.id}_result.txt`)} className="text-xs text-muted-foreground hover:text-foreground" aria-label={t("common.download")}>
                    <Download className="size-3.5" />
                  </Button>
                </div>
              </div>
              <div className="bg-card rounded-lg p-4 max-h-96 overflow-y-auto">
                <TaskResultView type={task.type} result={task.result} />
              </div>
              <AIAnalysisButton taskId={Number(task.id) || 0} />
            </div>
          )}
          {task.error && (
            <div className="mt-4">
              <ErrorState title={t("tasks.detail_error")} message={<pre className="text-xs font-mono whitespace-pre-wrap">{task.error}</pre>} />
            </div>
          )}
        </div>
      </DialogContent>
    </Dialog>
  );
}

function calcDuration(created?: string, updated?: string): string {
  if (!created) return "-";
  const start = new Date(created).getTime();
  if (isNaN(start)) return "-";
  const end = updated ? new Date(updated).getTime() : Date.now();
  if (isNaN(end)) return "-";
  const ms = end - start;
  if (ms < 0) return "-";
  if (ms < 1000) return `${ms}ms`;
  const sec = Math.floor(ms / 1000);
  if (sec < 60) return `${sec}s`;
  const min = Math.floor(sec / 60);
  const remSec = sec % 60;
  if (min < 60) return `${min}m ${remSec}s`;
  const hr = Math.floor(min / 60);
  return `${hr}h ${min % 60}m`;
}

