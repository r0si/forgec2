import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { API_BASE } from "@/lib/constants";
import { paths } from "@/lib/api-paths";
import { TASK_HARD_TIMEOUT_MS } from "@/lib/task-poll";
import { EXFIL_CAP, EXFIL_CHUNK, exfilBasename, pullPlan, transferProgressAt } from "./file-task";
import { pullRemoteFile, pushLocalFile } from "./file-transfer";

const mocks = vi.hoisted(() => ({
  post: vi.fn(),
  downloadGet: vi.fn(),
  pollTask: vi.fn(),
  getCsrfToken: vi.fn(() => "csrf-token"),
  handleUnauthorized: vi.fn(),
  downloadBlob: vi.fn(),
}));

vi.mock("@/lib/api", () => ({
  api: { post: mocks.post, downloadGet: mocks.downloadGet },
  pollTask: mocks.pollTask,
  getCsrfToken: mocks.getCsrfToken,
  handleUnauthorized: mocks.handleUnauthorized,
}));
vi.mock("@/lib/download", () => ({ downloadBlob: mocks.downloadBlob }));

const t = vi.fn((key: string) => key);

class FakeXHR {
  static instances: FakeXHR[] = [];
  static reset() {
    FakeXHR.instances = [];
  }

  method = "";
  url = "";
  headers: Record<string, string> = {};
  withCredentials = false;
  timeout = 0;
  status = 0;
  responseText = "";
  aborted = false;
  sentBody: unknown;

  private listeners = new Map<string, Array<(ev?: unknown) => void>>();
  private uploadListeners = new Map<string, Array<(ev?: unknown) => void>>();

  upload = {
    addEventListener: (type: string, cb: (ev?: unknown) => void) => {
      const list = this.uploadListeners.get(type) ?? [];
      list.push(cb);
      this.uploadListeners.set(type, list);
    },
  };

  constructor() {
    FakeXHR.instances.push(this);
  }

  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }

  setRequestHeader(key: string, value: string) {
    this.headers[key] = value;
  }

  addEventListener(type: string, cb: (ev?: unknown) => void) {
    const list = this.listeners.get(type) ?? [];
    list.push(cb);
    this.listeners.set(type, list);
  }

  send(body: unknown) {
    this.sentBody = body;
  }

  abort() {
    this.aborted = true;
  }

  emit(type: string, ev?: unknown) {
    for (const cb of this.listeners.get(type) ?? []) cb(ev);
  }

  emitUpload(type: string, ev?: unknown) {
    for (const cb of this.uploadListeners.get(type) ?? []) cb(ev);
  }
}

function lastXhr(): FakeXHR {
  const list = FakeXHR.instances;
  const xhr = list[list.length - 1];
  if (!xhr) throw new Error("no XHR was created");
  return xhr;
}

