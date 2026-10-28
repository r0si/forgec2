import { API_BASE } from "./constants";
import { subscribeTyped } from "./typed-ws";
import { logger } from "./logger";
import { paths } from "./api-paths";
import { TASK_HARD_TIMEOUT_MS, TASK_HIDDEN_POLL_MS, TASK_POLL_INTERVAL_MS, TASK_SOFT_TIMEOUT_MS } from "./task-poll";
import { preloadLoginPage } from "./login-preload";

const TIMEOUT_MS = 30000;

let rateLimitRetryAfter = 0;

// In-flight GET dedup cache (see request()). Deleted on settle.
const getDedupCache = new Map<string, Promise<unknown>>();

export function getRateLimitRetryAfter(): number {
  if (!Number.isFinite(rateLimitRetryAfter)) rateLimitRetryAfter = 0;
  return Math.max(0, rateLimitRetryAfter - Math.floor(Date.now() / 1000));
}

export function parseRetryAfterSeconds(value: string | null, now = Date.now()): number | null {
  if (!value) return null;
  const trimmed = value.trim();
  if (!trimmed) return null;
  const numeric = Number(trimmed);
  if (Number.isFinite(numeric)) return numeric >= 0 ? Math.ceil(numeric) : null;
  const at = Date.parse(value);
  if (!Number.isFinite(at)) return null;
  return Math.max(0, Math.ceil((at - now) / 1000));
}

function readCsrfCookie(): string {
  if (typeof document === "undefined") return "";
  const match = document.cookie.match(/(?:^|;\s*)forgec2_csrf=([^;]*)/);
  if (!match) return "";
  // A malformed cookie value ("%zz") must not throw URIError here — this
  // runs synchronously before EVERY mutation request, and an uncaught throw
  // would break all writes app-wide.
  try {
    return decodeURIComponent(match[1]);
  } catch {
    return match[1];
  }
}

export function getCsrfToken(): string {
  return readCsrfCookie();
}

function buildUrl(path: string): string {
  return `${API_BASE}${path}`;
}

export function unwrapBody<T>(body: unknown): T {
  if (body && typeof body === "object" && "success" in body && (body as Record<string, unknown>).success === true && "data" in body) {
    return (body as Record<string, unknown>).data as T;
  }
  return body as T;
}

let authRedirecting = false;
let authRedirectTimer: ReturnType<typeof setTimeout> | null = null;

/** Test-only: reset 401 redirect debounce state between cases. */
export function resetAuthRedirectState(): void {
  authRedirecting = false;
  if (authRedirectTimer) {
    clearTimeout(authRedirectTimer);
    authRedirectTimer = null;
  }
}

export class ApiError extends Error {
  status: number;
  /**
   * Server-side reference for a 5xx (`X-Error-Ref`). Every server error now logs a line tagged with
   * this id, so showing it lets an operator quote something the server can find; a 4xx has none.
   */
  ref?: string;
  constructor(message: string, status: number, ref?: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
    this.ref = ref;
  }
}

/** Append the server error reference to a message, when the response carried one. */
export function withErrorRef(message: string, ref: string | null | undefined): string {
  if (!ref) return message;
  return `${message} (ref ${ref})`;
}

/** Operator-facing text for thrown API/network errors (no `ApiError:` prefix). */
export function formatThrownError(e: unknown): string {
  if (e instanceof Error && e.message) return e.message;
  return String(e);
}

export function handleUnauthorized(res: Pick<Response, "status">): void {
  // Only session expiry (401) forces re-login. 403 = authenticated but denied — stay put.
  if (res.status !== 401 || typeof window === "undefined") return;
  const p = window.location.pathname;
  if (p === "/login" || p === "/login/") return;
  if (authRedirecting) return;
  authRedirecting = true;

  // The login route is lazily split; start its chunk fetch now so the redirect
  // below lands on a form that is already (or about to be) cached.
  void preloadLoginPage();

  // Defer so concurrent 401s collapse into one navigation
  if (authRedirectTimer) clearTimeout(authRedirectTimer);
  authRedirectTimer = setTimeout(() => {
    const next = p + (window.location.search || "");
    const params = new URLSearchParams();
    params.set("expired", "1");
    if (next && next !== "/" && next !== "/login") {
      params.set("next", next);
    }
    window.location.href = `/login?${params.toString()}`;
  }, 80);
}

