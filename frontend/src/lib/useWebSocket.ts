
import { useEffect, useRef } from "react";
import { useWS, type WSMessage } from "@/lib/wsContext";

export type { WSMessage };

export function useWebSocket(onMessage?: (msg: WSMessage) => void) {
  const { connected, reconnectFailed, demoMode, subscribe, reconnect } = useWS();
  const onMessageRef = useRef(onMessage);

  useEffect(() => {
    onMessageRef.current = onMessage;
  }, [onMessage]);

  useEffect(() => {
    const handler = (msg: WSMessage) => onMessageRef.current?.(msg);
    return subscribe(handler);
  }, [subscribe]);

  return { connected, reconnectFailed, demoMode, subscribe, reconnect };
}
