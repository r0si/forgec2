import { act, renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { toast } from "sonner";
import { AI_ERROR_TOOL_NAME, AI_REGENERATE_TOOL_NAME } from "./types";
import { restoreSessionMessages, useAISessions } from "./useAISessions";

const { getMock, delMock, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  delMock: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    del: (...args: unknown[]) => delMock(...args),
  },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("sonner", () => ({
  toast: { error: vi.fn(), success: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t: tMock }),
}));

describe("restoreSessionMessages", () => {
  it("restores only the latest regenerated branch", () => {
    const messages = restoreSessionMessages([
      { role: "user", content: "first" },
      { role: "assistant", content: "first answer" },
      { role: "user", content: "retry me" },
      { role: "assistant", content: "old answer" },
      { role: "user", content: "retry me", tool_name: AI_REGENERATE_TOOL_NAME },
      { role: "assistant", content: "new answer" },
    ]);

    expect(messages.map((message) => message.content)).toEqual([
      "first",
      "first answer",
      "retry me",
      "new answer",
    ]);
    expect(messages[2].regenerated).toBe(true);
  });

  it("drops invalid roles and malformed trace records", () => {
    const messages = restoreSessionMessages([
      { role: "system", content: "not allowed" },
      { role: "assistant", content: "{bad", tool_name: "__ai_trace__" },
      { role: "user", content: "valid" },
    ]);
    expect(messages).toHaveLength(1);
    expect(messages[0].content).toBe("valid");
  });

  it("restores interrupted assistant responses as retryable errors", () => {
    const messages = restoreSessionMessages([
      { role: "user", content: "question" },
      { role: "assistant", content: "partial response", tool_name: AI_ERROR_TOOL_NAME },
    ]);
    expect(messages[1]).toMatchObject({
      role: "assistant",
      content: "partial response",
      error: true,
    });
  });
});

describe("useAISessions failure surfaces", () => {
  beforeEach(() => {
    getMock.mockReset();
    delMock.mockReset();
    vi.mocked(toast.error).mockClear();
  });

  it("reads the sessions list from the single-envelope sessions field", async () => {
    // api.get unwraps {"success":true,"data":...}; the payload is
    // {sessions, next_cursor}. The old double envelope put the list at
    // data.data, which read back as undefined and emptied the sidebar.
    getMock
      .mockResolvedValueOnce({
        sessions: [{ id: 7, title: "gamma", updated_at: "2026-11-01T10:00:00.000Z" }],
        next_cursor: "",
      })
      .mockResolvedValueOnce({
        messages: [{ role: "user", content: "hello" }],
        next_cursor: "",
      });

    const pushed: { role: string; content: string }[][] = [];
    const { result } = renderHook(() => useAISessions((updater) => {
      const next = typeof updater === "function" ? updater([]) : updater;
      pushed.push(next as { role: string; content: string }[]);
    }));
    await waitFor(() => expect(result.current.sessionsLoading).toBe(false));
    expect(result.current.sessions.map((session) => session.id)).toEqual([7]);

    let selected: boolean | undefined;
    await act(async () => {
      selected = await result.current.selectSession(7);
    });
    expect(selected).toBe(true);
    const last = pushed[pushed.length - 1];
    expect(last.map((message) => message.content)).toEqual(["hello"]);
  });

  it("maps a failed session list read onto the shared copy on both surfaces", async () => {
    getMock.mockRejectedValueOnce(Object.assign(new Error("boom"), { status: 500 }));

    const { result } = renderHook(() => useAISessions(vi.fn()));
    await waitFor(() => expect(result.current.sessionsError).toBe("common.server_error"));

    expect(vi.mocked(toast.error)).toHaveBeenCalledWith("common.server_error");
    expect(result.current.sessionsLoading).toBe(false);
  });

  it("maps a failed message load and leaves the conversation unchanged", async () => {
    getMock
      .mockResolvedValueOnce([{ id: 1, title: "alpha", updated_at: "2026-10-01T10:00:00.000Z" }])
      .mockRejectedValueOnce(Object.assign(new Error("boom"), { status: 500 }));

    const { result } = renderHook(() => useAISessions(vi.fn()));
    await waitFor(() => expect(result.current.sessionsLoading).toBe(false));
    vi.mocked(toast.error).mockClear();

    let selected: boolean | undefined;
    await act(async () => {
      selected = await result.current.selectSession(1);
    });

    expect(selected).toBe(false);
    expect(vi.mocked(toast.error)).toHaveBeenCalledWith("common.server_error");
    expect(result.current.activeSessionId).toBeNull();
  });

  it("maps a failed delete onto the shared copy and keeps the session listed", async () => {
    getMock.mockResolvedValueOnce([{ id: 1, title: "alpha", updated_at: "2026-10-01T10:00:00.000Z" }]);
    delMock.mockRejectedValueOnce(Object.assign(new Error("boom"), { status: 500 }));

    const { result } = renderHook(() => useAISessions(vi.fn()));
    await waitFor(() => expect(result.current.sessionsLoading).toBe(false));
    vi.mocked(toast.error).mockClear();

    let deleted: boolean | undefined;
    await act(async () => {
      deleted = await result.current.deleteSession(1);
    });

    expect(deleted).toBe(false);
    expect(vi.mocked(toast.error)).toHaveBeenCalledWith("common.server_error");
    expect(result.current.sessions.map((session) => session.id)).toContain(1);
  });
});
