import { API_BASE } from "@/lib/constants";
import { api, getCsrfToken, handleUnauthorized, pollTask } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { TASK_HARD_TIMEOUT_MS } from "@/lib/task-poll";
import { downloadBlob } from "@/lib/download";
import { exfilBasename, fileTaskId, fileTaskIds, pullPlan, transferProgressAt, type TransferProgress } from "./file-task";

type TFn = (key: string, params?: Record<string, string | number>) => string;

// Must match the server's MaxUploadSize (50MiB): larger files upload fully
// only to be rejected with 400, so fail fast client-side instead.
const MAX_PUSH_BYTES = 50 * 1024 * 1024;

// Upper bound for one push (50MB on a slow link); XHR has no default timeout.
const UPLOAD_TIMEOUT_MS = 600_000;

interface StagedRemoteFile {
  /** Server-side `files/exfil` URL; supports HTTP Range once staged. */
  url: string;
  filename: string;
  partial: boolean;
}

/**
 * Pull a remote file into the teamserver's per-agent upload cache without
 * downloading it to the browser. Playback uses this so `<video>`/`<audio>` can
 * issue Range requests against the staged copy instead of buffering base64
 * task results.
 */
export async function stageRemoteFile(opts: {
  agentId: string;
  remotePath: string;
  filename: string;
  fileSize?: number;
  t: TFn;
  signal?: AbortSignal;
  onProgress?: (p: TransferProgress) => void;
}): Promise<StagedRemoteFile> {
  const plan = pullPlan(opts.fileSize ?? 0);
  let offset = 0;
  opts.onProgress?.(transferProgressAt(0, plan));
  while (offset < plan.total) {
    if (opts.signal?.aborted) throw new DOMException("aborted", "AbortError");
    const size = Math.min(plan.chunk, plan.total - offset);
    const data = await api.post(paths.agents.filesExfil(opts.agentId), {
      path: opts.remotePath,
      offset: String(offset),
      size: String(size),
    });
    const taskId = fileTaskId(data);
    if (!taskId) throw new Error(opts.t("agents.files_pull_failed"));
    const st = await pollTask(opts.agentId, taskId, { timeoutMs: TASK_HARD_TIMEOUT_MS, signal: opts.signal });
    if (st.status === "failed") throw new Error(st.error || opts.t("agents.files_pull_failed"));
    offset += size;
    opts.onProgress?.(transferProgressAt(offset, plan));
  }
  return {
    url: paths.agents.filesExfilGet(opts.agentId, exfilBasename(opts.remotePath)),
    filename: exfilBasename(opts.remotePath),
    partial: plan.partial,
  };
}

export async function pullRemoteFile(opts: {
  agentId: string;
  remotePath: string;
  filename: string;
  fileSize?: number;
  t: TFn;
  signal?: AbortSignal;
  onProgress?: (p: TransferProgress) => void;
}): Promise<"pulled" | "partial"> {
  const staged = await stageRemoteFile(opts);
  const { blob, filename: saved } = await api.downloadGet(staged.url);
  downloadBlob(blob, saved || opts.filename);
  return staged.partial ? "partial" : "pulled";
}

export function pushLocalFile(opts: {
  agentId: string;
  destPath: string;
  file: File;
  t: TFn;
  onProgress?: (pct: number) => void;
  signal?: AbortSignal;
}): Promise<void> {
  return new Promise((resolve, reject) => {
    if (opts.file.size > MAX_PUSH_BYTES) {
      reject(new Error(opts.t("agents.files_upload_too_large", { max: "50 MB" })));
      return;
    }
    const formData = new FormData();
    formData.append("file", opts.file);
    formData.append("target_path", opts.destPath);
    formData.append("path", opts.destPath);
    const xhr = new XMLHttpRequest();
    xhr.open("POST", `${API_BASE}${paths.agents.filesPush(opts.agentId)}`);
    xhr.setRequestHeader("X-CSRF-Token", getCsrfToken());
    xhr.withCredentials = true;
    // XHR (not fetch) is deliberate: only XHR reports upload progress.
    // Bound the wait and surface auth expiry instead of a generic error:
    // a 401 here previously read as "Upload failed (HTTP 401)".
    xhr.timeout = UPLOAD_TIMEOUT_MS;
    const onAbort = () => {
      xhr.abort();
      reject(new DOMException("aborted", "AbortError"));
    };
    opts.signal?.addEventListener("abort", onAbort, { once: true });
    xhr.upload.addEventListener("progress", (evt) => {
      if (evt.lengthComputable) opts.onProgress?.(Math.round((evt.loaded / evt.total) * 100));
    });
    xhr.addEventListener("load", () => {
      opts.signal?.removeEventListener("abort", onAbort);
      void (async () => {
        try {
          if (xhr.status === 401) {
            // Central session-expiry flow (debounced redirect to login);
            // without this a 401 reads as a generic upload failure.
            handleUnauthorized({ status: xhr.status });
          }
          if (xhr.status < 200 || xhr.status >= 300) {
            throw new Error(opts.t("agents.files_upload_failed_status", { status: xhr.status }));
          }
          let ack: unknown = {};
          try { ack = JSON.parse(xhr.responseText || "{}"); } catch { /* non-json */ }
          const taskIds = fileTaskIds(ack);
          if (taskIds.length > 0) {
            // Large pushes are split into one task per chunk. Wait for every
            // chunk and fail the whole upload if any of them did not complete;
            // polling only the first one reported success on a partial file.
            const statuses = await Promise.all(
              taskIds.map((taskId) =>
                pollTask(opts.agentId, taskId, { timeoutMs: TASK_HARD_TIMEOUT_MS, signal: opts.signal }),
              ),
            );
            const unfinished = statuses.find((st) => st.status !== "completed");
            if (unfinished) throw new Error(unfinished.error || opts.t("agents.files_push_failed"));
          }
          resolve();
        } catch (err) {
          reject(err);
        }
      })();
    });
    xhr.addEventListener("error", () => {
      opts.signal?.removeEventListener("abort", onAbort);
      reject(new Error(opts.t("agents.files_upload_failed_network")));
    });
    xhr.addEventListener("timeout", () => {
      opts.signal?.removeEventListener("abort", onAbort);
      reject(new Error(opts.t("agents.files_upload_timed_out", { seconds: Math.round(UPLOAD_TIMEOUT_MS / 1000) })));
    });
    xhr.send(formData);
  });
}
