import { describe, expect, it } from "vitest";
import { countEventSources, filterUnified, mergeEvents } from "./merge-events";

describe("mergeEvents", () => {
  it("sorts mixed sources newest first", () => {
    const merged = mergeEvents(
      [{ id: "1", timestamp: "2026-01-01T10:00:00Z", type: "agent_online", title: "up" }],
      [{ id: 2, agent_id: "a", type: "shell", command: "whoami", status: "completed", result: "", error: "", created_by: "", claimed_by: "", claimed_at: "", created_at: "2026-01-01T12:00:00Z", updated_at: "" }],
      [{ id: 3, type: "task_failed", title: "fail", message: "boom", created_at: "2026-01-01T11:00:00Z" }],
    );
    expect(merged.map((e) => e.source)).toEqual(["task", "alert", "timeline"]);
  });
});

describe("filterUnified", () => {
  it("filters by source and query", () => {
    const events = mergeEvents(
      [{ id: "1", timestamp: "2026-01-01T10:00:00Z", type: "agent_online", title: "beacon up" }],
      [],
      [{ id: 3, type: "task_failed", title: "fail", message: "boom", created_at: "2026-01-01T11:00:00Z" }],
    );
    expect(filterUnified(events, "alert", "").length).toBe(1);
    expect(filterUnified(events, "all", "beacon").map((e) => e.source)).toEqual(["timeline"]);
  });

  it("filters to one session when agentId is set", () => {
    const events = mergeEvents(
      [{ id: "1", timestamp: "2026-01-01T10:00:00Z", type: "agent_online", title: "up", agent_id: "aaa" }],
      [{ id: 2, agent_id: "bbb", type: "shell", command: "whoami", status: "completed", result: "", error: "", created_by: "", claimed_by: "", claimed_at: "", created_at: "2026-01-01T12:00:00Z", updated_at: "" }],
      [],
    );
    expect(filterUnified(events, "all", "", "aaa")).toHaveLength(1);
    expect(filterUnified(events, "all", "", "bbb")[0].source).toBe("task");
    expect(filterUnified(events, "all", "")).toHaveLength(2);
  });
});

describe("countEventSources", () => {
  it("counts the merged stream, including live rows", () => {
    const polled = mergeEvents(
      [{ id: "1", timestamp: "2026-01-01T10:00:00Z", type: "agent_online", title: "up" }],
      [],
      [],
    );
    const live = { ...polled[0], id: "task-live", source: "task" as const };
    expect(countEventSources([...polled, live])).toEqual({ timeline: 1, task: 1, alert: 0 });
  });
});
