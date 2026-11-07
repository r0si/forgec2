import { act, render, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { probeSessionExpiry } from "./api";
import { useWS, WebSocketProvider, WS_OUTBOX_DROP_EVENT } from "./wsContext";

vi.mock("./api", () => ({
  probeSessionExpiry: vi.fn(),
}));

class FakeWebSocket {
  static instances: FakeWebSocket[] = [];
  static CONNECTING = 0;
  static OPEN = 1;
  static CLOSED = 3;
  readyState = FakeWebSocket.CONNECTING;
  sent: string[] = [];
  onopen: (() => void) | null = null;
  onclose: ((e: { code: number }) => void) | null = null;
  onmessage: ((e: { data: string }) => void) | null = null;
  onerror: (() => void) | null = null;
  constructor(public url: string) {
    FakeWebSocket.instances.push(this);
  }
  send(data: string) {
    this.sent.push(data);
  }
  close() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1000 });
  }
  triggerOpen() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
  }
  triggerClose(code: number) {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code });
  }
}

type Ctx = ReturnType<typeof useWS>;

function renderProvider() {
  // Holder stays current across re-renders: the useWS() value is a fresh
  // object per commit, so asserting on a destructured snapshot would test the
  // initial render forever.
  const holder: { value: Ctx | null } = { value: null };
  function Probe() {
    holder.value = useWS();
    return null;
  }
  const view = render(
    <WebSocketProvider>
      <Probe />
    </WebSocketProvider>,
  );
  if (!holder.value) throw new Error("context not captured");
  return { get ctx() { return holder.value as Ctx; }, holder, ...view };
}

describe("WebSocketProvider outbox", () => {
  beforeEach(() => {
    FakeWebSocket.instances.length = 0;
    vi.stubGlobal("WebSocket", FakeWebSocket);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.clearAllMocks();
  });

  it("buffers offline sends, sheds oldest past the cap, and reports drops", () => {
    const drops: number[] = [];
    const onDrop = (e: Event) => drops.push((e as CustomEvent<{ dropped: number }>).detail.dropped);
    window.addEventListener(WS_OUTBOX_DROP_EVENT, onDrop);
    try {
      const { ctx, unmount } = renderProvider();
      // Socket is CONNECTING: everything buffers.
      act(() => {
        for (let i = 0; i < 52; i++) ctx.send({ n: i });
      });
      expect(drops).toEqual([1, 2]);
      act(() => {
        FakeWebSocket.instances[0].triggerOpen();
      });
      const sent = FakeWebSocket.instances[0].sent.map((s) => (JSON.parse(s) as { n: number }).n);
      expect(sent).toHaveLength(50);
      expect(sent[0]).toBe(2);
      expect(sent[49]).toBe(51);
      unmount();
    } finally {
      window.removeEventListener(WS_OUTBOX_DROP_EVENT, onDrop);
    }
  });

  it("flushes critical frames ahead of the normal backlog", () => {
    const { ctx, unmount } = renderProvider();
    try {
      act(() => {
        ctx.send({ n: 1 });
        ctx.send("arm", { critical: true });
        ctx.send({ n: 2 });
      });
      act(() => {
        FakeWebSocket.instances[0].triggerOpen();
      });
      expect(FakeWebSocket.instances[0].sent).toEqual(['"arm"', '{"n":1}', '{"n":2}']);
      unmount();
    } catch (e) {
      unmount();
      throw e;
    }
  });

  it("probes the session on abnormal closure instead of only after backoff", () => {
    const { unmount } = renderProvider();
    try {
      act(() => {
        FakeWebSocket.instances[0].triggerOpen();
      });
      act(() => {
        FakeWebSocket.instances[0].triggerClose(1006);
      });
      expect(vi.mocked(probeSessionExpiry)).toHaveBeenCalled();
      unmount();
    } catch (e) {
      unmount();
      throw e;
    }
  });

  it("goes offline fast when the backend is up but the socket 404s twice", async () => {
    const fetchMock = vi.fn(async () => ({ ok: true, status: 200 }) as Response);
    vi.stubGlobal("fetch", fetchMock);
    const { holder, unmount } = renderProvider();
    try {
      await act(async () => {
        FakeWebSocket.instances[0].triggerOpen();
      });
      await waitFor(() => expect(holder.value?.connected).toBe(true));

      // First abnormal closure: one health-ok failure is not enough yet.
      await act(async () => {
        FakeWebSocket.instances[0].triggerClose(1006);
      });
      await waitFor(() => expect(fetchMock).toHaveBeenCalled());
      expect(holder.value?.reconnectFailed).toBe(false);

      // The fast backoff schedules a reconnect (~1s). Wait for the 2nd socket.
      await waitFor(() => expect(FakeWebSocket.instances.length).toBeGreaterThan(1), { timeout: 8000 });
      await act(async () => {
        FakeWebSocket.instances[FakeWebSocket.instances.length - 1].triggerClose(1006);
      });
      // Two backend-up failures in a row: Offline immediately, no 10min wait.
      await waitFor(() => expect(holder.value?.reconnectFailed).toBe(true), { timeout: 8000 });
      unmount();
    } catch (e) {
      unmount();
      throw e;
    }
  });
});
