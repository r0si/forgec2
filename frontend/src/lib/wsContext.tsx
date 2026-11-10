
import {
  createContext,
  useContext,
  useEffect,
  useRef,
  useState,
  useCallback,
  useMemo,
  type ReactNode,
} from "react";
import { DEFAULT_WS_HOST, DEFAULT_WS_PORT } from "./constants";
import { probeSessionExpiry } from "./api";
import { logger } from "./logger";

export interface WSMessage {
  type: string;
  [key: string]: unknown;
}

type WSListener = (msg: WSMessage) => void;

// Module-level listeners so non-React code (e.g. the task poller in api.ts)
// can observe WS messages without a React context. This is a second, distinct
// channel from the provider-level `listenersRef` set below — subscribers are
// never duplicated across channels, and each channel stays idempotent because
// both are backed by Sets (re-registering the same listener is a no-op).
const globalListeners = new Set<WSListener>();

// onWSMessage registers a global WS message listener and returns an unsubscribe fn.
export function onWSMessage(listener: WSListener): () => void {
  globalListeners.add(listener);
  return () => {
    globalListeners.delete(listener);
  };
}

/**
 * Test-only: push a synthetic frame through the module-global listener
 * channel (the same channel onWSMessage / subscribeTyped consume) without a
 * live socket. Never call from application code.
 */
export function __testDispatchWS(msg: WSMessage): void {
  for (const fn of globalListeners) {
    try { fn(msg); } catch { /* listener error: skip */ }
  }
}

interface WSContextValue {
  connected: boolean;
  reconnectFailed: boolean;
  /** Read-only demo deployment: operator sockets are disabled by design. */
  demoMode: boolean;
  subscribe: (listener: WSListener) => () => void;
  send: (data: unknown, opts?: { critical?: boolean }) => void;
  reconnect: () => void;
}

// Two contexts, one hook: the status fields (connected/reconnectFailed)
// change on every connect flap, while the methods keep stable identity.
// Splitting them means components that only call subscribe() (most pages)
// no longer re-render when the socket reconnects. useWS() keeps the
// combined shape so consumers are unchanged.
type WSStatusValue = Pick<WSContextValue, "connected" | "reconnectFailed" | "demoMode">;
type WSMethodsValue = Pick<WSContextValue, "subscribe" | "send" | "reconnect">;

const WSStatusContext = createContext<WSStatusValue>({ connected: false, reconnectFailed: false, demoMode: false });
const WSMethodsContext = createContext<WSMethodsValue | null>(null);

export function getHealthURL(): string {
  // Same origin as the socket: derive the HTTP(S) URL from getWSURL so the
  // Vite-dev backend-port override applies to both.
  const ws = getWSURL("/health");
  return ws.replace(/^ws:/, "http:").replace(/^wss:/, "https:");
}

export function getWSURL(path = "/ws"): string {
  const envURL = import.meta.env.VITE_FORGEC2_WS_URL;
  if (envURL) return envURL + path;
  const proto = typeof window !== "undefined" && window.location.protocol === "https:" ? "wss:" : "ws:";
  const host = typeof window !== "undefined" ? window.location.hostname : DEFAULT_WS_HOST;
  let port = import.meta.env.VITE_FORGEC2_BACKEND_PORT || (typeof window !== "undefined" ? window.location.port : DEFAULT_WS_PORT);
  if (!port) port = proto === "wss:" ? "443" : "80";
  return `${proto}//${host}:${port}${path}`;
}

const MAX_RECONNECT_DELAY = 30000;
const MAX_RECONNECT_ATTEMPTS = 20;
const BACKGROUND_RETRY_DELAY_MS = 60000;
const HEARTBEAT_INTERVAL_MS = 30000;
const HEARTBEAT_TIMEOUT_MS = 60000;
const MAX_BUFFER_SIZE = 50;
// Critical outbox (operator-intent frames like emergency arm/disarm): small,
// flushed ahead of the normal buffer on reconnect, same overflow discipline.
const MAX_CRITICAL_BUFFER_SIZE = 10;
/** Dispatched on window when an offline send is shed (detail: {dropped}). */
export const WS_OUTBOX_DROP_EVENT = "forgec2:ws-outbox-drop";

