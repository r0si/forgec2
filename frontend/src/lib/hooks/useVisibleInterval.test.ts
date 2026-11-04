import { act, renderHook } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { useVisibleInterval } from "./useVisibleInterval";

describe("useVisibleInterval", () => {
  afterEach(() => {
    vi.restoreAllMocks();
    vi.useRealTimers();
  });

  it("does not start in a hidden tab and catches up when visible", async () => {
    vi.useFakeTimers();
    let hidden = true;
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    const callback = vi.fn();
    renderHook(() => useVisibleInterval(callback, 1000));

    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(callback).not.toHaveBeenCalled();

    hidden = false;
    act(() => document.dispatchEvent(new Event("visibilitychange")));
    expect(callback).toHaveBeenCalledTimes(1);

    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(callback).toHaveBeenCalledTimes(2);
  });

  it("skips overlapping async ticks", async () => {
    vi.useFakeTimers();
    let release: (() => void) | undefined;
    const callback = vi.fn(() => new Promise<void>((resolve) => { release = resolve }));
    renderHook(() => useVisibleInterval(callback, 1000));

    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(callback).toHaveBeenCalledTimes(1);

    await act(async () => { await vi.advanceTimersByTimeAsync(3000); });
    expect(callback).toHaveBeenCalledTimes(1);

    act(() => release?.());
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(callback).toHaveBeenCalledTimes(2);
  });

  it("does not overlap an in-flight callback when its interval changes", async () => {
    vi.useFakeTimers();
    let release!: () => void;
    const pending = new Promise<void>(resolve => { release = resolve; });
    const callback = vi.fn(() => pending);
    const { rerender } = renderHook(({ delay }) => useVisibleInterval(callback, delay), {
      initialProps: { delay: 1000 },
    });
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    rerender({ delay: 500 });
    await act(async () => { await vi.advanceTimersByTimeAsync(1500); });
    expect(callback).toHaveBeenCalledTimes(1);
    await act(async () => { release(); await pending; });
    await act(async () => { await vi.advanceTimersByTimeAsync(500); });
    expect(callback).toHaveBeenCalledTimes(2);
  });

  it.each([NaN, Infinity, -Infinity, 0, -1, 2 ** 31])("does not schedule an invalid interval: %s", async delay => {
    vi.useFakeTimers();
    const callback = vi.fn();
    renderHook(() => useVisibleInterval(callback, delay));
    await act(async () => { await vi.advanceTimersByTimeAsync(30); });
    expect(callback).not.toHaveBeenCalled();
    expect(vi.getTimerCount()).toBe(0);
  });

  it("skips a queued tick when the tab is already hidden", async () => {
    vi.useFakeTimers();
    let hidden = false;
    vi.spyOn(document, "hidden", "get").mockImplementation(() => hidden);
    const callback = vi.fn();
    renderHook(() => useVisibleInterval(callback, 1000));
    hidden = true; // the visibilitychange event may arrive after a queued tick
    await act(async () => { await vi.advanceTimersByTimeAsync(1000); });
    expect(callback).not.toHaveBeenCalled();
  });
});
