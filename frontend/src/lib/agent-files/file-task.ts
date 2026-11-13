/** dispatchTask / file read-delete return this — not file bytes and not a completed delete. */
export function isFileTaskAck(data: unknown): boolean {
  if (!data || typeof data !== "object" || Array.isArray(data)) return false;
  const o = data as Record<string, unknown>;
  const taskId = Number(o.task_id);
  if (!Number.isFinite(taskId) || taskId <= 0) return false;
  if (typeof o.content === "string" && o.content.length > 0) return false;
  if (o.kind === "file_task" || o.kind === "ls_task" || o.queued === true) return true;
  return o.success === true;
}

export function fileTaskId(data: unknown): number | null {
  if (!data || typeof data !== "object") return null;
  const id = Number((data as Record<string, unknown>).task_id);
  return Number.isFinite(id) && id > 0 ? id : null;
}

/**
 * Every task a file push queued. Large files are split into one task per
 * chunk, and a push is only successful when all of them finished: polling
 * only the first chunk reported success while later chunks could still fail.
 */
export function fileTaskIds(data: unknown): number[] {
  if (!data || typeof data !== "object") return [];
  const o = data as Record<string, unknown>;
  const ids: number[] = [];
  const add = (value: unknown) => {
    const id = Number(value);
    if (Number.isFinite(id) && id > 0 && !ids.includes(id)) ids.push(id);
  };
  if (Array.isArray(o.task_ids)) {
    for (const value of o.task_ids) add(value);
  }
  add(o.task_id);
  return ids;
}

export function looksLikeFileTaskAckJson(text: string): boolean {
  try {
    return isFileTaskAck(JSON.parse(text));
  } catch {
    return false;
  }
}

export function deleteCopyKind(queued: boolean): "queued" | "deleted" {
  return queued ? "queued" : "deleted";
}

export function deleteLooksConfirmed(kind: "queued" | "deleted"): boolean {
  return kind === "deleted";
}

export const EXFIL_CHUNK = 4 * 1024 * 1024;
export const EXFIL_CAP = 50 * 1024 * 1024;

export function exfilBasename(remotePath: string): string {
  const parts = remotePath.split(/[\\/]/).filter(Boolean);
  return parts[parts.length - 1] || "file.bin";
}

export function pullPlan(fileSize: number): { total: number; chunk: number; partial: boolean } {
  // C7 fix: when file size is unknown (≤0), mark as partial so the pull loop
  // keeps fetching until a short chunk arrives (length < chunk size), rather
  // than assuming the file is exactly one EXFIL_CHUNK and showing 100% after
  // the first chunk is received.
  if (!Number.isFinite(fileSize) || fileSize <= 0) {
    return { total: EXFIL_CHUNK, chunk: EXFIL_CHUNK, partial: true };
  }
  if (fileSize > EXFIL_CAP) {
    return { total: EXFIL_CAP, chunk: EXFIL_CHUNK, partial: true };
  }
  return { total: fileSize, chunk: EXFIL_CHUNK, partial: false };
}

export interface TransferProgress {
  offset: number;
  total: number;
  chunkIndex: number;
  chunkCount: number;
  partial: boolean;
}

export function transferChunkCount(total: number, chunk: number): number {
  if (!Number.isFinite(total) || total <= 0 || !Number.isFinite(chunk) || chunk <= 0) return 1;
  return Math.max(1, Math.ceil(total / chunk));
}

export function transferProgressAt(
  offset: number,
  plan: { total: number; chunk: number; partial: boolean },
): TransferProgress {
  const chunkCount = transferChunkCount(plan.total, plan.chunk);
  const clamped = Math.max(0, Math.min(offset, plan.total));
  const chunkIndex = clamped <= 0 ? 0 : Math.min(chunkCount, Math.ceil(clamped / plan.chunk));
  return { offset: clamped, total: plan.total, chunkIndex, chunkCount, partial: plan.partial };
}

export function transferPercent(p: Pick<TransferProgress, "offset" | "total">): number {
  if (p.total <= 0) return 0;
  return Math.min(100, Math.round((p.offset / p.total) * 100));
}

export function parseFindResult(result: string): string[] {
  if (!result || looksLikeFileTaskAckJson(result)) return [];
  const out: string[] = [];
  for (const raw of result.split(/\r?\n/)) {
    const line = raw.trim();
    if (!line) continue;
    if (line.startsWith("=== downloaded")) break;
    if (line.startsWith("#") || line.startsWith("===") || line === "path\tsize\tmtime\tstatus" || line.startsWith("path\t")) {
      continue;
    }
    const path = line.split("\t")[0]?.trim() || "";
    if (path) out.push(path);
  }
  return out;
}

const IMAGE_MIME_BY_EXT: Record<string, string> = {
  jpg: "image/jpeg",
  jpeg: "image/jpeg",
  png: "image/png",
  gif: "image/gif",
  bmp: "image/bmp",
  webp: "image/webp",
  tiff: "image/tiff",
  tif: "image/tiff",
  ico: "image/x-icon",
  svg: "image/svg+xml",
};

export function imageMimeForFilename(filename: string): string {
  const ext = filename.split(".").pop()?.toLowerCase() ?? "";
  return IMAGE_MIME_BY_EXT[ext] ?? "image/png";
}

export function fileReadPreview(result: string, isImage: boolean, filename = ""): { content: string; isImage: boolean } {
  const raw = result || "";
  if (looksLikeFileTaskAckJson(raw)) {
    return { content: "", isImage: false };
  }
  if (!isImage) return { content: raw, isImage: false };
  if (raw.startsWith("data:")) return { content: raw, isImage: true };
  const compact = raw.replace(/\s+/g, "");
  if (compact.length > 32 && /^[A-Za-z0-9+/=]+$/.test(compact)) {
    return { content: `data:${imageMimeForFilename(filename)};base64,${compact}`, isImage: true };
  }
  return { content: raw, isImage: false };
}
