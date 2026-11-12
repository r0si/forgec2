import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, renderHook } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { downloadText } from "@/lib/download";
import { useAgentBulkOps, parseSleepInput } from "./useAgentBulkOps";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), del: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  return { getLocale: () => "en", useI18n: () => ({ t }) };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), info: vi.fn(), warning: vi.fn() }),
}));

vi.mock("@/lib/download", () => ({ downloadText: vi.fn() }));

const errorToast = vi.mocked(toast.error);

describe("parseSleepInput", () => {
  it("accepts in-range interval/jitter", () => {
    expect(parseSleepInput("30", "20")).toEqual({ interval: 30, jitter: 20 });
    expect(parseSleepInput("1", "0")).toEqual({ interval: 1, jitter: 0 });
    expect(parseSleepInput("86400", "100")).toEqual({ interval: 86400, jitter: 100 });
  });

  it("rejects out-of-range values", () => {
    expect(parseSleepInput("0", "20")).toBeNull();
    expect(parseSleepInput("86401", "20")).toBeNull();
    expect(parseSleepInput("30", "-1")).toBeNull();
    expect(parseSleepInput("30", "101")).toBeNull();
  });

  it("rejects non-numeric input", () => {
    expect(parseSleepInput("", "20")).toBeNull();
    expect(parseSleepInput("abc", "20")).toBeNull();
    expect(parseSleepInput("30", "abc")).toBeNull();
    // Number("") is 0: empty jitter coerces to 0, same as the legacy inline check.
    expect(parseSleepInput("30", "")).toEqual({ interval: 30, jitter: 0 });
  });
});

function makeDeps(overrides: Record<string, unknown> = {}) {
  const setActionMsg = vi.fn();
  const setConfirm = vi.fn();
  const deps = {
    t: (key: string) => key,
    selected: new Set(["a1"]),
    setSelected: vi.fn(),
    loadBeacons: vi.fn(),
    setActionMsg,
    setConfirm,
    setScreenshotConfirm: vi.fn(),
    openBatchSleep: vi.fn(),
    closeBatchSleep: vi.fn(),
    batchSleepInterval: "30",
    batchSleepJitter: "10",
    cmdType: "shell",
    cmdText: "whoami",
    closeCmdModal: vi.fn(),
    quickSleepAgent: null as { id: string; hostname: string } | null,
    sleepInterval: "30",
    sleepJitter: "10",
    closeQuickSleep: vi.fn(),
    editingNotesId: null as string | null,
    editNotesText: "",
    closeNotesEdit: vi.fn(),
    searchQuery: "",
    statusFilter: "",
    osFilter: "",
    tagFilter: "",
    linkedFilter: "",
    sortKey: "",
    sortDir: "",
    failureWindow: "24h",
    ...overrides,
  };
  return { deps, setActionMsg, setConfirm };
}

beforeEach(() => {
  vi.clearAllMocks();
});

describe("useAgentBulkOps failure reporting", () => {
  it("surfaces the backend reason when a kill is rejected", async () => {
    const { deps, setActionMsg, setConfirm } = makeDeps();
    const { result } = renderHook(() => useAgentBulkOps(deps));

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "agent offline" });
    await act(async () => {
      await result.current.killAgent("a1");
    });

    expect(setActionMsg).toHaveBeenCalledWith("agent offline");
    expect(setConfirm).toHaveBeenCalledWith(null);
  });

  it("resolves mapped statuses for a rejected batch delete", async () => {
    const { deps, setActionMsg } = makeDeps();
    const { result } = renderHook(() => useAgentBulkOps(deps));

    vi.mocked(api.postJson).mockRejectedValue({ status: 500, message: "boom" });
    await act(async () => {
      await result.current.batchDelete();
    });

    expect(setActionMsg).toHaveBeenCalledWith("common.server_error");
  });

  it("surfaces the backend reason when a quick sleep is rejected", async () => {
    const { deps, setActionMsg } = makeDeps({ quickSleepAgent: { id: "a1", hostname: "host1" } });
    const { result } = renderHook(() => useAgentBulkOps(deps));

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "interval too low" });
    await act(async () => {
      await result.current.submitQuickSleep();
    });

    expect(setActionMsg).toHaveBeenCalledWith("interval too low");
  });

  it("reports a successful batch run", async () => {
    const { deps, setActionMsg } = makeDeps();
    const { result } = renderHook(() => useAgentBulkOps(deps));

    vi.mocked(api.postJson).mockResolvedValue({ success: true, tasks_created: 2 });
    await act(async () => {
      await result.current.runBatch({ agent_ids: ["a1"], task_type: "screenshot" });
    });

    expect(setActionMsg).toHaveBeenCalledWith("agents.batch_result");
  });

  it("exports the failure counts the list shows", async () => {
    const downloads: string[] = [];
    vi.mocked(downloadText).mockImplementation((csv: string) => {
      downloads.push(csv);
    });
    vi.mocked(api.get).mockResolvedValue({
      success: true,
      data: {
        agents: [{ id: "a1", hostname: "web-01", ip: "10.0.0.5", status: "offline", failure_count: 3 }],
        total: 1,
      },
    });

    const { deps } = makeDeps();
    const { result } = renderHook(() => useAgentBulkOps(deps));
    await act(async () => {
      await result.current.exportCSV();
    });

    // The file has to answer "which beacon is failing?" like the screen does.
    const csv = downloads.join("\n");
    expect(csv).toContain("Failures");
    expect(csv).toContain("web-01");
    expect(csv).toContain('"3"');
    // And the export asks the server for the counts it writes.
    const request = String(vi.mocked(api.get).mock.calls[0][0]);
    expect(request).toContain("failures=1");
    expect(request).toContain("failures_window=24h");
  });

  it("routes an export failure through the normalizer", async () => {
    const { deps } = makeDeps();
    const { result } = renderHook(() => useAgentBulkOps(deps));

    vi.mocked(api.get).mockRejectedValue({ status: 500, message: "boom" });
    await act(async () => {
      await result.current.exportCSV();
    });

    expect(errorToast).toHaveBeenCalledWith("common.server_error");
    expect(toast.info).not.toHaveBeenCalled();
  });
});
