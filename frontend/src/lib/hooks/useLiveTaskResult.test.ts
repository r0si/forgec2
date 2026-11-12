import { act, renderHook } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { runTask, type TaskStatus } from "@/lib/api";

const mocks = vi.hoisted(() => ({
  t: vi.fn((key: string) => `T:${key}`),
}));

vi.mock("@/lib/api", () => ({ runTask: vi.fn() }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: mocks.t }) }));

import { useLiveTaskResult } from "./useLiveTaskResult";

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

const running = (id: number): TaskStatus => ({ id, status: "running" });

describe("useLiveTaskResult", () => {
  beforeEach(() => {
    vi.mocked(runTask).mockReset();
    mocks.t.mockClear();
  });

  it("follows the live frames from dispatch to completion", async () => {
    const d = deferred<TaskStatus>();
    let lastOpts: Parameters<typeof runTask>[2];
    vi.mocked(runTask).mockImplementation((_agentId, _path, opts) => {
      lastOpts = opts;
      opts?.onStatus?.({ id: 7, status: "pending_approval" });
      return d.promise;
    });

    const { result } = renderHook(() => useLiveTaskResult());
    let p: Promise<TaskStatus | null> = Promise.resolve(null);
    await act(async () => {
      p = result.current.run("a1", "/tasks/shell", { command: "whoami" });
    });

    expect(result.current.status).toBe("pending");
    expect(result.current.taskId).toBe(7);
    expect(result.current.running).toBe(true);
    expect(runTask).toHaveBeenCalledWith(
      "a1",
      "/tasks/shell",
      expect.objectContaining({ body: { command: "whoami" } }),
    );

    await act(async () => {
      lastOpts?.onStatus?.({ id: 7, status: "sent" });
    });
    expect(result.current.status).toBe("running");

    await act(async () => {
      lastOpts?.onStatus?.({ id: 7, status: "running", result: "partial output" });
    });
    expect(result.current.result).toBe("partial output");

    let final: TaskStatus | null = null;
    await act(async () => {
      d.resolve({ id: 7, status: "completed", result: "final output" });
      final = await p;
    });
    expect(final).toMatchObject({ id: 7, status: "completed" });
    expect(result.current.status).toBe("completed");
    expect(result.current.result).toBe("final output");
    expect(result.current.running).toBe(false);
  });

  it("reports a cancelled task as cancelled, not failed", async () => {
    vi.mocked(runTask).mockResolvedValue({ id: 4, status: "cancelled" });
    const { result } = renderHook(() => useLiveTaskResult());

    await act(async () => {
      await result.current.run("a", "/p", {});
    });

    expect(result.current.status).toBe("cancelled");
    expect(result.current.error).toBe("T:common.task_cancelled");
    expect(mocks.t).toHaveBeenCalledWith("common.task_cancelled");
  });

  it("keeps the server's error text for a cancelled task", async () => {
    vi.mocked(runTask).mockResolvedValue({ id: 4, status: "cancelled", error: "cancelled by operator" });
    const { result } = renderHook(() => useLiveTaskResult());

    await act(async () => {
      await result.current.run("a", "/p", {});
    });

    expect(result.current.status).toBe("cancelled");
    expect(result.current.error).toBe("cancelled by operator");
    expect(mocks.t).not.toHaveBeenCalled();
  });

  it("maps a silent agent to timeout and other failures to failed", async () => {
    vi.mocked(runTask).mockImplementation(() => Promise.reject(new Error("agent did not respond in time")));
    const { result } = renderHook(() => useLiveTaskResult());

    let out: TaskStatus | null = null;
    await act(async () => {
      out = await result.current.run("a", "/p", {});
    });
    expect(out).toBeNull();
    expect(result.current.status).toBe("timeout");
    expect(result.current.error).toBe("agent did not respond in time");

    vi.mocked(runTask).mockImplementation(() => Promise.reject("plain failure"));
    await act(async () => {
      out = await result.current.run("a", "/p", {});
    });
    expect(result.current.status).toBe("failed");
    expect(result.current.error).toBe("plain failure");
  });

  it("forwards the configured timeout to the dispatch", async () => {
    vi.mocked(runTask).mockResolvedValue({ id: 1, status: "completed", result: "ok" });
    const { result } = renderHook(() => useLiveTaskResult({ timeoutMs: 5000 }));

    await act(async () => {
      await result.current.run("a", "/p", {});
    });

    expect(runTask).toHaveBeenCalledWith("a", "/p", expect.objectContaining({ timeoutMs: 5000 }));
  });

  it("aborts the in-flight request on unmount", async () => {
    let signal: AbortSignal | undefined;
    vi.mocked(runTask).mockImplementation((_agentId, _path, opts) => {
      signal = opts?.signal;
      opts?.onStatus?.(running(1));
      return new Promise<TaskStatus>(() => {});
    });
    const { result, unmount } = renderHook(() => useLiveTaskResult());

    await act(async () => {
      void result.current.run("a", "/p", {});
    });
    expect(result.current.status).toBe("running");
    expect(signal?.aborted).toBe(false);

    unmount();
    expect(signal?.aborted).toBe(true);
  });

  it("reset() returns to idle and stale completions cannot repaint it", async () => {
    const d = deferred<TaskStatus>();
    vi.mocked(runTask).mockImplementation((_agentId, _path, opts) => {
      opts?.onStatus?.(running(9));
      return d.promise;
    });
    const { result } = renderHook(() => useLiveTaskResult());

    let p: Promise<TaskStatus | null> = Promise.resolve(null);
    await act(async () => {
      p = result.current.run("a", "/p", {});
    });
    expect(result.current.status).toBe("running");
    expect(result.current.taskId).toBe(9);

    act(() => {
      result.current.reset();
    });
    expect(result.current.status).toBe("idle");
    expect(result.current.taskId).toBeNull();
    expect(result.current.running).toBe(false);

    let out: TaskStatus | null = null;
    await act(async () => {
      d.resolve({ id: 9, status: "completed", result: "late" });
      out = await p;
    });
    expect(out).toBeNull();
    expect(result.current.status).toBe("idle");
    expect(result.current.result).toBe("");
    expect(result.current.error).toBe("");
  });
});
