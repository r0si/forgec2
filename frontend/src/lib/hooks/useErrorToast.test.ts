import { act, renderHook, waitFor } from "@testing-library/react";
import { describe, it, expect, vi, beforeEach } from "vitest";

const mocks = vi.hoisted(() => ({
  toast: vi.fn(),
  warn: vi.fn(),
  t: vi.fn((key: string) => `T:${key}`),
}));

vi.mock("sonner", () => ({ toast: { error: mocks.toast, warning: mocks.warn } }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: mocks.t }) }));

import { useErrorToast } from "./useErrorToast";

describe("useErrorToast", () => {
  beforeEach(() => {
    mocks.toast.mockClear();
    mocks.warn.mockClear();
    mocks.t.mockClear();
  });

  it("returns aborted errors without showing a toast", () => {
    const { result } = renderHook(() => useErrorToast());
    let returned: { kind: string } | undefined;

    act(() => {
      returned = result.current.toastError(new DOMException("go away", "AbortError"));
    });

    expect(mocks.toast).not.toHaveBeenCalled();
    expect(returned?.kind).toBe("aborted");
  });

  it("resolves mapped API errors through t", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.toastError({ status: 403, message: "nope" });
    });

    expect(mocks.t).toHaveBeenCalledWith("common.forbidden", undefined);
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("T:common.forbidden"));
  });

  it("falls back to the raw message for unmapped errors", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.toastError(new Error("disk offline"));
    });

    expect(mocks.t).not.toHaveBeenCalled();
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("disk offline"));
  });

  it("falls back to the fallback key when the message is empty", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.toastError(new Error(""), "common.error");
    });

    expect(mocks.t).toHaveBeenCalledWith("common.error");
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("T:common.error"));
  });

  it("interpolates fallback params into the fallback key", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.toastError(new Error(""), "settings.emergency.failed", { action: "arm" });
    });

    expect(mocks.t).toHaveBeenCalledWith("settings.emergency.failed", { action: "arm" });
    await waitFor(() => expect(mocks.toast).toHaveBeenCalledWith("T:settings.emergency.failed"));
  });

  it("warnError keeps a 409 approval outcome a warning with the backend text", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.warnError(
        { status: 409, message: "destructive command needs approval" },
        "ai.toast.assist_failed",
      );
    });

    await waitFor(() => expect(mocks.warn).toHaveBeenCalledWith("destructive command needs approval"));
    expect(mocks.toast).not.toHaveBeenCalled();
    // 409 is outcome-specific: the fallback key must not be consulted.
    expect(mocks.t).not.toHaveBeenCalled();
  });

  it("warnError resolves a message-less conflict through the fallback key", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.warnError({ status: 409, message: "" }, "agents.detail_command_send_failed");
    });

    // An empty approval notice must not toast an empty string.
    await waitFor(() => expect(mocks.warn).toHaveBeenCalledWith("T:agents.detail_command_send_failed"));
  });

  it("warnError maps server failures exactly like toastError does", async () => {
    const { result } = renderHook(() => useErrorToast());

    act(() => {
      result.current.warnError({ status: 503, message: "boom" });
    });

    await waitFor(() => expect(mocks.warn).toHaveBeenCalledWith("T:common.server_error"));
    expect(mocks.toast).not.toHaveBeenCalled();
  });

  it("warnError absorbs aborted errors without a toast", () => {
    const { result } = renderHook(() => useErrorToast());
    let returned: { kind: string } | undefined;

    act(() => {
      returned = result.current.warnError(new DOMException("go away", "AbortError"));
    });

    expect(mocks.warn).not.toHaveBeenCalled();
    expect(returned?.kind).toBe("aborted");
  });
});
