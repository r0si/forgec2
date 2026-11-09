
import { registerFragment } from "@/lib/i18n";
import { enAI } from "@/lib/i18n/fragments/en.ai";
registerFragment(enAI);

import { useCallback, useEffect, useRef, useState } from "react";
import { useSearchParams } from "react-router-dom";
import { useI18n } from "@/lib/i18n";
import { API_BASE } from "@/lib/constants";
import { downloadText } from "@/lib/download";
import { consumeSSEBuffer, flushSSEBuffer, type ParsedSSEEvent } from "@/lib/sse";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { normalizeListEnvelope } from "@/lib/envelope";
import { WorkspaceShell } from "@/components/ui/workspace-shell";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from "@/components/ui/dialog";
import { Sheet, SheetContent } from "@/components/ui/sheet";
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu";
import { toast } from "sonner";
import { Badge } from "@/components/ui/badge";
import { ArrowDown, Bot, Download, Menu, MoreHorizontal, Plus, Settings, SlidersHorizontal, Trash2 } from "lucide-react";
import AISessionSidebar from "./components/AISessionSidebar";
import {
  type AIMessage,
  type AITraceStage,
  type AITraceStep,
} from "./components/types";
import { useAIConfig } from "./components/useAIConfig";
import { useAISessions } from "./components/useAISessions";
import { AIMessageList } from "./components/AIMessageList";
import { AIComposer } from "./components/AIComposer";
import { AIAssistBar } from "./components/AIAssistBar";
import { AIConfigPanel } from "./components/AIConfigPanel";
import { PendingAIIntents } from "./components/PendingAIIntents";
import { AIContextPanel } from "./components/AIContextPanel";
import { AI_INPUT_MAX_CHARS, buildConversationPayload, sessionTitleFromQuery } from "./components/chatPayload";
import { readAIResponseError } from "./components/streamErrors";
import { useConfirm } from "@/lib/hooks/useConfirm";
import { useErrorToast } from "@/lib/hooks/useErrorToast";
import { usePermissions } from "@/lib/hooks/usePermissions";
import { useAIRunStatuses } from "./components/useAIRunStatuses";
import { useAISessionDraft, readNewAISessionDraft } from "./components/useAISessionDraft";
import { fetchAISSE } from "./components/aiSse";

function nowMs(): number {
  return Date.now();
}