describe("pullRemoteFile", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("pulls a small file in one chunk and downloads the blob", async () => {
    mocks.post.mockResolvedValue({ success: true, task_id: 42 });
    mocks.pollTask.mockResolvedValue({ status: "completed" });
    const blob = new Blob(["loot"]);
    mocks.downloadGet.mockResolvedValue({ blob, filename: "saved.txt" });
    const onProgress = vi.fn();
    const plan = pullPlan(128);

    const out = await pullRemoteFile({
      agentId: "a1",
      remotePath: "C:\\tmp\\loot.txt",
      filename: "loot.txt",
      fileSize: 128,
      t,
      onProgress,
    });

    expect(out).toBe("pulled");
    expect(mocks.post).toHaveBeenCalledTimes(1);
    expect(mocks.post).toHaveBeenCalledWith(paths.agents.filesExfil("a1"), {
      path: "C:\\tmp\\loot.txt",
      offset: "0",
      size: "128",
    });
    expect(mocks.pollTask).toHaveBeenCalledWith(
      "a1",
      42,
      expect.objectContaining({ timeoutMs: TASK_HARD_TIMEOUT_MS }),
    );
    expect(mocks.downloadGet).toHaveBeenCalledWith(
      paths.agents.filesExfilGet("a1", exfilBasename("C:\\tmp\\loot.txt")),
    );
    expect(mocks.downloadBlob).toHaveBeenCalledWith(blob, "saved.txt");
    expect(onProgress).toHaveBeenNthCalledWith(1, transferProgressAt(0, plan));
    expect(onProgress).toHaveBeenNthCalledWith(2, transferProgressAt(128, plan));
  });

  it("treats an unknown size as a single partial chunk", async () => {
    mocks.post.mockResolvedValue({ task_id: 1 });
    mocks.pollTask.mockResolvedValue({ status: "completed" });
    mocks.downloadGet.mockResolvedValue({ blob: new Blob(), filename: "" });

    const out = await pullRemoteFile({
      agentId: "a1",
      remotePath: "/tmp/unknown.bin",
      filename: "unknown.bin",
      t,
    });

    expect(out).toBe("partial");
    expect(mocks.post).toHaveBeenCalledTimes(1);
    // The saved filename is empty: the caller-provided name is the fallback.
    expect(mocks.downloadBlob).toHaveBeenCalledWith(expect.any(Blob), "unknown.bin");
  });

  it("loops chunked pulls and reports partial at the cap", async () => {
    mocks.post.mockResolvedValue({ task_id: 7 });
    mocks.pollTask.mockResolvedValue({ status: "completed" });
    mocks.downloadGet.mockResolvedValue({ blob: new Blob(), filename: "big.bin" });
    const onProgress = vi.fn();

    const out = await pullRemoteFile({
      agentId: "a",
      remotePath: "/loot/big.bin",
      filename: "big.bin",
      fileSize: EXFIL_CAP + 1,
      t,
      onProgress,
    });

    const chunks = Math.ceil(EXFIL_CAP / EXFIL_CHUNK);
    const lastOffset = (chunks - 1) * EXFIL_CHUNK;
    expect(out).toBe("partial");
    expect(mocks.post).toHaveBeenCalledTimes(chunks);
    expect(mocks.pollTask).toHaveBeenCalledTimes(chunks);
    expect(mocks.post).toHaveBeenLastCalledWith(paths.agents.filesExfil("a"), {
      path: "/loot/big.bin",
      offset: String(lastOffset),
      size: String(EXFIL_CAP - lastOffset),
    });
    expect(mocks.downloadBlob).toHaveBeenCalledWith(expect.any(Blob), "big.bin");
    expect(onProgress).toHaveBeenCalledTimes(chunks + 1);
  });

  it("fails the pull when the dispatch ACK carries no task id", async () => {
    mocks.post.mockResolvedValue({});

    await expect(
      pullRemoteFile({ agentId: "a", remotePath: "/x", filename: "x", fileSize: 8, t }),
    ).rejects.toThrow("agents.files_pull_failed");
    expect(mocks.pollTask).not.toHaveBeenCalled();
  });

  it("surfaces the server's task error, falling back to the i18n message", async () => {
    mocks.post.mockResolvedValue({ task_id: 5 });

    mocks.pollTask.mockResolvedValue({ status: "failed", error: "no such file" });
    await expect(
      pullRemoteFile({ agentId: "a", remotePath: "/x", filename: "x", fileSize: 8, t }),
    ).rejects.toThrow("no such file");

    mocks.pollTask.mockResolvedValue({ status: "failed" });
    await expect(
      pullRemoteFile({ agentId: "a", remotePath: "/x", filename: "x", fileSize: 8, t }),
    ).rejects.toThrow("agents.files_pull_failed");
    expect(t).toHaveBeenCalledWith("agents.files_pull_failed");
  });

  it("refuses to start when the signal is already aborted", async () => {
    const controller = new AbortController();
    controller.abort();

    await expect(
      pullRemoteFile({
        agentId: "a",
        remotePath: "/x",
        filename: "x",
        fileSize: 8,
        t,
        signal: controller.signal,
      }),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(mocks.post).not.toHaveBeenCalled();
    expect(mocks.downloadBlob).not.toHaveBeenCalled();
  });

  it("stops chunking when the signal aborts mid-pull", async () => {
    const controller = new AbortController();
    mocks.post.mockResolvedValue({ task_id: 5 });
    mocks.pollTask.mockImplementation(async () => {
      controller.abort();
      return { status: "completed" };
    });

    await expect(
      pullRemoteFile({
        agentId: "a",
        remotePath: "/x",
        filename: "x",
        fileSize: EXFIL_CHUNK * 2,
        t,
        signal: controller.signal,
      }),
    ).rejects.toMatchObject({ name: "AbortError" });
    expect(mocks.post).toHaveBeenCalledTimes(1);
    expect(mocks.pollTask).toHaveBeenCalledTimes(1);
  });
});