/**
 * Lightweight session probe used when the WebSocket drops: reuses the
 * debounced 401 redirect so an expired session sends the user to /login
 * while a merely flaky network only shows the reconnect banner.
 */
export function probeSessionExpiry(): void {
  if (typeof window === "undefined" || window.location.pathname === "/login") return;
  fetch(buildUrl(paths.auth.me), { credentials: "include" })
    .then((res) => handleUnauthorized(res))
    .catch((err) => {
      // Network blip: rely on the reconnect path. Logged (not swallowed) so
      // a persistently failing probe is diagnosable in telemetry.
      logger.debug("session probe failed", err);
    });
}

interface RequestOptions {
  method?: string;
  retries?: number;
  timeout?: number;
  signal?: AbortSignal;
  headers?: Record<string, string>;
  /**
   * Skip unwrapBody: return the raw JSON envelope ({success, total, ...})
   * instead of the unwrapped `data` payload. Needed when the caller must see
   * pagination metadata (e.g. total) alongside the list.
   */
  unwrap?: boolean;
}

async function request<T>(path: string, options: RequestOptions & { body?: unknown; raw?: boolean } = {}): Promise<T> {
  const { retries = 0, timeout = TIMEOUT_MS, signal: externalSignal, headers: extraHeaders } = options;
  const method = options.method || "GET";
  const headers: Record<string, string> = {
    "Accept": "application/json",
    ...extraHeaders,
  };

  // Coalesce concurrent identical GETs (no signal = caller is not cancelling):
  // e.g. two components mounting at once hitting the same endpoint share one
  // network request instead of firing two. Cleared on settle, so sequential
  // polls are never cached.
  const isGet = method === "GET";
  if (isGet && !externalSignal) {
    // unwrap and raw each change what the caller receives from the same path,
    // so two distinct shapes must not coalesce into one in-flight promise.
    const key = `GET ${path} ${options.unwrap === false ? "raw" : "unwrap"} ${options.raw ? "rawfetch" : "json"}`;
    const inflight = getDedupCache.get(key);
    if (inflight) return inflight as Promise<T>;
    const promise = doRequest();
    getDedupCache.set(key, promise);
    promise.finally(() => getDedupCache.delete(key)).catch(() => { /* handled by caller */ });
    return promise as Promise<T>;
  }
  return doRequest();

  async function doRequest(): Promise<T> {

  const isMutation = method !== "GET" && method !== "HEAD";
  const csrf = isMutation ? readCsrfCookie() : "";
  if (csrf) headers["X-CSRF-Token"] = csrf;
  const isFormData = options.body instanceof FormData;
  const isUrlEncoded = options.body instanceof URLSearchParams;
  const isString = typeof options.body === "string";

  if (!isFormData && !isUrlEncoded && isString && !headers["Content-Type"]) {
    headers["Content-Type"] = "application/json";
  } else if (isUrlEncoded && !headers["Content-Type"]) {
    headers["Content-Type"] = "application/x-www-form-urlencoded";
  }

  let body: BodyInit | undefined;
  if (isFormData) {
    body = options.body as FormData;
  } else if (isUrlEncoded) {
    body = options.body as URLSearchParams;
  } else if (isString) {
    body = options.body as string;
  } else if (options.body && method !== "GET" && method !== "HEAD") {
    body = JSON.stringify(options.body);
    if (!headers["Content-Type"]) headers["Content-Type"] = "application/json";
  }

  const doFetch = async (attempt: number): Promise<T> => {
    // Timeout and the caller's AbortSignal are merged into one controller per
    // attempt. The timeout is raced against the fetch so it always rejects
    // even when the environment does not honor abort (e.g. jsdom mocks), while
    // aborting the controller still cancels the real request in browsers —
    // important for mutations, where a "timed out" POST may otherwise execute
    // server-side while the caller believes it failed and re-submits.
    const controller = new AbortController();
    const onExternalAbort = () => controller.abort();
    externalSignal?.addEventListener("abort", onExternalAbort, { once: true });
    let timer: ReturnType<typeof setTimeout> | null = null;
    let timedOut = false;
    try {
      const fetchPromise = fetch(buildUrl(path), {
        method,
        credentials: "include",
        headers,
        body,
        signal: controller.signal,
      });

      let res: Response;
      if (timeout > 0) {
        res = await Promise.race([
          fetchPromise,
          new Promise<never>((_, reject) => {
            timer = setTimeout(() => {
              timedOut = true;
              controller.abort();
              reject(new Error(`Request timed out after ${timeout}ms`));
            }, timeout);
          }),
        ]);
      } else {
        res = await fetchPromise;
      }

      if (!res.ok) {
        handleUnauthorized(res);
        if (res.status === 429) {
          const retryAfter = res.headers.get("Retry-After");
          const retryAfterSeconds = parseRetryAfterSeconds(retryAfter);
          if (retryAfterSeconds != null) {
            rateLimitRetryAfter = Math.floor(Date.now() / 1000) + retryAfterSeconds;
          }
        }
        let errorMsg: string;
        if (res.status === 403) {
          errorMsg = isMutation && !csrf ? "CSRF token missing - refresh the page and try again" : "Forbidden";
        } else {
          errorMsg = `HTTP ${res.status}`;
        }
        try {
          const errBody = await res.json();
          if (errBody && typeof errBody === "object" && "error" in errBody) {
            errorMsg = String(errBody.error);
          }
        } catch { /* ignore parse error */ }
        throw new ApiError(withErrorRef(errorMsg, res.headers.get("X-Error-Ref")), res.status, res.headers.get("X-Error-Ref") ?? undefined);
      }

      if (options.raw) {
        return res as unknown as T;
      }

      if (res.status === 204 || method === "HEAD") {
        return undefined as T;
      }

      const respBody = await res.json();
      if (respBody && typeof respBody === "object" && "success" in respBody && (respBody as Record<string, unknown>).success === false) {
        const msg = (respBody as Record<string, unknown>).error ? String((respBody as Record<string, unknown>).error) : "operation failed";
        throw new ApiError(withErrorRef(msg, res.headers.get("X-Error-Ref")), res.status, res.headers.get("X-Error-Ref") ?? undefined);
      }
      return options.unwrap === false ? (respBody as T) : unwrapBody<T>(respBody);
    } catch (e) {
      if (externalSignal?.aborted) throw e;
      if (timedOut) throw e;
      if (process.env.NODE_ENV === "development") logger.debug("api request failed", { path }, e);
      if (attempt >= retries) throw e;
      return new Promise<T>((resolve, reject) => {
        const onAbort = () => {
          clearTimeout(delay);
          reject(e instanceof Error ? e : new DOMException("Aborted", "AbortError"));
        };
        const delay = setTimeout(() => {
          externalSignal?.removeEventListener("abort", onAbort);
          resolve(doFetch(attempt + 1));
        }, 800 * Math.pow(2, attempt));
        // An abort during the backoff window must not fire the next attempt
        // — and must settle this promise: clearTimeout alone would leave the
        // caller awaiting forever (stuck spinner).
        externalSignal?.addEventListener("abort", onAbort, { once: true });
      });
    } finally {
      if (timer) clearTimeout(timer);
      externalSignal?.removeEventListener("abort", onExternalAbort);
    }
  };

  return doFetch(0);
  }
}