export default function AIPage() {
  const { t } = useI18n();
  const { role: currentUserRole, can } = usePermissions();
  const { toastError } = useErrorToast();
  const [messages, setMessages] = useState<AIMessage[]>([]);
  const [input, setInput] = useState("");
  const inputRef = useRef("");
  const draftsRef = useRef<Map<string, string>>(new Map());
  const [loading, setLoading] = useState(false);
  const [usage, setUsage] = useState<{ prompt: number; completion: number }>({ prompt: 0, completion: 0 });
  const [showJumpToLatest, setShowJumpToLatest] = useState(false);
	const [contextPanelOpen, setContextPanelOpen] = useState(false);
	const [selectedProfileId, setSelectedProfileId] = useState<number | null>(null);
	const [selectedAttachmentIds, setSelectedAttachmentIds] = useState<string[]>([]);
	const [selectedCollectionIds, setSelectedCollectionIds] = useState<number[]>([]);
	const [lowRiskAuto, setLowRiskAuto] = useState(false);
	const [chatOnly, setChatOnly] = useState(false);
	const [profilesReady, setProfilesReady] = useState(false);
	const { runStatuses, setRunStatuses } = useAIRunStatuses();
  const abortRef = useRef<AbortController | null>(null);
  const streamFlushRef = useRef<(() => void) | null>(null);
  const sendLockRef = useRef(false);
  const streamSeqRef = useRef(0);
  const streamGenRef = useRef(0);
  const activeStreamIdRef = useRef<string | null>(null);
	const activeRunIdRef = useRef<string | null>(null);
  const messagesEndRef = useRef<HTMLDivElement>(null);
  const scrollFrameRef = useRef<number | null>(null);
  const hasMessagesRef = useRef(false);
  const textareaRef = useRef<HTMLTextAreaElement>(null);

  const {
    enabled, setEnabled,
    provider, setProvider, model, setModel, apiKey, setApiKey,
    endpoint, setEndpoint, systemPrompt, setSystemPrompt,
    engagementNotes, setEngagementNotes,
    allowExecute, setAllowExecute, configSaving, showSettings, setShowSettings,
    handleSaveConfig, hasApiKey, configLoading, configLoaded, configError, loadConfig,
  } = useAIConfig();

  const adjustTextarea = () => {
    const el = textareaRef.current;
    if (!el) return;
    el.style.height = "auto";
    el.style.height = Math.min(el.scrollHeight, 128) + "px";
  };

  // Operator context: deep links like /ai?agent=<id>&q=<text> (e.g. the
  // "Ask AI" button on an agent page) focus the conversation on one agent.
  // Every chat request carries it so tools can resolve "this machine".
  const [searchParams] = useSearchParams();
  const [contextAgentId, setContextAgentId] = useState("");
  const deepLinkDoneRef = useRef(false);
  const handleSendRef = useRef<(textOverride?: string, regenerated?: boolean) => Promise<void>>(async () => {});

  // messagesRef mirrors `messages` synchronously so streaming/autosave can read
  // the latest value without stale closures.
  const messagesRef = useRef<AIMessage[]>([]);
  const setMessagesBoth = useCallback(
    (updater: AIMessage[] | ((prev: AIMessage[]) => AIMessage[])) => {
      // Update the ref before enqueueing React state. Regenerate/session
      // actions can immediately start another request in the same event turn;
      // reading a ref updated only inside React's async updater reused stale
      // messages and could duplicate context or persist the wrong slice.
      const previous = messagesRef.current;
      const next = typeof updater === "function"
        ? (updater as (p: AIMessage[]) => AIMessage[])(previous)
        : updater;
      const capped = next.length > 500 ? next.slice(-500) : next;
      messagesRef.current = capped;
      setMessages(capped);
    },
    []
  );

  const stopGeneration = useCallback((reason?: string) => {
    // Commit the last coalesced snapshot before invalidating the generation;
    // otherwise Stop/session navigation can lose the final visible tokens.
    streamFlushRef.current?.();
    streamFlushRef.current = null;
    streamGenRef.current += 1;
    sendLockRef.current = false;
    if (abortRef.current) {
      abortRef.current.abort(reason || "navigate");
      abortRef.current = null;
    }
    setLoading(false);
  }, []);

  const {
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
  } = useAISessions(setMessagesBoth, stopGeneration, {
    restoreLast: !searchParams.get("q") && !searchParams.get("agent"),
  });
  const { confirm: confirmDelete, modal: deleteSessionModal } = useConfirm();
	useEffect(() => {
	  let active = true;
	  api.get<unknown>(paths.ai.profiles)
		.then((payload) => {
		  if (!active) return;
		  const profiles = normalizeListEnvelope(payload, ["profiles", "data"]) as Array<{ id: number; is_default?: boolean }>;
		  setProfilesReady(profiles.length > 0);
		  const preferred = profiles.find((profile) => profile.is_default) ?? profiles[0];
		  if (preferred) setSelectedProfileId((current) => current ?? preferred.id);
		})
		.catch(() => { if (active) setProfilesReady(false); });
	  return () => { active = false; };
	}, []);
	const configured = (enabled && hasApiKey) || profilesReady;
  const canConfigure = can("ai.configure") || currentUserRole.toLocaleLowerCase() === "admin";
  const draftKey = (sessionId: number | null) => sessionId == null ? "new" : `session:${sessionId}`;
  const replaceInput = useCallback((value: string) => {
    inputRef.current = value;
    setInput(value);
  }, []);
	useAISessionDraft(activeSessionId, input);

  const stickToBottomRef = useRef(true);
  const scheduleScrollToBottom = useCallback((smooth: boolean) => {
    if (scrollFrameRef.current != null) window.cancelAnimationFrame(scrollFrameRef.current);
    scrollFrameRef.current = window.requestAnimationFrame(() => {
      scrollFrameRef.current = null;
      const viewport = messagesEndRef.current?.closest<HTMLElement>("[data-ai-message-scroll]");
      if (!viewport) return;
      const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)").matches;
      viewport.scrollTo({
        top: viewport.scrollHeight,
        behavior: smooth && !reducedMotion ? "smooth" : "auto",
      });
    });
  }, []);

  useEffect(() => {
    const viewport = messagesEndRef.current?.closest<HTMLElement>("[data-ai-message-scroll]");
    if (!viewport) return;
    const onScroll = () => {
      const nearBottom = viewport.scrollHeight - viewport.scrollTop - viewport.clientHeight < 96;
      stickToBottomRef.current = nearBottom;
      setShowJumpToLatest((visible) => visible === !nearBottom ? visible : !nearBottom);
    };
    viewport.addEventListener("scroll", onScroll, { passive: true });
    return () => viewport.removeEventListener("scroll", onScroll);
  }, []);

  useEffect(() => {
    hasMessagesRef.current = messages.length > 0;
  }, [messages.length]);
  useEffect(() => {
    if (messages.length === 0) {
      const viewport = messagesEndRef.current?.closest<HTMLElement>("[data-ai-message-scroll]");
      viewport?.scrollTo({ top: 0, behavior: "auto" });
      stickToBottomRef.current = true;
      setShowJumpToLatest(false);
      return;
    }
    if (stickToBottomRef.current) scheduleScrollToBottom(!loading);
  }, [loading, messages, scheduleScrollToBottom]);
  useEffect(() => {
    // An empty conversation has nothing to reveal: scrolling to the bottom here
    // shifted the centered mobile empty state by ~32px once the session list
    // resolved (a race the visual baselines caught). Keep it pinned to the top.
    if (!hasMessagesRef.current) return;
    stickToBottomRef.current = true;
    setShowJumpToLatest(false);
    scheduleScrollToBottom(false);
  }, [activeSessionId, scheduleScrollToBottom]);
  useEffect(() => { adjustTextarea(); }, [input]);
  useEffect(() => {
    return () => {
      abortRef.current?.abort();
      if (scrollFrameRef.current != null) window.cancelAnimationFrame(scrollFrameRef.current);
    };
  }, []);

  // Consume deep-link params once. Mark the send as consumed inside the timer
  // so StrictMode can clean up and schedule it again without either losing the
  // request or leaving an async callback alive after unmount.
  useEffect(() => {
    if (configLoading) return;
    if (deepLinkDoneRef.current) return;
    const agent = searchParams.get("agent") || "";
    const q = searchParams.get("q") || "";
    if (!agent && !q) return;
    if (agent) setContextAgentId(agent);
    if (!q) {
      deepLinkDoneRef.current = true;
      return;
    }
    if (q.length > AI_INPUT_MAX_CHARS) {
      deepLinkDoneRef.current = true;
      replaceInput(q.slice(0, AI_INPUT_MAX_CHARS));
      toast.error(t("ai.input_too_long", { count: AI_INPUT_MAX_CHARS }));
      return;
    }
    replaceInput(q);
    const timer = window.setTimeout(() => {
      if (deepLinkDoneRef.current) return;
      deepLinkDoneRef.current = true;
      void handleSendRef.current(q);
    }, 500);
    return () => window.clearTimeout(timer);
  }, [configLoading, replaceInput, searchParams, t]);

  const confirmRename = async () => {
    if (!renameTarget) return;
    const title = renameValue.trim();
    if (title === "" || title === renameTarget.current) { setRenameTarget(null); return; }
    try {
      await api.putJson(paths.ai.session(renameTarget.id), { title });
      setSessions((prev) => prev.map((s) => (s.id === renameTarget.id ? { ...s, title } : s)));
    } catch (e) { toastError(e, "ai.toast.rename_session_failed"); }
    setRenameTarget(null);
  };

  const quickActions = [
    { label: t("ai.quick_situation"), query: t("ai.quick_situation_query") },
    { label: t("ai.quick_list_implants"), query: t("ai.quick_list_implants") },
    { label: t("ai.quick_elevated"), query: t("ai.quick_elevated_query") },
    { label: t("ai.quick_alerts"), query: t("ai.quick_alerts_query") },
    { label: t("ai.quick_next"), query: t("ai.quick_next_query") },
  ];

  // handleRegenerate trims the trailing assistant/tool reply and re-asks the last
  // user message, reusing the existing streaming path via handleSend.
  const handleRegenerate = useCallback(() => {
    if (loading) return;
    const cur = messagesRef.current;
    let lastUser = -1;
    for (let i = cur.length - 1; i >= 0; i--) {
      if (cur[i].role === "user") { lastUser = i; break; }
    }
    if (lastUser < 0) return;
    const lastUserMsg = cur[lastUser];
    setMessagesBoth(cur.slice(0, lastUser));
    void handleSendRef.current(lastUserMsg.content, true);
  }, [loading, setMessagesBoth]);

  const handleSend = async (textOverride?: string, regenerated = false) => {
    const text = (textOverride ?? input).trim();
    if (!text || loading || sendLockRef.current) return;
    if (configLoading) return;
    if (!configured) {
      if (canConfigure) setShowSettings(true);
      toast.error(canConfigure ? t("ai.not_ready") : t("ai.contact_admin_warning"));
      return;
    }
    if (text.length > AI_INPUT_MAX_CHARS) {
      toast.error(t("ai.input_too_long", { count: AI_INPUT_MAX_CHARS }));
      return;
    }
    sendLockRef.current = true;
    const userMsg: AIMessage = { role: "user", content: text, regenerated };
    // Snapshot before dispatching; setMessagesBoth keeps messagesRef in sync
    // immediately, so regenerate and deep-link sends use this exact history.
    const historyBefore = messagesRef.current.slice();
    setMessagesBoth((prev) => [...prev, userMsg]);
    replaceInput("");
    draftsRef.current.delete(draftKey(activeSessionId));
    setLoading(true);
    const gen = ++streamGenRef.current;

    let sessionId = activeSessionId;
    if (sessionId == null) {
      try {
        const created = await api.postJson<{ id: number; title: string }>(paths.ai.sessions, {
          title: sessionTitleFromQuery(text, quickActions),
		  profile_id: selectedProfileId,
		  write_policy: lowRiskAuto ? "low_risk_auto" : "approval",
        });
        sessionId = created?.id ?? null;
        if (sessionId != null) {
          setActiveSessionId(sessionId);
          draftsRef.current.delete(draftKey(sessionId));
          loadSessions();
        }
      } catch (e) {
        sessionId = null;
        toastError(e, "ai.toast.create_session_failed");
      }
    }

    // The operator may switch conversations while session creation is in
    // flight. Do not start a detached stream after that navigation.
    if (gen !== streamGenRef.current) return;

    const controller = new AbortController();
    abortRef.current = controller;

    const streamId = `ai-${++streamSeqRef.current}`;
    activeStreamIdRef.current = streamId;
    const runStartedAt = nowMs();
    let runPromptTokens = 0;
    let runCompletionTokens = 0;
    let traceStepSeq = 0;
    const makeTraceStep = (stage: AITraceStage, startedAt: number, details?: Pick<AITraceStep, "tool_name" | "tool_call_id">): AITraceStep => ({
      id: `${streamId}-${++traceStepSeq}`,
      stage,
      status: "active",
      started_at: startedAt,
      ...details,
    });
    const traceMsg: AIMessage = {
      role: "assistant",
      content: "",
      stream_id: streamId,
      trace: [makeTraceStep("analyzing", nowMs())],
      trace_status: "running",
    };
    setMessagesBoth((prev) => [...prev, traceMsg]);

    const advanceTrace = (stage: AITraceStage, details?: Pick<AITraceStep, "tool_name" | "tool_call_id">) => {
      const now = nowMs();
      const nextStep = makeTraceStep(stage, now, details);
      setMessagesBoth((prev) => prev.map((message) => {
        if (message.stream_id !== streamId || !message.trace) return message;
        const current = message.trace[message.trace.length - 1];
        if (current?.status === "active" && current.stage === stage && current.tool_call_id === details?.tool_call_id && current.tool_name === details?.tool_name) {
          return message;
        }
        const trace = message.trace.map((step) => step.status === "active"
          ? { ...step, status: "complete" as const, completed_at: now }
          : step);
        return { ...message, trace: [...trace, nextStep] };
      }));
    };

    const finishTrace = (failed = false) => {
      const now = nowMs();
      setMessagesBoth((prev) => prev.map((message) => {
        if (message.stream_id !== streamId || !message.trace || message.trace_status !== "running") return message;
        return {
          ...message,
          trace_status: failed ? "error" as const : "complete" as const,
          trace: message.trace.map((step) => step.status === "active"
            ? { ...step, status: failed ? "error" as const : "complete" as const, completed_at: now }
            : step),
        };
      }));
    };

    const completeToolTrace = (toolCallId: string, toolName: string, failed = false) => {
      const completedAt = nowMs();
      setMessagesBoth((prev) => prev.map((message) => {
        if (message.stream_id !== streamId || !message.trace) return message;
        const trace = [...message.trace];
        for (let i = trace.length - 1; i >= 0; i--) {
          const step = trace[i];
          if (step.stage !== "tool" || step.status !== "active") continue;
          if ((toolCallId && step.tool_call_id === toolCallId) || (!toolCallId && step.tool_name === toolName)) {
            trace[i] = {
              ...step,
              status: failed ? "error" : "complete",
              completed_at: completedAt,
            };
            break;
          }
        }
        return { ...message, trace };
      }));
    };

    const upsertAssistantContent = (content: string, error = false) => {
      setMessagesBoth((prev) => {
        const updated = [...prev];
        const idx = updated.findIndex((message) =>
          message.stream_id === streamId && message.role === "assistant" && !message.trace);
        const responseMessage: AIMessage = {
          ...(idx >= 0 ? updated[idx] : {}),
          role: "assistant",
          content,
          stream_id: streamId,
          error,
          run_status: error ? "error" : "streaming",
          truncated: /\[Response truncated\]\s*$/i.test(content),
        };
        if (idx === -1) updated.push(responseMessage);
        else updated[idx] = responseMessage;
        return updated;
      });
    };

    const updateAssistantRunMeta = (meta: Partial<AIMessage>) => {
      setMessagesBoth((prev) => prev.map((message) => (
        message.stream_id === streamId && message.role === "assistant" && !message.trace
          ? { ...message, ...meta }
          : message
      )));
    };

    const upsertReasoning = (text: string) => {
      setMessagesBoth((prev) => prev.map((message) => {
        if (message.stream_id !== streamId || !message.trace) return message;
        return { ...message, reasoning: text };
      }));
    };

    // Provider streams can deliver hundreds of cumulative snapshots per
    // second. Coalesce them to one React update per short frame so markdown,
    // trace and scroll rendering stay responsive on long answers.
    let pendingAssistantContent: string | null = null;
    let pendingReasoning: string | null = null;
    let streamCommitTimer: number | null = null;
    let latestAssistantContent = "";
    const flushPendingStream = () => {
      if (streamCommitTimer != null) {
        window.clearTimeout(streamCommitTimer);
        streamCommitTimer = null;
      }
      if (gen !== streamGenRef.current) {
        pendingAssistantContent = null;
        pendingReasoning = null;
        return;
      }
      if (pendingReasoning != null) {
        const reasoning = pendingReasoning;
        pendingReasoning = null;
        upsertReasoning(reasoning);
      }
      if (pendingAssistantContent != null) {
        const content = pendingAssistantContent;
        pendingAssistantContent = null;
        upsertAssistantContent(content);
      }
    };
    const scheduleStreamCommit = () => {
      if (streamCommitTimer != null) return;
      streamCommitTimer = window.setTimeout(flushPendingStream, 32);
    };
    const queueAssistantContent = (content: string) => {
      latestAssistantContent = content;
      pendingAssistantContent = content;
      scheduleStreamCommit();
    };
    const queueReasoning = (reasoning: string) => {
      pendingReasoning = reasoning;
      scheduleStreamCommit();
    };
    const presentStreamError = (message: string) => {
      flushPendingStream();
      const previous = latestAssistantContent.trimEnd();
      const content = previous ? `${previous}\n\n---\n${message}` : message;
      latestAssistantContent = content;
      upsertAssistantContent(content, true);
      updateAssistantRunMeta({
        run_status: "error",
        duration_ms: nowMs() - runStartedAt,
        prompt_tokens: runPromptTokens,
        completion_tokens: runCompletionTokens,
        created_at: new Date().toISOString(),
      });
    };
    streamFlushRef.current = flushPendingStream;

    let streamFailed = false;
    let streamCompleted = false;
	let backgroundRunId = "";
    let lastEventAt = nowMs();
    // A 4s interval kept waking the main thread for every active run. A
    // recursive timeout is sufficient for a 120s idle threshold and reduces
    // wakeups by an order of magnitude while still aborting stalled streams.
    let idleWatchdog: number | null;
    const checkIdle = () => {
      if (nowMs() - lastEventAt > 120_000) {
        controller.abort("idle");
        idleWatchdog = null;
        return;
      }
      idleWatchdog = window.setTimeout(checkIdle, 30_000);
    };
    idleWatchdog = window.setTimeout(checkIdle, 30_000);
    try {
	  if (sessionId == null) {
		throw new Error("AI session was not created");
	  }
	  const conversationHistory = buildConversationPayload([...historyBefore, userMsg]);
	  const run = await api.postJson<{ id: string; status: string }>(paths.ai.runs, {
		session_id: sessionId,
		profile_id: selectedProfileId,
		idempotency_key: crypto.randomUUID(),
		messages: conversationHistory,
		attachment_ids: selectedAttachmentIds,
		knowledge_collection_ids: selectedCollectionIds,
		context: {
		  page: typeof window !== "undefined" ? window.location.pathname : "",
		  agent_id: contextAgentId,
		  allow_low_risk_writes: lowRiskAuto,
		  disable_tools: chatOnly,
		},
	  });
	  backgroundRunId = run.id;
	  activeRunIdRef.current = backgroundRunId;
	  setRunStatuses((current) => ({ ...current, [sessionId]: run.status || "queued" }));

	  let lastEventId = "";
	  let response: Response | null = null;
	  for (let attempt = 0; attempt < 4; attempt += 1) {
		const headers: Record<string, string> = { Accept: "text/event-stream" };
		if (lastEventId) headers["Last-Event-ID"] = lastEventId;
		response = await fetchAISSE(`${API_BASE}${paths.ai.runEvents(backgroundRunId)}${lastEventId ? `?after=${encodeURIComponent(lastEventId)}` : ""}`, {
		  method: "GET",
		  headers,
		  credentials: "include",
		}, controller.signal);
		if (response.status === 401) {
		  const { handleUnauthorized } = await import("@/lib/api");
		  handleUnauthorized(response);
		  finishTrace(true);
		  presentStreamError(t("ai.error_connection"));
		  return;
		}
		if (response.ok && response.body) break;
		if (response.status >= 400 && response.status < 500) break;
		await new Promise((resolve) => window.setTimeout(resolve, 500 * (attempt + 1)));
	  }

	  if (!response || !response.ok || !response.body) {
		finishTrace(true);
		const detail = response ? await readAIResponseError(response) : "";
        presentStreamError(detail ? `${t("ai.error_prefix")}${detail}` : t("ai.error_connection"));
        return;
      }

	  const processEvent = ({ id, event, data }: ParsedSSEEvent) => {
		if (gen !== streamGenRef.current) return;
		if (id) lastEventId = id;
        lastEventAt = nowMs();
        switch (event) {
          case "usage": {
            try {
              const parsed = JSON.parse(data) as { prompt_tokens?: number; completion_tokens?: number };
              runPromptTokens += parsed.prompt_tokens || 0;
              runCompletionTokens += parsed.completion_tokens || 0;
              setUsage((prev) => ({
                prompt: prev.prompt + (parsed.prompt_tokens || 0),
                completion: prev.completion + (parsed.completion_tokens || 0),
              }));
            } catch { /* Ignore malformed provider usage. */ }
            break;
          }
          case "progress": {
            try {
              const parsed = JSON.parse(data) as { stage?: AITraceStage };
              if (parsed.stage) advanceTrace(parsed.stage);
            } catch { /* Ignore unknown progress metadata. */ }
            break;
          }
          case "thinking":
            advanceTrace("analyzing");
            break;
          case "reasoning":
            advanceTrace("reasoning");
            if (data.trim() !== "") queueReasoning(data);
            break;
          case "clear":
            advanceTrace("answering");
            break;
          case "text":
            advanceTrace("answering");
            queueAssistantContent(data);
            break;
          case "tool_start": {
            let toolName = data;
            let toolCallId = "";
            try {
              const parsed = JSON.parse(data) as { id?: string; name?: string };
              toolName = parsed.name || toolName;
              toolCallId = parsed.id || "";
            } catch { /* Backward-compatible plain tool name. */ }
            advanceTrace("tool", { tool_name: toolName, tool_call_id: toolCallId });
            setMessagesBoth((prev) => [
              ...prev,
              {
                role: "tool",
                content: `${t("ai.calling_tool")} ${toolName}...`,
                tool_name: toolName,
                tool_call_id: toolCallId,
                stream_id: streamId,
                tool_status: "running",
              },
            ]);
            break;
          }
          case "tool":
          case "tool_result": {
            try {
              const parsed = JSON.parse(data) as { id?: string; name?: string; result?: unknown; error?: unknown; status?: string };
              const toolName = parsed.name || t("ai.tool");
              const toolCallId = parsed.id || "";
              const payload = parsed.result ?? parsed.error ?? parsed;
              const result = typeof payload === "string" ? payload : JSON.stringify(payload, null, 2);
              const resultLabel = `${t("ai.tool_result")}:\n${result ?? ""}`;
              const failed = Boolean(parsed.error) || parsed.status === "failed" || parsed.status === "error";
              setMessagesBoth((prev) => {
                const updated = [...prev];
                let idx = -1;
                for (let i = updated.length - 1; i >= 0; i--) {
                  const message = updated[i];
                  if (message.stream_id !== streamId || message.role !== "tool") continue;
                  if ((toolCallId && message.tool_call_id === toolCallId) || (!toolCallId && message.tool_name === toolName)) {
                    idx = i;
                    break;
                  }
                }
                const toolMessage: AIMessage = {
                  role: "tool",
                  content: resultLabel,
                  tool_name: toolName,
                  tool_call_id: toolCallId,
                  stream_id: streamId,
                  tool_status: failed ? "error" : "success",
                };
                if (idx === -1) updated.push(toolMessage);
                else updated[idx] = toolMessage;
                return updated;
              });
              completeToolTrace(toolCallId, toolName, failed);
            } catch {
              setMessagesBoth((prev) => [
                ...prev,
                { role: "tool", content: data, tool_name: t("ai.tool"), stream_id: streamId, tool_status: "success" },
              ]);
            }
            break;
          }
          case "tool_intent": {
            try {
              const parsed = JSON.parse(data) as { id?: string; tool?: string; name?: string; risk?: string };
              const toolName = parsed.tool || parsed.name || t("ai.tool");
              setMessagesBoth((prev) => [...prev, {
                role: "tool",
                content: JSON.stringify({ status: "waiting_approval", risk: parsed.risk || "write" }, null, 2),
                tool_name: toolName,
                tool_call_id: parsed.id || "",
                stream_id: streamId,
                tool_status: "waiting_approval",
              }]);
              advanceTrace("tool", { tool_name: toolName, tool_call_id: parsed.id || "" });
            } catch { /* The approval queue remains the source of truth. */ }
            break;
          }
          case "done":
            streamCompleted = true;
            flushPendingStream();
            if (!latestAssistantContent.trim()) {
              latestAssistantContent = t("ai.empty_response");
              upsertAssistantContent(latestAssistantContent);
            }
            updateAssistantRunMeta({
              run_status: "complete",
              duration_ms: nowMs() - runStartedAt,
              prompt_tokens: runPromptTokens,
              completion_tokens: runCompletionTokens,
              created_at: new Date().toISOString(),
              error: false,
            });
            finishTrace(false);
            break;
          case "error":
            streamFailed = true;
            flushPendingStream();
            finishTrace(true);
            presentStreamError(t("ai.error_prefix") + data);
            break;
          default:
            break;
        }
	  };

	  const consumeRunStream = async (current: Response) => {
		if (!current.body) return;
		const reader = current.body.getReader();
		const decoder = new TextDecoder();
		let buffer = "";
		while (gen === streamGenRef.current && !streamCompleted) {
		  const { done, value } = await reader.read();
		  if (done) break;
		  buffer += decoder.decode(value, { stream: true });
		  const parsed = consumeSSEBuffer(buffer);
		  buffer = parsed.remainder;
		  if (parsed.overflow) {
			presentStreamError(t("ai.error_prefix") + "SSE buffer overflow - truncated");
		  }
		  parsed.events.forEach(processEvent);
		  if (streamCompleted) {
			await reader.cancel();
			break;
		  }
		}
		buffer += decoder.decode();
		flushSSEBuffer(buffer).forEach(processEvent);
		flushPendingStream();
	  };

	  let currentResponse = response;
	  for (let reconnect = 0; reconnect < 4 && gen === streamGenRef.current && !streamCompleted && !streamFailed; reconnect += 1) {
		await consumeRunStream(currentResponse);
		if (streamCompleted || streamFailed || gen !== streamGenRef.current) break;
		await new Promise((resolve) => window.setTimeout(resolve, 600 * (reconnect + 1)));
		const h: Record<string, string> = { Accept: "text/event-stream" };
		if (lastEventId) h["Last-Event-ID"] = lastEventId;
		currentResponse = await fetchAISSE(`${API_BASE}${paths.ai.runEvents(backgroundRunId)}?after=${encodeURIComponent(lastEventId || "0")}`, {
		  headers: h,
		  credentials: "include",
		}, controller.signal);
		if (currentResponse.status === 401) {
		  const { handleUnauthorized } = await import("@/lib/api");
		  handleUnauthorized(currentResponse);
		  break;
		}
		if (!currentResponse.ok || !currentResponse.body) break;
	  }
	  if (gen === streamGenRef.current) {
		if (!streamFailed && streamCompleted) {
		  finishTrace(false);
		} else if (!streamFailed) {
		  streamFailed = true;
		  finishTrace(true);
		  presentStreamError(t("ai.error_stream_interrupted"));
		}
	  }
    } catch (err: unknown) {
      if (gen !== streamGenRef.current) return;
      if (err instanceof Error && err.name === "AbortError") {
        finishTrace(true);
        if (controller.signal.reason === "idle") {
          presentStreamError(t("ai.error_stream_interrupted"));
        }
        return;
      }
      finishTrace(true);
      presentStreamError(t("ai.error_stream_interrupted"));
    } finally {
	  if (idleWatchdog != null) window.clearTimeout(idleWatchdog);
      if (streamCommitTimer != null) {
        window.clearTimeout(streamCommitTimer);
        streamCommitTimer = null;
      }
      if (streamFlushRef.current === flushPendingStream) {
        streamFlushRef.current = null;
      }
      if (activeStreamIdRef.current === streamId) activeStreamIdRef.current = null;
	  if (activeRunIdRef.current === backgroundRunId && streamCompleted) activeRunIdRef.current = null;
	  if (streamCompleted && sessionId != null) setRunStatuses((current) => { const next = { ...current }; delete next[sessionId!]; return next; });
      if (gen === streamGenRef.current) {
        setLoading(false);
        sendLockRef.current = false;
        abortRef.current = null;
      }
	  if (sessionId != null) loadSessions();
    }
  };
  // Keep the ref in sync from an effect (not render body) so concurrent
  // renders never publish a half-built closure to the deep-link timer.
  useEffect(() => {
    handleSendRef.current = handleSend;
  });

  const handleStop = () => {
	const runId = activeRunIdRef.current;
	if (runId) {
	  void api.postJson(paths.ai.runCancel(runId), {}).catch((e) => {
		toastError(e, "ai.error_connection");
	  });
	  activeRunIdRef.current = null;
	}
    const streamId = activeStreamIdRef.current;
    stopGeneration("stop");
    activeStreamIdRef.current = null;
    const now = nowMs();
    setMessagesBoth((prev) => prev.map((message) => {
      if (!message.trace || message.trace_status !== "running") return message;
      return {
        ...message,
        trace_status: "error" as const,
        trace: message.trace.map((step) => step.status === "active"
          ? { ...step, status: "error" as const, completed_at: now }
          : step),
      };
    }));
    if (streamId) {
      setMessagesBoth((prev) => {
        const exists = prev.some((message) => message.stream_id === streamId && message.role === "assistant" && !message.trace);
        if (exists) {
          return prev.map((message) => message.stream_id === streamId && message.role === "assistant" && !message.trace
            ? { ...message, error: true, run_status: "interrupted" as const }
            : message);
        }
        return [...prev, {
          role: "assistant" as const,
          content: t("ai.generation_stopped"),
          stream_id: streamId,
          error: true,
          run_status: "interrupted" as const,
        }];
      });
    }
  };

	const handleBranch = useCallback(async (messageId: number) => {
	  if (activeSessionId == null) return;
	  try {
		const child = await api.postJson<{ id: number }>(paths.ai.sessionBranch(activeSessionId), { message_id: messageId });
		await loadSessions();
		await selectSession(child.id);
		toast.success(t("ai.branch_created"));
	  } catch (error) {
		toastError(error, "ai.branch_failed");
	  }
	}, [activeSessionId, loadSessions, selectSession, t, toastError]);

  const handleClear = () => {
    handleNewChat();
    setUsage({ prompt: 0, completion: 0 });
  };

  const handleSelectSession = async (id: number) => {
    const sourceSessionId = activeSessionId;
    setUsage({ prompt: 0, completion: 0 });
    const selected = await selectSession(id);
    if (!selected || id === sourceSessionId) return;
	 draftsRef.current.set(draftKey(sourceSessionId), inputRef.current);
	 const sessionDraft = sessions.find((session) => session.id === id)?.draft;
	 replaceInput(sessionDraft ?? draftsRef.current.get(draftKey(id)) ?? "");
	 const selectedSession = sessions.find((session) => session.id === id);
	 setSelectedProfileId(selectedSession?.profile_id ?? selectedProfileId);
	 setLowRiskAuto(selectedSession?.write_policy === "low_risk_auto");
	 setSelectedAttachmentIds([]);
  };

  const handleStartNewChat = () => {
    if (activeSessionId == null) {
      draftsRef.current.delete(draftKey(null));
      replaceInput("");
	} else {
      draftsRef.current.set(draftKey(activeSessionId), inputRef.current);
	  let temporaryDraft = draftsRef.current.get(draftKey(null)) || "";
	  temporaryDraft = readNewAISessionDraft() || temporaryDraft;
	  replaceInput(temporaryDraft);
    }
    handleNewChat();
    setUsage({ prompt: 0, completion: 0 });
  };

  const handleDeleteSession = async (id: number) => {
    const title = sessions.find((session) => session.id === id)?.title || `#${id}`;
    const accepted = await confirmDelete({
      title: t("ai.delete_session"),
      message: t("ai.delete_session_confirm", { title }),
      confirmText: t("common.delete"),
      danger: true,
    });
    if (!accepted) return;
    const deleted = await deleteSession(id);
    if (!deleted) return;
    draftsRef.current.delete(draftKey(id));
    if (activeSessionId === id) replaceInput("");
  };

	const handlePinSession = async (id: number, pinned: boolean) => {
	  try {
		await api.putJson(paths.ai.session(id), { pinned });
		setSessions((current) => current.map((session) => session.id === id ? { ...session, pinned } : session));
		void loadSessions();
	  } catch (error) {
		toastError(error, "ai.pin_session_failed");
	  }
	};

	const handleArchiveSession = async (id: number) => {
	  try {
		await api.putJson(paths.ai.session(id), { archived: true });
		setSessions((current) => current.filter((session) => session.id !== id));
		if (activeSessionId === id) handleStartNewChat();
	  } catch (error) {
		toastError(error, "ai.archive_session_failed");
	  }
	};

  const handleExport = () => {
    const text = messages
      .filter((m) => !m.thinking && !m.trace)
      .map((m) => {
        const roleLabel =
          m.role === "user" ? "[user]" : m.role === "assistant" ? "[assistant]" : `[tool:${m.tool_name}]`;
        return `${roleLabel} ${m.content}`;
      })
      .join("\n\n");
    downloadText(text, "ai-chat-export.txt");
  };

  const lastUserText = (() => {
    for (let i = messages.length - 1; i >= 0; i--) {
      if (messages[i].role === "user") return messages[i].content;
    }
    return "";
  })();
  const followUps = [
    { label: t("ai.followup_next"), query: t("ai.quick_next_query") },
    { label: t("ai.followup_elevated"), query: t("ai.quick_elevated_query") },
    { label: t("ai.followup_alerts"), query: t("ai.quick_alerts_query") },
    { label: t("ai.followup_stale"), query: t("ai.quick_stale_query") },
  ].filter((item) => item.query !== lastUserText);
  const handleFollowUp = useCallback((query: string) => {
    void handleSendRef.current(query);
  }, []);
  const handleBranchMessage = useCallback((messageId: number) => {
    void handleBranch(messageId);
  }, [handleBranch]);

  const handleKeyDown = (e: React.KeyboardEvent) => {
    // While a response is streaming, keep the composer fully editable. Enter
    // becomes a normal newline until the current turn finishes; the draft is
    // then ready to send without being lost.
    if (loading) return;
    if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      handleSend();
    }
  };

  const lastAssistantIndex = (() => {
    let idx = -1;
    messages.forEach((m, i) => {
      if (m.role === "assistant" && !m.thinking && !m.trace) idx = i;
    });
    return idx;
  })();

  return (
    <WorkspaceShell
      className="h-full"
      sidebar={
        <AISessionSidebar
          sessions={sessions}
          sessionsLoading={sessionsLoading}
          sessionsError={sessionsError}
          activeSessionId={activeSessionId}
              onSelect={(id) => { void handleSelectSession(id); }}
          onDelete={(id) => { void handleDeleteSession(id); }}
          onRename={renameSession}
          onNewChat={handleStartNewChat}
		  onPin={(id, pinned) => { void handlePinSession(id, pinned); }}
		  onArchive={(id) => { void handleArchiveSession(id); }}
          selectingSessionId={selectingSessionId}
		  runStatuses={runStatuses}
        />
      }
    >

      {/* Mobile session sidebar */}
      <Sheet open={sidebarOpen} onOpenChange={setSidebarOpen}>
        <SheetContent side="left" className="w-[min(20rem,88vw)] p-0">
          <AISessionSidebar
            sessions={sessions}
            sessionsLoading={sessionsLoading}
            sessionsError={sessionsError}
            activeSessionId={activeSessionId}
              onSelect={(id) => { void handleSelectSession(id); setSidebarOpen(false); }}
            onDelete={(id) => { void handleDeleteSession(id); }}
            onRename={renameSession}
            onNewChat={() => { handleStartNewChat(); setSidebarOpen(false); }}
			onPin={(id, pinned) => { void handlePinSession(id, pinned); }}
			onArchive={(id) => { void handleArchiveSession(id); }}
            selectingSessionId={selectingSessionId}
			runStatuses={runStatuses}
          />
        </SheetContent>
      </Sheet>

      {/* Configuration is secondary workspace chrome. Keeping it in a sheet
          preserves the conversation width and scroll position while editing. */}
      <Sheet open={showSettings} onOpenChange={setShowSettings}>
        <SheetContent side="right" className="w-full overflow-y-auto p-0 sm:max-w-xl">
          <AIConfigPanel
            enabled={enabled}
            setEnabled={setEnabled}
            provider={provider}
            setProvider={setProvider}
            model={model}
            setModel={setModel}
            apiKey={apiKey}
            setApiKey={setApiKey}
            endpoint={endpoint}
            setEndpoint={setEndpoint}
            systemPrompt={systemPrompt}
            setSystemPrompt={setSystemPrompt}
            engagementNotes={engagementNotes}
            setEngagementNotes={setEngagementNotes}
            allowExecute={allowExecute}
            setAllowExecute={setAllowExecute}
            configSaving={configSaving}
            configLoaded={configLoaded}
            onClose={() => setShowSettings(false)}
            onSave={handleSaveConfig}
          />
        </SheetContent>
      </Sheet>

	  <Sheet open={contextPanelOpen} onOpenChange={setContextPanelOpen}>
		<SheetContent side="right" className="w-full p-0 sm:max-w-md">
		  <AIContextPanel
			sessionId={activeSessionId}
			profileId={selectedProfileId}
			selectedAttachmentIds={selectedAttachmentIds}
			selectedCollectionIds={selectedCollectionIds}
			lowRiskAuto={lowRiskAuto}
			onProfileChange={setSelectedProfileId}
			onAttachmentIdsChange={setSelectedAttachmentIds}
			onCollectionIdsChange={setSelectedCollectionIds}
			onLowRiskAutoChange={setLowRiskAuto}
		  />
		</SheetContent>
	  </Sheet>

      <div className="flex h-full min-h-0 min-w-0 w-full flex-1 flex-col bg-muted/20">
        <header className="flex min-h-16 shrink-0 items-center justify-between gap-3 border-b border-border/75 bg-card px-3 py-2.5 sm:px-5">
          <div className="flex min-w-0 items-center gap-3">
            <Button
              variant="outline"
              size="icon"
              onClick={() => setSidebarOpen(true)}
              className="lg:hidden"
              aria-label={t("ai.toggle_sessions")}
            >
              <Menu className="size-4" />
            </Button>
            <div className="icon-well hidden size-9 border border-primary/15 bg-primary/10 text-primary sm:flex sm:size-10">
              <Bot className="size-4" />
            </div>
            <div className="min-w-0">
              <div className="flex min-w-0 items-center gap-2">
                <h1 className="truncate text-sm font-semibold text-foreground sm:text-base">{t("nav.ai")}</h1>
                <Badge variant={configured ? "success" : "warning"} className="hidden max-w-48 truncate font-mono text-micro-sm sm:inline-flex">
                  {configLoading ? t("common.loading") : configError ? t("ai.config_status_unknown") : enabled ? (model || provider) : t("ai.status_disabled")}
                </Badge>
				{activeSessionId != null && runStatuses[activeSessionId] && <Badge variant="info" className="animate-pulse motion-reduce:animate-none">{runStatuses[activeSessionId]}</Badge>}
              </div>
              <p className="truncate text-xs text-muted-foreground">
                {activeSessionId != null ? `${t("ai.sessions")} #${activeSessionId}` : t("ai.new_chat")}
              </p>
            </div>
          </div>
          <div className="flex shrink-0 items-center gap-1.5">
			<Button variant="ghost" size="sm" onClick={() => setContextPanelOpen(true)} aria-label={t("ai.context_title")} title={t("ai.context_title")}>
			  <SlidersHorizontal className="size-4" /> <span className="hidden sm:inline">{t("ai.context")}</span>
			</Button>
            <Button variant="outline" size="icon" onClick={handleStartNewChat} className="lg:hidden" aria-label={t("ai.new_chat")} title={t("ai.new_chat")}>
              <Plus className="size-4" />
            </Button>
            {canConfigure && (
              <Button variant="ghost" size="sm" onClick={() => setShowSettings(true)} aria-label={t("ai.config_title")} title={t("ai.config_title")}>
                <Settings className="size-4" /> <span className="hidden sm:inline">{t("common.edit")}</span>
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={handleExport} disabled={messages.length === 0} className="hidden sm:inline-flex" aria-label={t("ai.export")} title={t("ai.export")}>
              <Download className="size-4" /> <span className="hidden sm:inline">{t("ai.export")}</span>
            </Button>
            <Button variant="ghost" size="sm" onClick={handleClear} disabled={messages.length === 0} className="hidden text-muted-foreground hover:text-destructive-ink sm:inline-flex" aria-label={t("ai.clear")} title={t("ai.clear")}>
              <Trash2 className="size-4" /> <span className="hidden sm:inline">{t("ai.clear")}</span>
            </Button>
            <DropdownMenu>
              <DropdownMenuTrigger render={<Button variant="ghost" size="icon" className="sm:hidden" aria-label={t("common.more")} />}>
                <MoreHorizontal className="size-4" />
              </DropdownMenuTrigger>
              <DropdownMenuContent align="end" className="w-40">
                <DropdownMenuItem disabled={messages.length === 0} onClick={handleExport}>
                  <Download className="size-4" /> {t("ai.export")}
                </DropdownMenuItem>
                <DropdownMenuItem disabled={messages.length === 0} variant="destructive" onClick={handleClear}>
                  <Trash2 className="size-4" /> {t("ai.clear")}
                </DropdownMenuItem>
              </DropdownMenuContent>
            </DropdownMenu>
          </div>
        </header>

        {configError && (
          <div role="alert" className="shrink-0 border-b border-destructive/25 bg-destructive/8 px-3 py-2.5 sm:px-5">
            <div className="mx-auto flex w-full max-w-4xl flex-col gap-1 text-sm text-destructive-ink sm:flex-row sm:items-center sm:justify-between">
              <span>{t("ai.config_unread", { message: configError })}</span>
              <Button variant="ghost" size="xs" onClick={() => { void loadConfig(); }} className="h-7 self-start text-destructive-ink sm:self-auto">
                {t("common.try_again")}
              </Button>
            </div>
          </div>
        )}

        {!configLoading && !configError && !configured && (
          <div className="shrink-0 border-b border-warning/25 bg-warning/8 px-3 py-2.5 sm:px-5">
            <div className="mx-auto flex w-full max-w-4xl flex-col gap-1 text-sm text-warning-ink sm:flex-row sm:items-center sm:justify-between">
              <span>{canConfigure
                ? (hasApiKey ? t("ai.disabled_warning") : t("ai.no_api_key_warning"))
                : t("ai.contact_admin_warning")}</span>
              {canConfigure && (
                <Button variant="ghost" size="xs" onClick={() => setShowSettings(true)} className="h-7 self-start text-warning-ink sm:self-auto">
                  {t("ai.configure_now")}
                </Button>
              )}
            </div>
          </div>
        )}

        <div className="relative flex min-h-0 flex-1">
          <AIMessageList
            messages={messages}
            loading={loading}
            lastAssistantIndex={lastAssistantIndex}
            quickActions={quickActions}
            followUps={followUps}
              onFollowUp={handleFollowUp}
              onRegenerate={handleRegenerate}
			 onBranch={handleBranchMessage}
            messagesEndRef={messagesEndRef}
          />
          {showJumpToLatest && (
            <Button
              type="button"
              variant="outline"
              size="sm"
              onClick={() => {
                stickToBottomRef.current = true;
                setShowJumpToLatest(false);
                scheduleScrollToBottom(true);
              }}
              className="absolute bottom-3 left-1/2 z-10 -translate-x-1/2 rounded-full bg-card/95 shadow-md backdrop-blur"
              aria-label={t("ai.jump_latest")}
            >
              <ArrowDown className="size-3.5" />
              {t("ai.jump_latest")}
            </Button>
          )}
        </div>

        <div className="mx-auto w-full max-w-4xl shrink-0 px-3 sm:px-5">
		  <PendingAIIntents activeSessionId={activeSessionId} />
        </div>

        {contextAgentId && (
          <div className="mx-auto flex w-full max-w-4xl items-center gap-2 px-4 pb-1.5 text-xs text-muted-foreground sm:px-5">
            <span>{t("ai.context_target")}</span>
            <Badge variant="info" className="font-mono">{contextAgentId.slice(0, 16)}</Badge>
            <button
              type="button"
              onClick={() => setContextAgentId("")}
              className="underline underline-offset-2 hover:text-foreground"
            >
              {t("common.clear")}
            </button>
          </div>
        )}

        <div className="shrink-0 border-t border-border/70 bg-card/95 px-3 py-3 sm:px-5 sm:py-4">
          <div className="mx-auto w-full max-w-4xl">
            <AIAssistBar
              sessionId={activeSessionId}
              disabled={configLoading || !configured}
              onInsertPrompt={(prompt) => { replaceInput(prompt); adjustTextarea(); textareaRef.current?.focus(); }}
            />
            <AIComposer
              input={input}
              loading={loading}
              disabled={configLoading || !configured}
              messageCount={messages.filter((m) => !m.thinking && !m.trace).length}
              usage={usage}
              maxLength={AI_INPUT_MAX_CHARS}
              chatOnly={chatOnly}
              textareaRef={textareaRef}
              onChange={(v) => { replaceInput(v); adjustTextarea(); }}
              onKeyDown={handleKeyDown}
              onSend={handleSend}
              onStop={handleStop}
              onChatOnlyChange={setChatOnly}
            />
          </div>
        </div>
      </div>

      <Dialog open={!!renameTarget} onOpenChange={() => setRenameTarget(null)}>
        <DialogContent size="md">
          <DialogHeader>
            <DialogTitle>{t("ai.rename")}</DialogTitle>
          </DialogHeader>
          <Input
            type="text"
            maxLength={255}
            value={renameValue}
            onChange={(e) => setRenameValue(e.target.value)}
            onKeyDown={(e) => { if (e.key === "Enter") confirmRename(); }}
            className="w-full"
            autoFocus
          />
          <DialogFooter>
            <Button variant="outline" onClick={() => setRenameTarget(null)}>{t("common.cancel")}</Button>
            <Button onClick={confirmRename}>{t("common.rename")}</Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
      {deleteSessionModal}
    </WorkspaceShell>
  );
}
