import { useCallback, useRef, useState } from "react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { errorText } from "@/lib/errors";
import { unwrapEnvelope } from "@/lib/envelope";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { downloadText } from "@/lib/download";
import type { Beacon, BulkResult } from "./types";

type TKey = (key: string, params?: Record<string, string | number>) => string;
type ConfirmState = { type: "kill" | "delete" | "uninstall" | "batch-delete" | "bulk-kill" | "bulk-uninstall"; id?: string; hostname?: string } | null;

interface BulkOpsDeps {
  t: TKey;
  selected: Set<string>;
  setSelected: (next: Set<string>) => void;
  loadBeacons: () => void;
  setActionMsg: (msg: string | null) => void;
  setConfirm: (c: ConfirmState) => void;
  // modals (from useAgentModals)
  setScreenshotConfirm: (open: boolean) => void;
  openBatchSleep: (interval: string, jitter: string) => void;
  closeBatchSleep: () => void;
  batchSleepInterval: string;
  batchSleepJitter: string;
  cmdType: string;
  cmdText: string;
  closeCmdModal: () => void;
  quickSleepAgent: { id: string; hostname: string } | null;
  sleepInterval: string;
  sleepJitter: string;
  closeQuickSleep: () => void;
  editingNotesId: string | null;
  editNotesText: string;
  closeNotesEdit: () => void;
  // active list filters (export spans all pages but respects them)
  searchQuery: string;
  statusFilter: string;
  osFilter: string;
  tagFilter: string;
  linkedFilter: string;
  sortKey: string;
  sortDir: string;
  // The window the failure counts describe; the export carries the same numbers.
  failureWindow: string;
}

/** Shared interval/jitter validation for both sleep submit paths. */
export function parseSleepInput(intervalRaw: string, jitterRaw: string): { interval: number; jitter: number } | null {
  const interval = Number(intervalRaw);
  const jitter = Number(jitterRaw);
  if (!Number.isFinite(interval) || interval < 1 || interval > 86400 || !Number.isFinite(jitter) || jitter < 0 || jitter > 100) {
    return null;
  }
  return { interval, jitter };
}

/**
 * All bulk/single destructive list actions + CSV export for the agents page.
 * Pure dispatch logic — no JSX — so the page stays a composition layer.
 */
