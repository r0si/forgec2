import { describe, it, expect, vi } from "vitest";
import { act, renderHook, waitFor } from "@testing-library/react";
import { useMutation } from "./useMutation";

function deferred<T>() {
  let resolve!: (v: T) => void;
  let reject!: (e: unknown) => void;
  const promise = new Promise<T>((res, rej) => { resolve = res; reject = rej; });
  return { promise, resolve, reject };
}

describe("useMutation", () => {
  it("tracks pending through the mutation and reports the result", async () => {
    const d = deferred<number>();
    const onSuccess = vi.fn();
    const { result } = renderHook(() => useMutation<[], number>({
      fn: () => d.promise,
      onSuccess,
    }));

    let mutateDone = false;
    act(() => {
      result.current.mutate().then(() => { mutateDone = true; });
    });
    expect(result.current.isPending).toBe(true);

    await act(async () => {
      d.resolve(42);
      await d.promise;
    });
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(mutateDone).toBe(true);
    expect(onSuccess).toHaveBeenCalledWith(42);
  });

  it("records the error and invokes onError; reset clears it", async () => {
    const onError = vi.fn();
    const { result } = renderHook(() => useMutation<[], void>({
      fn: () => Promise.reject(new Error("boom")),
      onError,
    }));

    await act(async () => {
      await result.current.mutate();
    });
    expect(result.current.error).toEqual(new Error("boom"));
    expect(onError).toHaveBeenCalledWith(new Error("boom"));

    act(() => result.current.reset());
    expect(result.current.error).toBeNull();
    expect(result.current.isPending).toBe(false);
  });

  it("ignores stale completions when a newer mutation supersedes an older one", async () => {
    const first = deferred<string>();
    const second = deferred<string>();
    const onSuccess = vi.fn();
    const fn = vi
      .fn()
      .mockImplementationOnce(() => first.promise)
      .mockImplementationOnce(() => second.promise);
    const { result } = renderHook(() => useMutation<[], string>({ fn, onSuccess }));

    let p1!: Promise<string | undefined>;
    act(() => { p1 = result.current.mutate(); });
    act(() => { void result.current.mutate(); });
    expect(result.current.isPending).toBe(true);

    await act(async () => {
      first.resolve("stale");
      await p1;
    });
    // earlier completion must not fire callbacks or flip pending
    expect(onSuccess).not.toHaveBeenCalled();
    expect(result.current.isPending).toBe(true);

    await act(async () => {
      second.resolve("fresh");
    });
    await waitFor(() => expect(result.current.isPending).toBe(false));
    expect(onSuccess).toHaveBeenCalledWith("fresh");
  });

  it("does not fire lifecycle callbacks after unmount", async () => {
    const pending = deferred<string>();
    const onSuccess = vi.fn();
    const onSettled = vi.fn();
    const { result, unmount } = renderHook(() => useMutation<[], string>({
      fn: () => pending.promise,
      onSuccess,
      onSettled,
    }));

    let completion!: Promise<string | undefined>;
    act(() => { completion = result.current.mutate(); });
    unmount();
    pending.resolve("late");
    await completion;

    expect(onSuccess).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();
  });

  it("does not start an operation through a retained callback after unmount", async () => {
    const fn = vi.fn().mockResolvedValue("unexpected");
    const onSuccess = vi.fn();
    const onSettled = vi.fn();
    const { result, unmount } = renderHook(() => useMutation({ fn, onSuccess, onSettled }));
    const mutate = result.current.mutate;
    unmount();
    expect(await mutate()).toBeUndefined();
    expect(fn).not.toHaveBeenCalled();
    expect(onSuccess).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();
  });

  it("ignores a late failure after reset without reviving error or pending state", async () => {
    const pending = deferred<string>();
    const onError = vi.fn();
    const onSettled = vi.fn();
    const { result } = renderHook(() => useMutation({ fn: () => pending.promise, onError, onSettled }));
    let completion!: Promise<string | undefined>;
    act(() => { completion = result.current.mutate(); });
    act(() => result.current.reset());
    await act(async () => {
      pending.reject(new Error("stale"));
      await completion;
    });
    expect(result.current.error).toBeNull();
    expect(result.current.isPending).toBe(false);
    expect(onError).not.toHaveBeenCalled();
    expect(onSettled).not.toHaveBeenCalled();
  });
});