function parseFilenameFromDisposition(cd: string | null, fallback = "download.bin"): string {
  if (!cd) return fallback;
  const star = cd.match(/filename\*\s*=\s*(?:UTF-8'')?([^;]+)/i);
  if (star) {
    try {
      const decoded = decodeURIComponent(star[1].trim().replace(/"/g, ""));
      if (decoded) return decoded;
    } catch { /* fall back to plain filename */ }
  }
  const plain = cd.match(/filename\s*=\s*"?([^";]+)"?/i);
  return plain ? plain[1].trim() : fallback;
}

export interface RequestOpts {
  retries?: number;
  signal?: AbortSignal;
  unwrap?: boolean;
  timeout?: number;
}

export const api = {
  get<T = Record<string, unknown>>(path: string, opts?: RequestOpts): Promise<T> {
    return request<T>(path, { method: "GET", retries: opts?.retries ?? 0, signal: opts?.signal, unwrap: opts?.unwrap, timeout: opts?.timeout });
  },

  post<T = Record<string, unknown>>(path: string, data?: Record<string, string>, opts?: RequestOpts): Promise<T> {
    const body = data ? new URLSearchParams(data).toString() : undefined;
    return request<T>(path, {
      method: "POST",
      headers: body ? { "Content-Type": "application/x-www-form-urlencoded" } : {},
      body,
      retries: opts?.retries ?? 0,
      signal: opts?.signal,
      timeout: opts?.timeout,
      unwrap: opts?.unwrap,
    });
  },

  postJson<T = Record<string, unknown>>(path: string, body: unknown, opts?: RequestOpts): Promise<T> {
    return request<T>(path, { method: "POST", body, retries: opts?.retries ?? 0, signal: opts?.signal, timeout: opts?.timeout, unwrap: opts?.unwrap });
  },

  postFormData<T = Record<string, unknown>>(path: string, body: FormData, opts?: RequestOpts): Promise<T> {
    return request<T>(path, { method: "POST", body, headers: {}, retries: opts?.retries ?? 0, signal: opts?.signal, timeout: opts?.timeout, unwrap: opts?.unwrap });
  },

  put<T = Record<string, unknown>>(path: string, data?: Record<string, string>, opts?: RequestOpts): Promise<T> {
    const body = data ? new URLSearchParams(data).toString() : undefined;
    return request<T>(path, {
      method: "PUT",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
      retries: opts?.retries ?? 0,
      signal: opts?.signal,
      timeout: opts?.timeout,
      unwrap: opts?.unwrap,
    });
  },

  putJson<T = Record<string, unknown>>(path: string, body: unknown, opts?: RequestOpts): Promise<T> {
    return request<T>(path, { method: "PUT", body, retries: opts?.retries ?? 0, signal: opts?.signal, timeout: opts?.timeout, unwrap: opts?.unwrap });
  },

  del<T = Record<string, unknown>>(path: string, opts?: RequestOpts): Promise<T> {
    return request<T>(path, { method: "DELETE", retries: opts?.retries ?? 0, signal: opts?.signal, timeout: opts?.timeout, unwrap: opts?.unwrap });
  },

  async download(path: string, data?: Record<string, string>, fallbackFilename = "download.bin", opts?: RequestOpts): Promise<{ blob: Blob; filename: string }> {
    const body = data ? new URLSearchParams(data).toString() : undefined;
    const res = await request<Response>(path, {
      method: "POST",
      headers: { "Content-Type": "application/x-www-form-urlencoded" },
      body,
      raw: true,
      retries: opts?.retries ?? 0,
      signal: opts?.signal,
      // Downloads can be large: default to 60s unless the caller overrides.
      timeout: opts?.timeout ?? 60000,
    });
    return { blob: await res.blob(), filename: parseFilenameFromDisposition(res.headers.get("Content-Disposition"), fallbackFilename) };
  },

  async downloadGet(path: string, fallbackFilename = "download.bin", opts?: RequestOpts): Promise<{ blob: Blob; filename: string }> {
    const res = await request<Response>(path, {
      method: "GET",
      raw: true,
      retries: opts?.retries ?? 0,
      signal: opts?.signal,
      timeout: opts?.timeout ?? 60000,
    });
    return { blob: await res.blob(), filename: parseFilenameFromDisposition(res.headers.get("Content-Disposition"), fallbackFilename) };
  },
};

export { buildUrl };

type TaskStatusBase = {
  id: number;
  result?: string;
  error?: string;
  command?: string;
  type?: string;
  agent?: string;
  created?: string;
};

type TaskRunning = TaskStatusBase & { status: "running" | "pending" | "sent" | "pending_approval" };
type TaskCompleted = TaskStatusBase & { status: "completed"; result: string };
type TaskFailed = TaskStatusBase & { status: "failed"; error: string };
type TaskCancelled = TaskStatusBase & { status: "cancelled"; error?: string };
export type TaskStatus = TaskRunning | TaskCompleted | TaskFailed | TaskCancelled;

interface PollTaskHandle {
  promise: Promise<TaskStatus>;
  cancel: () => void;
}

export async function pollTask(
  agentId: string,
  taskId: number,
  opts: {
    intervalMs?: number;
    timeoutMs?: number;
    softTimeoutMs?: number;
    signal?: AbortSignal;
    onStatus?: (st: TaskStatus) => void;
    onSoftTimeout?: () => void;
  } = {},
): Promise<TaskStatus> {
  const h = pollTaskWithCancel(agentId, taskId, opts);
  return h.promise;
}

function pollTaskWithCancel(
  agentId: string,
  taskId: number,
  opts: {
    intervalMs?: number;
    timeoutMs?: number;
    softTimeoutMs?: number;
    signal?: AbortSignal;
    onStatus?: (st: TaskStatus) => void;
    onSoftTimeout?: () => void;
  } = {},
): PollTaskHandle {
  const intervalMs = opts.intervalMs ?? TASK_POLL_INTERVAL_MS;
  // Hard deadline matches the server stale sweep (StaleRunningTaskTimeout):
  // rejecting earlier reports a live task dead and invites double-execution.
  const timeoutMs = opts.timeoutMs ?? TASK_HARD_TIMEOUT_MS;
  const softTimeoutMs = opts.softTimeoutMs ?? TASK_SOFT_TIMEOUT_MS;
  const startTime = Date.now();
  const deadline = startTime + timeoutMs;
  const softDeadline = startTime + softTimeoutMs;
  let softFired = false;
  const ac = new AbortController();
  const signal = opts.signal ?? ac.signal;

  let done = false;
  let unsub: (() => void) | null = null;
  let timer: ReturnType<typeof setTimeout> | null = null;
  let rejectRef: ((err: Error) => void) | null = null;
  // Capture the latest WebSocket-delivered plaintext so we can recover the
  // operator-visible result even if a final REST fetch momentarily returns an
  // at-rest ciphertext blob (e.g. during a key rotation window).
  let wsResult: string | undefined;
  let wsError: string | undefined;
  // WS liveness drives REST backoff in tick(); wsComplete marks an explicitly
  // complete server result (result_complete:true) — the only WS payload
  // authoritative enough to skip the final detail fetch. Shared by finalFetch,
  // the subscription callback, and tick() below.
  let wsLive = false;
  let wsComplete = false;
  let pollAttempts = 0;

  const cleanup = () => {
    done = true;
    if (unsub) unsub();
    if (timer) clearTimeout(timer);
    if (!opts.signal) ac.abort();
  };

  const atRestCipher = (s?: string) => !!s && (s.startsWith("FC2ENC:") || s.startsWith("FC2EXT:"));
  const isTerminal = (status: TaskStatus["status"]) =>
    status === "completed" || status === "failed" || status === "cancelled";
  const plaintext = (...values: Array<string | undefined>) => {
    // A just-completed REST read can briefly expose an empty persisted value
    // while the WebSocket already delivered the complete operator result.
    // Prefer meaningful plaintext, but preserve an explicit empty value when
    // every safe candidate is empty.
    let empty: string | undefined;
    for (const value of values) {
      if (value == null || atRestCipher(value)) continue;
      if (value !== "") return value;
      empty ??= value;
    }
    return empty;
  };
  const operatorStatus = (status: TaskStatus): TaskStatus => ({
    ...status,
    result: plaintext(status.result),
    error: plaintext(status.error),
  } as TaskStatus);
  const finalFetch = async (fallback: TaskStatus): Promise<TaskStatus> => {
    // Skip the authoritative REST fetch only when the WebSocket already
    // delivered an explicitly complete result (task_update with
    // result_complete:true) or a terminal cancellation: the persisted detail
    // cannot add anything. Streamed task_output accumulation is NOT enough —
    // the persisted full result is authoritative (see api-stream.test.ts).
    if (isTerminal(fallback.status)) {
      const fb = operatorStatus(fallback);
      if (fallback.status === "completed" && wsComplete && fb.result != null && fb.result !== "" && !atRestCipher(fb.result)) return fb;
      if (fallback.status === "cancelled") return fb;
    }
    try {
      const fetched = await api.get<TaskStatus>(paths.agents.task(agentId, taskId));
      // WebSocket delivery can win the race with the database-backed detail
      // endpoint. A lagging pending/running read must not roll a terminal state
      // back to non-terminal.
      const status = isTerminal(fallback.status) && !isTerminal(fetched.status)
        ? fallback.status
        : fetched.status;
      return operatorStatus({
        ...fetched,
        status,
        result: plaintext(fetched.result, fallback.result, wsResult),
        error: plaintext(fetched.error, fallback.error, wsError),
      } as TaskStatus);
    } catch {
      return operatorStatus(fallback);
    }
  };

  const promise = new Promise<TaskStatus>((resolve, reject) => {
    rejectRef = reject;
    const finish = async (st: TaskStatus) => {
      if (done) return;
      cleanup();
      resolve(await finalFetch(st));
    };
    const fail = (err: Error) => {
      if (done) return;
      cleanup();
      reject(err);
    };

    try {
      let streamedOutput = "";
      unsub = subscribeTyped(["task_update", "task_output"], (ev) => {
        if (Number(ev.task_id) !== taskId) return;
        wsLive = true;
        if ("status" in ev) {
          const status = String(ev.status) as TaskStatus["status"];
          // Most task_update results are short previews. Interactive shell
          // results are consumed only when the server explicitly marks the
          // value complete; otherwise full output comes from task_output frames
          // or the authoritative detail fetch in finalFetch().
          const eventResult = ev.result_complete === true
            ? plaintext(ev.result as string | undefined)
            : undefined;
          const eventError = plaintext(ev.error as string | undefined);
          if (eventResult != null) { wsResult = eventResult; wsComplete = true; }
          if (eventError != null) wsError = eventError;
          const partial = operatorStatus({
            id: taskId,
            status,
            result: eventResult,
            error: eventError,
          } as TaskStatus);
          opts.onStatus?.(partial);
          if (isTerminal(status)) finish(partial);
          return;
        }
        // Ordered streaming frame for large shell results: deliver the
        // accumulated output so callers can render incrementally.
        streamedOutput += String(ev.chunk ?? "");
        wsResult = streamedOutput;
        opts.onStatus?.({
          id: taskId,
          status: "running",
          result: streamedOutput,
        } as TaskStatus);
        if (ev.done) {
          finish({
            id: taskId,
            status: "completed",
            result: streamedOutput,
          } as TaskStatus);
        }
      });
    } catch {
      unsub = null;
    }

    const tick = async () => {
      if (done) return;
      if (signal.aborted) return fail(new Error("cancelled"));
      if (Date.now() > deadline) return fail(new Error("Agent did not respond within the timeout (is it online?)"));
      if (!softFired && Date.now() > softDeadline) {
        softFired = true;
        try {
          opts.onSoftTimeout?.();
        } catch {
          // Caller hint must never break the poll loop.
        }
      }
      try {
        const st = operatorStatus(await api.get<TaskStatus>(paths.agents.task(agentId, taskId), { signal }));
        // A terminal WebSocket event may have completed the task while this
        // fallback request was in flight. Never emit its older snapshot or
        // schedule another timer after cleanup.
        if (done) return;
        opts.onStatus?.(st);
        if (isTerminal(st.status)) return finish(st);
      } catch (err) {
        if (done) return;
        if (signal.aborted) return fail(new Error("cancelled"));
        if (process.env.NODE_ENV === "development") logger.debug("pollTask failed", { agentId, taskId }, err);
      }
      // WS live: the socket delivers terminal state promptly, so back off REST
      // polling to 8s to cut ~5x requests. Otherwise exponential backoff
      // 1.5s -> 2s -> 4s capped at 10s for disconnected/degraded sockets.
      // Hidden tab: slow to a background cadence instead of stalling (the old
      // hook stopped entirely) or hammering a throttled timer.
      pollAttempts += 1;
      const backoff = Math.min(intervalMs * Math.pow(1.5, Math.min(pollAttempts, 4)), 10000);
      const nextDelay = document.hidden
        ? Math.max(backoff, TASK_HIDDEN_POLL_MS)
        : wsLive ? Math.max(backoff, 8000) : backoff;
      timer = setTimeout(tick, nextDelay);
    };
    tick();
  });

  return {
    promise,
    cancel: () => {
      if (done) return;
      cleanup();
      rejectRef?.(new Error("cancelled"));
    },
  };
}

import { AGENT_STATUSES } from "@/lib/status";
import type { AgentStatus } from "@/types/agent";

const VALID_STATUSES: readonly string[] = AGENT_STATUSES;

async function getAgentStatus(agentId: string, signal?: AbortSignal): Promise<AgentStatus | "unknown"> {
  const data = await api.get<{ Agent?: { status?: string }; agent?: { status?: string } }>(paths.agents.one(agentId), { signal });
  const raw = data.agent?.status || data.Agent?.status || "unknown";
  return (VALID_STATUSES.includes(raw) ? raw : "unknown") as AgentStatus | "unknown";
}

interface RunTaskOptions {
  method?: "post" | "postJson";
  body?: Record<string, unknown> | Record<string, string>;
  intervalMs?: number;
  timeoutMs?: number;
  softTimeoutMs?: number;
  checkOnline?: boolean;
  onStatus?: (st: TaskStatus) => void;
  onSoftTimeout?: () => void;
  signal?: AbortSignal;
}

export async function runTask(
  agentId: string,
  path: string,
  opts: RunTaskOptions = {},
): Promise<TaskStatus> {
  if (opts.checkOnline) {
    const status = await getAgentStatus(agentId, opts.signal).catch(() => "unknown");
    if (status !== "online") {
      throw new Error(`Agent is ${status} — start the agent and ensure it is online before running this.`);
    }
  }
  const postFn = opts.method === "postJson" ? api.postJson : api.post;
  const res = await postFn<{ success?: boolean; task_id?: number; error?: string }>(
    path,
    (opts.body as Record<string, string>) || {},
    { signal: opts.signal },
  );
  const taskId = res.task_id;
  if (!taskId) {
    const errMsg = (res as Record<string, unknown>).error;
    throw new Error(typeof errMsg === "string" ? errMsg : "Failed to queue task (no task_id returned)");
  }
  return pollTask(agentId, taskId, {
    intervalMs: opts.intervalMs,
    timeoutMs: opts.timeoutMs,
    softTimeoutMs: opts.softTimeoutMs,
    signal: opts.signal,
    onStatus: opts.onStatus,
    onSoftTimeout: opts.onSoftTimeout,
  });
}


