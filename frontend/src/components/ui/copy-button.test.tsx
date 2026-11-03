import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import { CopyButton } from "./copy-button";
import { toast } from "sonner";

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));
vi.mock("sonner", () => ({ toast: { error: vi.fn() } }));
const originalExecCommand = Object.getOwnPropertyDescriptor(document, "execCommand");

afterEach(() => {
  cleanup();
  vi.restoreAllMocks();
  vi.unstubAllGlobals();
  vi.clearAllMocks();
  vi.useRealTimers();
  if (originalExecCommand) Object.defineProperty(document, "execCommand", originalExecCommand);
  else Reflect.deleteProperty(document, "execCommand");
});

describe("CopyButton fallback", () => {
  for (const outcome of ["success", "rejected", "throw"] as const) {
    it(`cleans up and reports ${outcome} truthfully`, () => {
      vi.stubGlobal("navigator", { clipboard: undefined });
      const execCommand = vi.fn(() => {
        if (outcome === "throw") throw new Error("denied");
        return outcome === "success";
      });
      Object.defineProperty(document, "execCommand", { configurable: true, value: execCommand });
      const onError = vi.fn();
      render(<CopyButton text="sample" onError={onError}>{copied => copied ? "copied" : "copy"}</CopyButton>);
      const button = screen.getByRole("button");
      button.focus();
      fireEvent.click(button);
      expect(document.querySelector("textarea")).toBeNull();
      expect(document.activeElement).toBe(button);
      expect(button.textContent).toBe(outcome === "success" ? "copied" : "copy");
      expect(button.getAttribute("aria-label")).toBe(outcome === "success" ? "common.copied" : "common.copy");
      expect(screen.getByRole("status").textContent).toBe(outcome === "success" ? "common.copied" : "");
      expect(onError).toHaveBeenCalledTimes(outcome === "success" ? 0 : 1);
      expect(toast.error).toHaveBeenCalledTimes(outcome === "success" ? 0 : 1);
    });
  }

  it("restarts feedback on a later copy and clears its timer on unmount", () => {
    vi.useFakeTimers();
    const schedule = vi.spyOn(globalThis, "setTimeout");
    const clear = vi.spyOn(globalThis, "clearTimeout");
    vi.stubGlobal("navigator", { clipboard: undefined });
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => true) });
    const { unmount } = render(<CopyButton text="sample">{copied => copied ? "copied" : "copy"}</CopyButton>);
    const button = screen.getByRole("button");
    fireEvent.click(button);
    act(() => vi.advanceTimersByTime(1000));
    fireEvent.click(button);
    act(() => vi.advanceTimersByTime(500));
    expect(button.textContent).toBe("copied");
    act(() => vi.advanceTimersByTime(1000));
    expect(button.textContent).toBe("copy");
    fireEvent.click(button);
    const ownTimerIndex = schedule.mock.calls.findLastIndex(([, delay]) => delay === 1500);
    expect(ownTimerIndex).toBeGreaterThanOrEqual(0);
    const ownTimer = schedule.mock.results[ownTimerIndex].value;
    unmount();
    expect(clear).toHaveBeenCalledWith(ownTimer);
  });
});
