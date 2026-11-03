import type { TimelineEvent, UnifiedEvent, UnifiedSource } from "./types";
import type { Task } from "@/types/task";

export interface AlertLike {
  id: number;
  type: string;
  title: string;
  message: string;
  agent_id?: string;
  severity?: string;
  created_at: string;
}

export interface EventSourceCounts {
  timeline: number;
  task: number;
  alert: number;
}

export function countEventSources(events: UnifiedEvent[]): EventSourceCounts {
  const counts: EventSourceCounts = { timeline: 0, task: 0, alert: 0 };
  for (const event of events) counts[event.source]++;
  return counts;
}

export function mergeEvents(
  timeline: TimelineEvent[],
  tasks: Task[],
  alerts: AlertLike[],
): UnifiedEvent[] {
  const out: UnifiedEvent[] = [];

  for (const e of timeline) {
    out.push({
      id: `tl-${e.id || e.timestamp || out.length}`,
      at: e.timestamp || "",
      source: "timeline",
      kind: (e.type || "system").toLowerCase(),
      title: e.title || e.type || "event",
      detail: e.description || "",
      agentId: e.agent_id,
      href: e.url,
    });
  }

  for (const task of tasks) {
    out.push({
      id: `task-${task.id}`,
      at: task.created_at || task.updated_at || "",
      source: "task",
      kind: task.type || "task",
      title: task.type || "task",
      detail: task.command || task.result || "",
      agentId: task.agent_id,
      status: task.status,
    });
  }

  for (const n of alerts) {
    out.push({
      id: `alert-${n.id}`,
      at: n.created_at || "",
      source: "alert",
      kind: n.type || n.severity || "alert",
      title: n.title || n.type || "alert",
      detail: n.message || "",
      agentId: n.agent_id,
      status: n.severity,
    });
  }

  return out.sort((a, b) => {
    const at = a.at ? new Date(a.at).getTime() : 0;
    const bt = b.at ? new Date(b.at).getTime() : 0;
    return bt - at;
  });
}

export function filterUnified(
  events: UnifiedEvent[],
  source: UnifiedSource | "all",
  query: string,
  agentId?: string | null,
): UnifiedEvent[] {
  const q = query.trim().toLowerCase();
  return events.filter((e) => {
    if (source !== "all" && e.source !== source) return false;
    if (agentId && (e.agentId || "") !== agentId) return false;
    if (!q) return true;
    return (
      e.title.toLowerCase().includes(q) ||
      e.detail.toLowerCase().includes(q) ||
      e.kind.toLowerCase().includes(q) ||
      (e.agentId || "").toLowerCase().includes(q)
    );
  });
}
