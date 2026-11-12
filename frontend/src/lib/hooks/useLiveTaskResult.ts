import { useCallback, useEffect, useRef, useState } from "react";
import { runTask, type TaskStatus } from "@/lib/api";
import { useI18n } from "@/lib/i18n";

type LiveTaskStatus = "idle" | "pending" | "running" | "completed" | "failed" | "cancelled" | "timeout";

function toLiveTaskStatus(status: TaskStatus["status"]): LiveTaskStatus {
  if (status === "pending" || status === "pending_approval") return "pending";
  if (status === "running" || status === "sent") return "running";
  if (status === "cancelled") return "cancelled";
  return status;
}

interface UseLiveTaskResultOptions {
  /** Max time to wait for a task result before failing with the timeout status. */
  timeoutMs?: number;
}

/**
 * Dispatches a task via `runTask` and tracks its lifecycle live over the
 * WebSocket (`task_update`/`task_output` frames) instead of page-side polling.
 * Resolves with the final TaskStatus (full result fetched once completed).
 */
export function useLiveTaskResult(opts: UseLiveTaskResultOptions = {}) {
  const { t } = useI18n();
  const [status, setStatus] = useState<LiveTaskStatus>("idle");
  const [result, setResult] = useState("");
  const [error, setError] = useState("");
  const [taskId, setTaskId] = useState<number | null>(null);
  const seqRef = useRef(0);
  const cancelRef = useRef<AbortController | null>(null);

  const cancel = useCallback(() => {
    seqRef.current += 1;
    if (cancelRef.current) {
      cancelRef.current.abort();
      cancelRef.current = null;
    }
  }, []);

  // Abort any in-flight poll on unmount so the global WS listener and the
  // 1.5s HTTP fallback poll are torn down instead of calling setState on a
  // dead component for up to the task timeout.
  useEffect(() => cancel, [cancel]);

  const reset = useCallback(() => {
    cancel();
    setStatus("idle");
    setResult("");
    setError("");
    setTaskId(null);
  }, [cancel]);

  const run = useCallback(
    async (agentId: string, path: string, body: Record<string, string>): Promise<TaskStatus | null> => {
      cancel();
      const seq = seqRef.current;
      const controller = new AbortController();
      cancelRef.current = controller;
      setStatus("pending");
      setResult("");
      setError("");
      setTaskId(null);
      try {
        const final = await runTask(agentId, path, {
          body,
          timeoutMs: opts.timeoutMs,
          signal: controller.signal,
          onStatus: (st: TaskStatus) => {
            if (seqRef.current !== seq) return;
            setTaskId(st.id);
            setStatus(toLiveTaskStatus(st.status));
            if (st.result) setResult(st.result);
            if (st.status === "failed") setError(st.error ?? t("common.task_failed"));
            else if (st.status === "cancelled") setError(st.error ?? t("common.task_cancelled"));
          },
        });
        if (seqRef.current !== seq) return null;
        setTaskId(final.id);
        setStatus(toLiveTaskStatus(final.status));
        if (final.result) setResult(final.result);
        if (final.status === "failed") setError(final.error ?? t("common.task_failed"));
        else if (final.status === "cancelled") setError(final.error ?? t("common.task_cancelled"));
        return final;
      } catch (e) {
        if (seqRef.current !== seq) return null;
        const msg = e instanceof Error ? e.message : String(e);
        setError(msg);
        setStatus(msg.includes("did not respond") ? "timeout" : "failed");
        return null;
      } finally {
        if (seqRef.current === seq) cancelRef.current = null;
      }
    },
    [opts.timeoutMs, cancel, t],
  );

  return { taskId, status, result, error, run, reset, running: status === "pending" || status === "running" };
}