export function WebSocketProvider({ children }: { children: ReactNode }) {
  const [connected, setConnected] = useState(false);
  const [reconnectFailed, setReconnectFailed] = useState(false);
  // A read-only demo answers /ws 404 by design. /health reports demo_mode, and
  // the provider skips the connect/reconnect loop instead of showing every
  // visitor a permanent "reconnecting" warning.
  const [demoMode, setDemoMode] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const heartbeatRef = useRef<ReturnType<typeof setInterval> | null>(null);
  const lastPongRef = useRef(0);
  const listenersRef = useRef<Set<WSListener>>(new Set());
  const reconnectAttemptRef = useRef(0);
  // Consecutive closes where /health answered 200 but the socket still
  // failed. Two in a row means the endpoint itself is absent (the public
  // demo's gateway answers /ws with 404) rather than a backend restart.
  const backendUpFailuresRef = useRef(0);
  const sendBufferRef = useRef<string[]>([]);
  const criticalBufferRef = useRef<string[]>([]);
  const droppedOutboxRef = useRef(0);
  const connectRef = useRef<() => void>(() => {});

  const subscribe = useCallback((listener: WSListener) => {
    listenersRef.current.add(listener);
    return () => {
      listenersRef.current.delete(listener);
    };
  }, []);

  const send = useCallback((data: unknown, opts?: { critical?: boolean }) => {
    const raw = JSON.stringify(data);
    const ws = wsRef.current;
    if (ws && ws.readyState === WebSocket.OPEN) {
      ws.send(raw);
      return;
    }
    // Offline: buffer for replay on reconnect. A shed frame is lost operator
    // intent (previously silent) — count it, log it, and notify the UI layer
    // via WS_OUTBOX_DROP_EVENT (sonner toasts with a stable id collapse).
    const buf = opts?.critical ? criticalBufferRef.current : sendBufferRef.current;
    const cap = opts?.critical ? MAX_CRITICAL_BUFFER_SIZE : MAX_BUFFER_SIZE;
    if (buf.length >= cap) {
      buf.shift();
      droppedOutboxRef.current += 1;
      logger.warn("ws outbox overflow, dropping oldest frame", { dropped: droppedOutboxRef.current, critical: !!opts?.critical });
      if (typeof window !== "undefined") {
        window.dispatchEvent(new CustomEvent(WS_OUTBOX_DROP_EVENT, { detail: { dropped: droppedOutboxRef.current } }));
      }
    }
    buf.push(raw);
  }, []);

  useEffect(() => {
    let disposed = false;
    const startHeartbeat = () => {
      lastPongRef.current = Date.now();
      heartbeatRef.current = setInterval(() => {
        const ws = wsRef.current;
        if (!ws || ws.readyState !== WebSocket.OPEN) return;
        if (Date.now() - lastPongRef.current > HEARTBEAT_TIMEOUT_MS) {
          ws.close();
          return;
        }
        ws.send(JSON.stringify({ type: "ping" }));
      }, HEARTBEAT_INTERVAL_MS);
    };

    const stopHeartbeat = () => {
      if (heartbeatRef.current) { clearInterval(heartbeatRef.current); heartbeatRef.current = null; }
    };

    const connect = () => {
      if (disposed) return;
      if (reconnectRef.current) { clearTimeout(reconnectRef.current); reconnectRef.current = null; }
      if (wsRef.current && (wsRef.current.readyState === WebSocket.OPEN || wsRef.current.readyState === WebSocket.CONNECTING)) return;

      // Fan a message out to both subscription channels. Listeners are
      // fire-and-forget: a throwing listener is skipped so one bad subscriber
      // cannot break the bus — but the failure is logged so broken
      // subscribers are diagnosable instead of silently dead.
      const dispatch = (msg: WSMessage) => {
        for (const fn of listenersRef.current) {
          try { fn(msg); } catch (err) { logger.warn("ws listener threw, skipped", err); }
        }
        for (const fn of globalListeners) {
          try { fn(msg); } catch (err) { logger.warn("ws global listener threw, skipped", err); }
        }
      };

      const ws = new WebSocket(getWSURL());
      wsRef.current = ws;

      const probeHealth = async (): Promise<boolean> => {
        try {
          const ctrl = new AbortController();
          const timer = setTimeout(() => ctrl.abort(), 3000);
          try {
            const res = await fetch(getHealthURL(), { signal: ctrl.signal, credentials: "include" });
            // The real endpoint answers JSON. A static preview serves the SPA
            // shell for /health too (200 text/html from the fallback), which
            // must NOT count as "backend up" — that would flip every preview
            // screenshot to the offline banner.
            return res.ok && (res.headers.get("content-type") ?? "").includes("application/json");
          } finally {
            clearTimeout(timer);
          }
        } catch {
          return false;
        }
      };

      ws.onopen = () => {
        reconnectAttemptRef.current = 0;
        backendUpFailuresRef.current = 0;
        setReconnectFailed(false);
        // Critical intent first, then the normal backlog, both in order.
        while (criticalBufferRef.current.length > 0) {
          const buffered = criticalBufferRef.current.shift()!;
          ws.send(buffered);
        }
        while (sendBufferRef.current.length > 0) {
          const buffered = sendBufferRef.current.shift()!;
          ws.send(buffered);
        }
        setConnected(true);
        startHeartbeat();
      };
      ws.onclose = (event) => {
        stopHeartbeat();
        setConnected(false);
        if (disposed) return;
        // NOTE: the session cookie is HttpOnly, so its presence can never be
        // checked from JS. An expired session is detected via the HTTP layer
        // (handleUnauthorized / probeSessionExpiry below) instead of redirecting
        // on every transient disconnect.
        // Auth close codes mean the session is already dead: probe now instead
        // of burning through ~10min of exponential backoff on a fake-live page.
        // A reconnect is still scheduled (reset backoff) in case the probe
        // finds the session alive.
        if (event.code === 4401 || event.code === 1008) {
          reconnectAttemptRef.current = 0;
          probeSessionExpiry();
          reconnectRef.current = setTimeout(connect, 1000);
          return;
        }
        // Abnormal closure (backend gone OR session dead server-side OR the
        // endpoint is absent): probe now instead of only after the ~10min
        // backoff is exhausted. A dead backend just fails the fetch
        // (reconnect path continues); a dead session 401s straight to
        // /login through the debounced handler.
        if (event.code === 1006) {
          probeSessionExpiry();
          // If /health answers but the socket still fails, the endpoint
          // itself is gone — the public demo gateway answers /ws with 404.
          // Two consecutive such failures are enough to stop the fast retry
          // loop and show Offline immediately instead of "Reconnecting..."
          // for the whole backoff. A backend restart fails the health probe
          // too and keeps the fast retry.
          void probeHealth().then((up) => {
            if (disposed || up === false) {
              if (up === false) backendUpFailuresRef.current = 0;
              return;
            }
            backendUpFailuresRef.current += 1;
            if (backendUpFailuresRef.current < 2) return;
            setReconnectFailed(true);
            reconnectAttemptRef.current = 0;
            if (reconnectRef.current) clearTimeout(reconnectRef.current);
            reconnectRef.current = setTimeout(connect, BACKGROUND_RETRY_DELAY_MS);
          });
        }
        if (reconnectAttemptRef.current < MAX_RECONNECT_ATTEMPTS) {
          // Jittered backoff: without it every tab reconnects in lockstep
          // after a backend restart (thundering herd).
          const base = Math.min(1000 * Math.pow(2, reconnectAttemptRef.current), MAX_RECONNECT_DELAY);
          const delay = Math.floor(base * (0.8 + Math.random() * 0.4));
          reconnectAttemptRef.current++;
          reconnectRef.current = setTimeout(connect, delay);
        } else {
          setReconnectFailed(true);
          // Keep trying at a slow cadence after the fast backoff is exhausted
          // so the socket recovers without user intervention once the backend
          // is reachable again. The banner stays up until a connection succeeds.
          reconnectAttemptRef.current = 0;
          reconnectRef.current = setTimeout(connect, BACKGROUND_RETRY_DELAY_MS);
          // The socket can only stay down this long when the backend is gone
          // or the session is dead — probe once so an expired session routes
          // the user to /login through the debounced 401 handler.
          probeSessionExpiry();
        }
      };
      ws.onerror = () => { ws.close(); };
      ws.onmessage = (event) => {
        try {
          const msg = JSON.parse(event.data) as WSMessage;
          if (msg.type === "pong") { lastPongRef.current = Date.now(); return; }
          dispatch(msg);
        } catch (err) {
          // Malformed frames used to vanish silently; log once per frame so
          // protocol breakage is diagnosable (payload omitted: may be large).
          logger.warn("ws malformed frame dropped", err);
        }
      };
    };

    // Expose connect to the reconnect() callback which lives outside the
    // useEffect closure.
    connectRef.current = connect;

    // Hidden tabs throttle intervals (down to ~1/min), so heartbeat checks
    // can go stale. On return to the foreground, probe immediately: a dead
    // socket is closed here and the reconnect path takes over. Probe the
    // session too — it may have expired while hidden with the socket
    // nominally open (the cheap GET no-ops when everything is fine).
    const onVisibility = () => {
      if (document.visibilityState !== "visible") return;
      probeSessionExpiry();
      const ws = wsRef.current;
      if (!ws || ws.readyState !== WebSocket.OPEN) return;
      if (lastPongRef.current === 0 || Date.now() - lastPongRef.current > HEARTBEAT_TIMEOUT_MS) {
        ws.close();
        return;
      }
      ws.send(JSON.stringify({ type: "ping" }));
    };
    document.addEventListener("visibilitychange", onVisibility);

    // Preflight the public health payload: a read-only demo sets demo_mode and
    // intentionally disables /ws, so do not start a loop that cannot succeed.
    const preflight = async () => {
      try {
        const ctrl = new AbortController();
        const timer = setTimeout(() => ctrl.abort(), 3000);
        try {
          const res = await fetch(getHealthURL(), { credentials: "include", signal: ctrl.signal });
          if (res.ok) {
            const data = (await res.json()) as { demo_mode?: boolean };
            if (data?.demo_mode === true) {
              if (!disposed) setDemoMode(true);
              return;
            }
          }
        } finally {
          clearTimeout(timer);
        }
      } catch {
        // Health unreachable: proceed with a normal connect.
      }
      if (!disposed) connect();
    };
    void preflight();

    return () => {
      disposed = true;
      stopHeartbeat();
      document.removeEventListener("visibilitychange", onVisibility);
      if (reconnectRef.current) clearTimeout(reconnectRef.current);
      wsRef.current?.close();
    };
  }, []);

  // G1 fix: directly call connect() instead of closing a potentially-CLOSED
  // socket (which is a no-op that never re-enters the onclose→connect path).
  const reconnect = useCallback(() => {
    setReconnectFailed(false);
    reconnectAttemptRef.current = 0;
    backendUpFailuresRef.current = 0;
    if (reconnectRef.current) { clearTimeout(reconnectRef.current); reconnectRef.current = null; }
    // If socket is still OPEN, close it so connect() can create a fresh one.
    // If already CLOSED/CLOSING, just call connect() — it creates a new socket.
    const ws = wsRef.current;
    if (ws && (ws.readyState === WebSocket.OPEN || ws.readyState === WebSocket.CONNECTING)) {
      ws.close();
    } else {
      wsRef.current = null;
      connectRef.current();
    }
  }, []);

  const methods = useMemo<WSMethodsValue>(() => ({ subscribe, send, reconnect }), [subscribe, send, reconnect]);

  return (
    <WSMethodsContext.Provider value={methods}>
      <WSStatusContext.Provider value={{ connected, reconnectFailed, demoMode }}>
        {children}
      </WSStatusContext.Provider>
    </WSMethodsContext.Provider>
  );
}

export function useWS() {
  const status = useContext(WSStatusContext);
  const methods = useContext(WSMethodsContext);
  if (!methods) throw new Error("useWS must be used within WebSocketProvider");
  return { ...status, ...methods };
}
