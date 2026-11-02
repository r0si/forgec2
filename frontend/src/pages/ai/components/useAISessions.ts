import { useCallback, useEffect, useRef, useState } from "react";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { toast } from "sonner";
import { errorText } from "@/lib/errors";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { useI18n } from "@/lib/i18n";
import { AI_ERROR_TOOL_NAME, AI_REGENERATE_TOOL_NAME, AI_TRACE_TOOL_NAME, type AIMessage, type AISession, type AITraceStatus, type AITraceStep } from "./types";

const HISTORY_MESSAGE_CAP = 200;
const HISTORY_CONTENT_CAP = 80_000;
const HISTORY_TOTAL_CONTENT_CAP = 2_000_000;
const HISTORY_TRACE_STEP_CAP = 100;
const LAST_SESSION_KEY = "forgec2.ai.lastSession";

function readLastSessionId(): number | null {
  if (typeof window === "undefined") return null;
  try {
    const id = Number(window.localStorage.getItem(LAST_SESSION_KEY));
    return Number.isFinite(id) && id > 0 ? id : null;
  } catch {
    return null;
  }
}

function writeLastSessionId(id: number | null) {
  if (typeof window === "undefined") return;
  try {
    if (id == null) window.localStorage.removeItem(LAST_SESSION_KEY);
    else window.localStorage.setItem(LAST_SESSION_KEY, String(id));
  } catch {
    // Storage can be disabled by browser privacy policy; session navigation
    // must continue to work without persistence.
  }
}

function clipContent(content: string): string {
  if (!content || content.length <= HISTORY_CONTENT_CAP) return content || "";
  return `${content.slice(0, HISTORY_CONTENT_CAP)}\n\n…`;
}

function restoreMessage(m: { id?: number; role: string; content: string; tool_name?: string; created_at?: string }): AIMessage | null {
  if (m.tool_name !== AI_TRACE_TOOL_NAME) {
    if (m.role !== "user" && m.role !== "assistant" && m.role !== "tool") return null;
    return {
	  id: m.id,
      created_at: m.created_at,
      role: m.role,
      content: clipContent(m.content || ""),
      tool_name: m.tool_name,
      thinking: false,
      regenerated: m.role === "user" && m.tool_name === AI_REGENERATE_TOOL_NAME,
      error: m.role === "assistant" && m.tool_name === AI_ERROR_TOOL_NAME,
    };
  }

  try {
    const parsed = JSON.parse(m.content) as { trace?: AITraceStep[]; trace_status?: AITraceStatus; reasoning?: string };
    if (!Array.isArray(parsed.trace)) return null;
    return {
      role: "assistant",
      content: "",
      trace: parsed.trace.slice(-HISTORY_TRACE_STEP_CAP).map((step) => ({
        ...step,
        status: step.status === "active" ? "complete" : step.status,
      })),
      trace_status: parsed.trace_status === "error" ? "error" : "complete",
      reasoning: typeof parsed.reasoning === "string" ? clipContent(parsed.reasoning) : undefined,
    };
  } catch {
    return null;
  }
}

/** Rebuild one visible branch of history. A regenerated user turn carries a
 * persistence marker; discard the previous matching turn and its answer so a
 * reload matches what the operator saw immediately after Regenerate. */
export function restoreSessionMessages(records: { id?: number; role: string; content: string; tool_name?: string; created_at?: string }[]): AIMessage[] {
  const restored: AIMessage[] = [];
  for (const record of records) {
    const message = restoreMessage(record);
    if (!message) continue;
    if (message.regenerated && message.role === "user") {
      for (let i = restored.length - 1; i >= 0; i--) {
        if (restored[i].role === "user" && restored[i].content === message.content) {
          restored.splice(i);
          break;
        }
      }
    }
    restored.push(message);
  }
  return restored;
}

function capRestoredHistory(messages: AIMessage[]): AIMessage[] {
  let total = 0;
  let start = messages.length;
  for (let i = messages.length - 1; i >= 0; i--) {
    const size = messages[i].content.length + (messages[i].reasoning?.length || 0);
    if (total + size > HISTORY_TOTAL_CONTENT_CAP && start < messages.length) break;
    total += size;
    start = i;
  }
  return messages.slice(start);
}