describe("pushLocalFile", () => {
  const file = () => new File(["payload"], "x.bin", { type: "application/octet-stream" });

  beforeEach(() => {
    vi.clearAllMocks();
    FakeXHR.reset();
    vi.stubGlobal("XMLHttpRequest", FakeXHR);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it("rejects files over the 50 MiB cap before any upload starts", async () => {
    const big = file();
    Object.defineProperty(big, "size", { value: EXFIL_CAP + 1 });

    await expect(pushLocalFile({ agentId: "a", destPath: "C:\\drop", file: big, t })).rejects.toThrow(
      "agents.files_upload_too_large",
    );
    expect(t).toHaveBeenCalledWith("agents.files_upload_too_large", { max: "50 MB" });
    expect(FakeXHR.instances).toHaveLength(0);
  });

  it("uploads the form and resolves on a 2xx without a task ACK", async () => {
    const payload = file();
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: payload, t });
    const xhr = lastXhr();

    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe(`${API_BASE}${paths.agents.filesPush("a1")}`);
    expect(xhr.headers["X-CSRF-Token"]).toBe("csrf-token");
    expect(xhr.withCredentials).toBe(true);
    expect(xhr.timeout).toBe(600_000);
    const formData = xhr.sentBody as FormData;
    expect(formData.get("file")).toBe(payload);
    expect(formData.get("target_path")).toBe("C:\\drop");
    expect(formData.get("path")).toBe("C:\\drop");

    xhr.status = 200;
    xhr.responseText = "{}";
    xhr.emit("load");

    await expect(p).resolves.toBeUndefined();
    expect(mocks.pollTask).not.toHaveBeenCalled();
  });

  it("polls the resulting chunk task and resolves when it completes", async () => {
    mocks.pollTask.mockResolvedValue({ status: "completed" });
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 200;
    xhr.responseText = JSON.stringify({ success: true, task_id: 9 });
    xhr.emit("load");

    await expect(p).resolves.toBeUndefined();
    expect(mocks.pollTask).toHaveBeenCalledWith(
      "a1",
      9,
      expect.objectContaining({ timeoutMs: TASK_HARD_TIMEOUT_MS }),
    );
  });

  it("rejects when the chunk task fails, with the i18n fallback", async () => {
    mocks.pollTask.mockResolvedValue({ status: "failed", error: "disk full" });
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 200;
    xhr.responseText = JSON.stringify({ success: true, task_id: 9 });
    xhr.emit("load");
    await expect(p).rejects.toThrow("disk full");

    mocks.pollTask.mockResolvedValue({ status: "failed" });
    const p2 = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr2 = lastXhr();
    xhr2.status = 200;
    xhr2.responseText = JSON.stringify({ success: true, task_id: 9 });
    xhr2.emit("load");
    await expect(p2).rejects.toThrow("agents.files_push_failed");
    expect(t).toHaveBeenCalledWith("agents.files_push_failed");
  });

  it("waits for every chunk task before reporting a successful push", async () => {
    mocks.pollTask.mockResolvedValue({ status: "completed" });
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 200;
    xhr.responseText = JSON.stringify({ success: true, task_id: 9, task_ids: [9, 10, 11] });
    xhr.emit("load");

    await expect(p).resolves.toBeUndefined();
    expect(mocks.pollTask).toHaveBeenCalledTimes(3);
    expect(mocks.pollTask.mock.calls.map((call) => call[1])).toEqual([9, 10, 11]);
  });

  it("fails the whole push when a later chunk failed", async () => {
    mocks.pollTask.mockImplementation((_agentId: string, taskId: number) =>
      Promise.resolve(taskId === 11
        ? { status: "failed", error: "chunk 3 write failed" }
        : { status: "completed" }),
    );
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 200;
    xhr.responseText = JSON.stringify({ success: true, task_id: 9, task_ids: [9, 10, 11] });
    xhr.emit("load");

    await expect(p).rejects.toThrow("chunk 3 write failed");
    expect(mocks.pollTask).toHaveBeenCalledTimes(3);
  });

  it("routes a 401 through the central unauthorized flow and reports the status", async () => {
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 401;
    xhr.responseText = "";
    xhr.emit("load");

    await expect(p).rejects.toThrow("agents.files_upload_failed_status");
    expect(mocks.handleUnauthorized).toHaveBeenCalledWith({ status: 401 });
    expect(t).toHaveBeenCalledWith("agents.files_upload_failed_status", { status: 401 });
  });

  it("reports non-401 HTTP failures without the unauthorized flow", async () => {
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 500;
    xhr.responseText = "boom";
    xhr.emit("load");

    await expect(p).rejects.toThrow("agents.files_upload_failed_status");
    expect(mocks.handleUnauthorized).not.toHaveBeenCalled();
    expect(t).toHaveBeenCalledWith("agents.files_upload_failed_status", { status: 500 });
  });

  it("treats a non-JSON success body as an empty ack", async () => {
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    const xhr = lastXhr();
    xhr.status = 200;
    xhr.responseText = "OK";
    xhr.emit("load");

    await expect(p).resolves.toBeUndefined();
    expect(mocks.pollTask).not.toHaveBeenCalled();
  });

  it("maps transport error and timeout events to i18n errors", async () => {
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    lastXhr().emit("error");
    await expect(p).rejects.toThrow("agents.files_upload_failed_network");
    expect(t).toHaveBeenCalledWith("agents.files_upload_failed_network");

    const p2 = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t });
    lastXhr().emit("timeout");
    await expect(p2).rejects.toThrow("agents.files_upload_timed_out");
    expect(t).toHaveBeenCalledWith("agents.files_upload_timed_out", { seconds: 600 });
  });

  it("aborts the request when the signal fires", async () => {
    const controller = new AbortController();
    const p = pushLocalFile({
      agentId: "a1",
      destPath: "C:\\drop",
      file: file(),
      t,
      signal: controller.signal,
    });
    const xhr = lastXhr();

    controller.abort();

    await expect(p).rejects.toMatchObject({ name: "AbortError" });
    expect(xhr.aborted).toBe(true);
  });

  it("reports upload progress percentages only when computable", async () => {
    const onProgress = vi.fn();
    const p = pushLocalFile({ agentId: "a1", destPath: "C:\\drop", file: file(), t, onProgress });
    const xhr = lastXhr();

    xhr.emitUpload("progress", { lengthComputable: true, loaded: 25, total: 100 });
    xhr.emitUpload("progress", { lengthComputable: true, loaded: 51, total: 100 });
    xhr.emitUpload("progress", { lengthComputable: false, loaded: 1, total: 2 });
    expect(onProgress).toHaveBeenNthCalledWith(1, 25);
    expect(onProgress).toHaveBeenNthCalledWith(2, 51);
    expect(onProgress).toHaveBeenCalledTimes(2);

    xhr.status = 200;
    xhr.responseText = "{}";
    xhr.emit("load");
    await expect(p).resolves.toBeUndefined();
  });
});