export function useAgentBulkOps(deps: BulkOpsDeps) {
  const {
    t, selected, setSelected, loadBeacons, setActionMsg, setConfirm,
    setScreenshotConfirm, openBatchSleep, closeBatchSleep, batchSleepInterval, batchSleepJitter,
    cmdType, cmdText, closeCmdModal,
    quickSleepAgent, sleepInterval, sleepJitter, closeQuickSleep,
    editingNotesId, editNotesText, closeNotesEdit,
    searchQuery, statusFilter, osFilter, tagFilter, linkedFilter, sortKey, sortDir,
    failureWindow,
  } = deps;

  const { toastError } = useErrorToast();
  const [exporting, setExporting] = useState(false);
  const exportAbortRef = useRef<AbortController | null>(null);

  const runBatch = useCallback(async (payload: Record<string, unknown>) => {
    try {
      const data = await api.postJson<{ success?: boolean; tasks_created?: number; error?: string }>(paths.agents.batch, payload);
      if (data.success) {
        setActionMsg(t("agents.batch_result").replace("{count}", String(data.tasks_created ?? selected.size)));
        setSelected(new Set());
        loadBeacons();
      } else {
        setActionMsg(data.error || t("agents.batch_failed"));
      }
    } catch (e) {
      // The rejection carries the envelope's error text (api.postJson throws
      // an ApiError for a success:false envelope), so the banner shows the
      // reason instead of only "failed".
      setActionMsg(errorText(t, e, () => t("agents.batch_failed")));
    }
  }, [t, selected.size, setSelected, loadBeacons, setActionMsg]);

  const bulkTask = useCallback(async (agentIds: string[], type: string, command: string) => {
    try {
      const data = await api.postJson<{ success?: boolean; tasks_created?: number; failed?: number; error?: string }>(paths.agents.bulkTask, { agent_ids: agentIds, task_type: type, command });
      if (data.success) {
        let bulkMsg = t("agents.bulk_type_result").replace("{type}", type).replace("{count}", String(data.tasks_created ?? agentIds.length));
        if (data.failed) bulkMsg += ` (${data.failed} ${t("agents.failed_suffix")})`;
        setActionMsg(bulkMsg);
        setSelected(new Set());
        loadBeacons();
      } else {
        setActionMsg(data.error || t("agents.bulk_task_failed"));
      }
    } catch (e) {
      setActionMsg(errorText(t, e, () => t("agents.bulk_task_failed")));
    }
  }, [t, setSelected, loadBeacons, setActionMsg]);

  const batchScreenshot = useCallback(() => {
    if (selected.size === 0) return;
    setScreenshotConfirm(true);
  }, [selected.size, setScreenshotConfirm]);

  const doBatchScreenshot = useCallback(() => {
    setScreenshotConfirm(false);
    void runBatch({ agent_ids: Array.from(selected), task_type: "screenshot" });
  }, [setScreenshotConfirm, runBatch, selected]);

  const batchSleep = useCallback(() => {
    if (selected.size === 0) return;
    openBatchSleep("30", "20");
  }, [selected.size, openBatchSleep]);

  const doBatchSleep = useCallback(() => {
    const ids = Array.from(selected);
    if (!ids.length) return;
    const parsed = parseSleepInput(batchSleepInterval, batchSleepJitter);
    if (!parsed) {
      setActionMsg(t("agents.sleep_invalid"));
      return;
    }
    void runBatch({ agent_ids: ids, task_type: "sleep", args: `${parsed.interval},${parsed.jitter}` });
    closeBatchSleep();
  }, [selected, batchSleepInterval, batchSleepJitter, t, setActionMsg, runBatch, closeBatchSleep]);

  const batchDelete = useCallback(async () => {
    const ids = Array.from(selected);
    if (!ids.length) return;
    try {
      const data = await api.postJson<{ success?: boolean; deleted?: number; error?: string }>(paths.agents.batchDelete, { agent_ids: ids });
      if (data.success) {
        setActionMsg(t("agents.delete_result").replace("{count}", String(data.deleted ?? 0)));
        setSelected(new Set());
        loadBeacons();
      } else {
        setActionMsg(data.error || t("agents.delete_failed"));
      }
    } catch (e) {
      setActionMsg(errorText(t, e, () => t("agents.delete_failed")));
    }
    setConfirm(null);
  }, [selected, t, setActionMsg, setSelected, loadBeacons, setConfirm]);

  const killAgent = useCallback(async (id: string) => {
    try {
      await api.postJson(paths.agents.kill(id), {});
      setActionMsg(t("agents.kill_sent"));
      loadBeacons();
    } catch (e) {
      setActionMsg(errorText(t, e, () => t("agents.kill_failed")));
    }
    setConfirm(null);
  }, [t, setActionMsg, loadBeacons, setConfirm]);

  const deleteAgent = useCallback(async (id: string) => {
    try {
      await api.del(paths.agents.one(id));
      setActionMsg(t("agents.deleted"));
      loadBeacons();
    } catch (e) {
      setActionMsg(errorText(t, e, () => t("agents.delete_failed")));
    }
    setConfirm(null);
  }, [t, setActionMsg, loadBeacons, setConfirm]);

  const uninstallAgent = useCallback(async (id: string) => {
    try {
      await api.postJson(paths.agents.uninstall(id), {});
      setActionMsg(t("agents.detail_uninstall_sent"));
      loadBeacons();
    } catch (e) {
      setActionMsg(errorText(t, e, () => t("agents.detail_uninstall_failed")));
    }
    setConfirm(null);
  }, [t, setActionMsg, loadBeacons, setConfirm]);

  const bulkKill = useCallback(async () => {
    const ids = Array.from(selected);
    if (!ids.length) return;
    await bulkTask(ids, "kill", "");
    setConfirm(null);
  }, [selected, bulkTask, setConfirm]);

  const bulkUninstall = useCallback(async () => {
    const ids = Array.from(selected);
    if (!ids.length) return;
    await bulkTask(ids, "uninstall", "");
    setConfirm(null);
  }, [selected, bulkTask, setConfirm]);

  const handleCmdSubmit = useCallback(async () => {
    const ids = Array.from(selected);
    if (!ids.length) return;
    await bulkTask(ids, cmdType, cmdText);
    closeCmdModal();
  }, [selected, bulkTask, cmdType, cmdText, closeCmdModal]);

  const submitQuickSleep = useCallback(async () => {
    if (!quickSleepAgent) return;
    const parsed = parseSleepInput(sleepInterval, sleepJitter);
    if (!parsed) {
      setActionMsg(t("agents.sleep_invalid"));
      return;
    }
    try {
      await api.postJson(paths.agents.setSleep(quickSleepAgent.id), { interval: parsed.interval, jitter: parsed.jitter });
      setActionMsg(t("agents.sleep_updated").replace("{name}", quickSleepAgent.hostname));
      closeQuickSleep();
      loadBeacons();
    } catch (e) { setActionMsg(errorText(t, e, () => t("agents.sleep_failed"))); }
  }, [quickSleepAgent, sleepInterval, sleepJitter, t, setActionMsg, closeQuickSleep, loadBeacons]);

  const submitNotes = useCallback(async () => {
    if (!editingNotesId) return;
    try {
      await api.postJson(paths.agents.note(editingNotesId), { notes: editNotesText });
      setActionMsg(t("agents.notes_updated"));
      closeNotesEdit();
      loadBeacons();
    } catch (e) { setActionMsg(errorText(t, e, () => t("agents.notes_failed"))); }
  }, [editingNotesId, editNotesText, t, setActionMsg, closeNotesEdit, loadBeacons]);

  const exportCSV = useCallback(async () => {
    // Second click cancels an in-flight export (up to 100k rows otherwise).
    if (exporting) {
      exportAbortRef.current?.abort();
      return;
    }
    setExporting(true);
    const controller = new AbortController();
    exportAbortRef.current = controller;
    try {
      // Export respects the active filters but spans ALL pages, not just
      // the current 50-row slice.
      const all: Beacon[] = [];
      const PAGE = 500;
      for (let p = 1; p <= 200 && !controller.signal.aborted; p++) {
        const q = new URLSearchParams({ page: String(p), page_size: String(PAGE), group: "host" });
        if (searchQuery) q.set("search", searchQuery);
        if (statusFilter) q.set("status", statusFilter);
        if (osFilter) q.set("os", osFilter);
        if (tagFilter) q.set("tag_id", tagFilter);
        if (linkedFilter) q.set("linked", linkedFilter);
        q.set("sort_key", sortKey);
        q.set("sort_dir", sortDir);
        // The failure counts the list shows; a file without them cannot answer
        // which beacon is failing?.
        q.set("failures", "1");
        q.set("failures_window", failureWindow);
        const d = await api.get<{ agents?: Beacon[]; total?: number | string }>(paths.agents.list(q.toString()), { unwrap: false, signal: controller.signal });
        const body = unwrapEnvelope(d) as { agents?: Beacon[]; total?: number | string };
        const pageList = (body.agents || []) as Beacon[];
        all.push(...pageList);
        if (pageList.length < PAGE) break;
        // Yield to the event loop so the UI (and the cancel click) stays live.
        await new Promise((r) => setTimeout(r, 0));
      }
      if (controller.signal.aborted) {
        toast.info(t("agents.export_cancelled"));
        return;
      }
      if (all.length === 0) {
        toast.info(t("agents.no_beacons"));
        return;
      }
      const headers = ["Hostname", "User", "OS", "IP", "Status", "Failures", "Last Seen", "Version", "Active Window", "Notes"];
      const rows = all.map((b) => [
        b.hostname || "", b.username || "", b.os || "", b.ip || "",
        b.status || "", String(b.failure_count ?? 0), b.last_seen || "", b.version || "",
        b.active_window || "", b.notes || "",
      ]);
      const csv = [headers.join(","), ...rows.map((r) => r.map((c) => `"${String(c).replace(/"/g, '""')}"`).join(","))].join("\n");
      downloadText(csv, `agents-${new Date().toISOString().slice(0, 10)}.csv`, "text/csv");
      toast.success(t("agents.export_done").replace("{count}", String(all.length)));
    } catch (e) {
      if (e instanceof DOMException && e.name === "AbortError") {
        toast.info(t("agents.export_cancelled"));
      } else {
        toastError(e, "agents.export_failed");
      }
    } finally {
      exportAbortRef.current = null;
      setExporting(false);
    }
  }, [exporting, t, searchQuery, statusFilter, osFilter, tagFilter, linkedFilter, sortKey, sortDir, failureWindow, toastError]);

  return {
    exporting, runBatch, bulkTask,
    batchScreenshot, doBatchScreenshot, batchSleep, doBatchSleep,
    batchDelete, killAgent, deleteAgent, uninstallAgent,
    bulkKill, bulkUninstall, handleCmdSubmit, submitQuickSleep, submitNotes,
    exportCSV,
  };
}

// BulkResult re-export for page-level convenience.
export type { BulkResult };
