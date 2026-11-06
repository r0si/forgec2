import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useSettingsData } from "./useSettingsData";

const { getMock, toastError, tMock } = vi.hoisted(() => ({
  getMock: vi.fn(),
  toastError: vi.fn(),
  tMock: (key: string) => key,
}));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
  },
  getRateLimitRetryAfter: () => 7,
}));

vi.mock("sonner", () => ({
  toast: { error: (...args: unknown[]) => toastError(...args), success: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t: tMock }),
}));

describe("useSettingsData", () => {
  beforeEach(() => {
    getMock.mockReset();
    toastError.mockReset();
  });

  it("keeps the server's malleable response headers so a save cannot wipe them", async () => {
    getMock.mockResolvedValueOnce({
      malleable_enabled: true,
      malleable_status: 200,
      malleable_ct: "application/json",
      malleable_headers: "Server: nginx/1.24.0\nX-Powered-By: ASP.NET",
      malleable_prepend: "<html><body><!--",
      malleable_append: "--></body></html>",
    });

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(getMock).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(result.current.loaded).toBe(true));

    expect(result.current.malleableForm.headers_text).toBe(
      "Server: nginx/1.24.0\nX-Powered-By: ASP.NET",
    );
    expect(result.current.malleableForm.prepend).toBe("<html><body><!--");
    expect(result.current.malleableForm.append).toBe("--></body></html>");
  });

  it("hydrates the fleet-default beacon randomization fields from the server", async () => {
    getMock.mockResolvedValueOnce({
      default_interval: 15,
      default_jitter: 30,
      beacon_randomization: "gaussian",
      max_random_interval_ms: 2500,
    });
    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.agentForm.beacon_randomization).toBe("gaussian");
    expect(result.current.agentForm.max_random_interval_ms).toBe(2500);
  });

  it("defaults the randomization fields when the server omits them", async () => {
    getMock.mockResolvedValueOnce({ default_interval: 8, default_jitter: 12 });
    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.agentForm.beacon_randomization).toBe("linear");
    expect(result.current.agentForm.max_random_interval_ms).toBe(0);
  });

  it("falls back to an empty header field only when the server sends no headers", async () => {
    getMock.mockResolvedValueOnce({ malleable_enabled: false });

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.loaded).toBe(true));

    expect(result.current.malleableForm.headers_text).toBe("");
  });

  it("never reports loaded after a failed first read, so saves stay blocked", async () => {
    getMock.mockRejectedValueOnce(new Error("settings endpoint is down"));

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.error).toBe("settings endpoint is down"));

    expect(result.current.loaded).toBe(false);
    expect(result.current.loading).toBe(false);
  });

  it("keeps loaded true after a later refresh fails, so stale values are flagged not discarded", async () => {
    getMock
      .mockResolvedValueOnce({ default_interval: 9 })
      .mockRejectedValueOnce(new Error("refresh failed"));

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.loaded).toBe(true));
    expect(result.current.agentForm.interval).toBe(9);

    await result.current.loadSettings();
    await waitFor(() => expect(result.current.error).toBe("refresh failed"));

    expect(result.current.loaded).toBe(true);
    expect(result.current.agentForm.interval).toBe(9);
  });

  it("maps a 5xx and reports the shared server copy on both the error state and the toast", async () => {
    getMock.mockRejectedValueOnce({ status: 500, message: "internals leaked" });

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.error).toBe("common.server_error"));

    expect(toastError).toHaveBeenCalledWith("common.server_error");
    expect(result.current.loading).toBe(false);
    expect(result.current.loaded).toBe(false);
  });

  it("maps a browser network failure on both surfaces", async () => {
    getMock.mockRejectedValueOnce(new TypeError("Failed to fetch"));

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.error).toBe("common.network_error"));

    expect(toastError).toHaveBeenCalledWith("common.network_error");
  });

  it("falls back to the settings copy when the failure carries no message", async () => {
    getMock.mockRejectedValueOnce(new Error(""));

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.error).toBe("settings.toast.load_failed"));

    expect(toastError).toHaveBeenCalledWith("settings.toast.load_failed");
  });

  it("resolves a rate limit through the shared copy on both surfaces", async () => {
    getMock.mockRejectedValueOnce({ status: 429, message: "slow down" });

    const { result } = renderHook(() => useSettingsData());
    await waitFor(() => expect(result.current.error).toBe("common.rate_limited"));

    expect(toastError).toHaveBeenCalledWith("common.rate_limited");
  });
});