export function useAISessions(
  setMessagesBoth: (updater: AIMessage[] | ((prev: AIMessage[]) => AIMessage[])) => void,
  onLeaveConversation?: () => void,
  options?: { restoreLast?: boolean },
) {
  const { t } = useI18n();
  const { toastError } = useErrorToast();
  const [sessions, setSessions] = useState<AISession[]>([]);
  const [sessionsLoading, setSessionsLoading] = useState(true);
  const [sessionsError, setSessionsError] = useState<string | null>(null);
  const [activeSessionId, setActiveSessionId] = useState<number | null>(null);
  const [sidebarOpen, setSidebarOpen] = useState(false);
  const [renameTarget, setRenameTarget] = useState<{ id: number; current: string } | null>(null);
  const [renameValue, setRenameValue] = useState("");
  const [selectingSessionId, setSelectingSessionId] = useState<number | null>(null);
  const selectSeqRef = useRef(0);
  const restoredRef = useRef(false);

  const loadSessions = useCallback(async () => {
    setSessionsLoading(true);
    try {
      const data = await api.get(paths.ai.sessions);
      // api.get already unwraps the outer {"success":true,"data":...};
      // the list lives under data.sessions (the previous data.data shape
      // was a second envelope, which read as undefined).
      const list = Array.isArray(data)
        ? data
        : ((data as { sessions?: AISession[] })?.sessions ?? []);
      setSessions(list as AISession[]);
      setSessionsError(null);
    } catch (e) {
      const msg = errorText(t, e, () => t("ai.toast.load_sessions_failed"));
      setSessionsError(msg);
      toast.error(msg);
    } finally {
      setSessionsLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadSessions();
  }, [loadSessions]);

  useEffect(() => {
    if (activeSessionId != null) writeLastSessionId(activeSessionId);
  }, [activeSessionId]);

  const selectSession = useCallback(async (id: number) => {
    if (id === activeSessionId) {
      // Clicking the already-active conversation means "stay here". Cancel
      // any slower selection that may still be resolving in the background.
      selectSeqRef.current += 1;
      setSelectingSessionId(null);
      setSidebarOpen(false);
      return true;
    }
    onLeaveConversation?.();
    const seq = ++selectSeqRef.current;
    setSelectingSessionId(id);
    try {
      const data = await api.get(paths.ai.sessionMessages(id));
      if (seq !== selectSeqRef.current) return false;
      // The list lives under data.messages inside the single envelope.
      const list = Array.isArray(data)
        ? data
        : ((data as { messages?: unknown[] })?.messages ?? []);
	  const mapped = restoreSessionMessages(list as { id?: number; role: string; content: string; tool_name?: string; created_at?: string }[]);
      const recent = mapped.length > HISTORY_MESSAGE_CAP ? mapped.slice(-HISTORY_MESSAGE_CAP) : mapped;
      setMessagesBoth(capRestoredHistory(recent));
      setActiveSessionId(id);
      writeLastSessionId(id);
      setSidebarOpen(false);
      return true;
    } catch (e) {
      if (seq !== selectSeqRef.current) return false;
      toastError(e, "ai.toast.load_session_messages_failed");
      return false;
    } finally {
      if (seq === selectSeqRef.current) setSelectingSessionId(null);
    }
  }, [activeSessionId, onLeaveConversation, setMessagesBoth, toastError]);

  const deleteSession = async (id: number) => {
    try {
      // A delete can race an outstanding selection. Invalidate that response
      // so a removed conversation cannot reappear after its GET finishes.
      selectSeqRef.current += 1;
      setSelectingSessionId(null);
      if (activeSessionId === id) {
        onLeaveConversation?.();
      }
      await api.del(paths.ai.session(id));
      setSessions((prev) => prev.filter((s) => s.id !== id));
      if (activeSessionId === id) {
        setActiveSessionId(null);
        writeLastSessionId(null);
        setMessagesBoth([]);
      }
      return true;
    } catch (e) {
      toastError(e, "ai.toast.delete_session_failed");
      return false;
    }
  };

  const handleNewChat = () => {
    // Invalidate an in-flight session load so it cannot overwrite the empty
    // conversation after the operator has already started a new chat.
    selectSeqRef.current += 1;
    onLeaveConversation?.();
    setMessagesBoth([]);
    setActiveSessionId(null);
    writeLastSessionId(null);
    setSidebarOpen(false);
  };

  useEffect(() => {
    if (options?.restoreLast === false || restoredRef.current || sessions.length === 0 || activeSessionId != null) return;
    const id = readLastSessionId();
    if (id == null || !sessions.some((s) => s.id === id)) {
      restoredRef.current = true;
      return;
    }
    restoredRef.current = true;
    void selectSession(id);
  }, [sessions, activeSessionId, options?.restoreLast, selectSession]);

  const renameSession = (id: number, current: string) => {
    setRenameTarget({ id, current });
    setRenameValue(current);
  };

  return {
    sessions,
    sessionsLoading,
    sessionsError,
    setSessions,
    activeSessionId,
    setActiveSessionId,
    sidebarOpen,
    setSidebarOpen,
    renameTarget,
    setRenameTarget,
    renameValue,
    setRenameValue,
    loadSessions,
    selectSession,
    deleteSession,
    handleNewChat,
    renameSession,
    selectingSessionId,
  };
}
