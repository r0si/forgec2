import { describe, expect, it } from "vitest";
import {
  auditActionLabel,
  auditActionTone,
  auditLogsToCSV,
  auditSessionId,
  humanizeAction,
  looksLikeAgentId,
  normalizeAuditLog,
  normalizeAuditLogs,
} from "./audit-log";

describe("normalizeAuditLog", () => {
  it("maps teamserver fields onto the operator row", () => {
    const row = normalizeAuditLog({
      id: 9,
      user: "alice",
      action: "file_upload_push",
      resource: "agent",
      agent_id: "11111111-2222-4333-8444-555555555555",
      ip: "10.0.0.8",
      success: true,
      details: "C:\\secret.txt",
      created_at: "2026-08-14T12:00:00Z",
    });
    expect(row).toMatchObject({
      id: "9",
      username: "alice",
      timestamp: "2026-08-14T12:00:00Z",
      status: "success",
      target: "11111111-2222-4333-8444-555555555555",
      agent_id: "11111111-2222-4333-8444-555555555555",
      severity: "info",
    });
  });

  it("marks failed actions", () => {
    expect(normalizeAuditLog({ success: false, action: "login" })?.status).toBe("failed");
    expect(normalizeAuditLog({ success: false })?.severity).toBe("error");
  });
});

describe("auditSessionId", () => {
  it("prefers agent_id and only guesses UUID targets", () => {
    expect(auditSessionId({ agent_id: "abc" })).toBe("abc");
    expect(auditSessionId({ target: "11111111-2222-4333-8444-555555555555" })).toBe("11111111-2222-4333-8444-555555555555");
    expect(auditSessionId({ target: "login" })).toBe("");
    expect(looksLikeAgentId("not-a-uuid")).toBe(false);
  });
});

describe("normalizeAuditLogs", () => {
  it("drops junk rows", () => {
    expect(normalizeAuditLogs([null, { id: 1, user: "a" }])).toHaveLength(1);
  });
});

describe("auditLogsToCSV", () => {
  it("quotes commas, quotes and newlines without changing the record shape", () => {
    const csv = auditLogsToCSV([{
      id: "1",
      timestamp: "2026-11-03T00:00:00Z",
      username: "alice",
      action: "update",
      details: "changed, \"two\" fields\nnext line",
    }]);
    expect(csv).toContain('"changed, ""two"" fields\nnext line"');
    expect(csv.split("\r\n")).toHaveLength(3);
  });

  it("neutralizes spreadsheet formulas in exported cells", () => {
    const csv = auditLogsToCSV([{
      id: "1",
      username: "=HYPERLINK(\"https://example.test\")",
      details: "+cmd|' /C calc'!A0",
    }]);
    expect(csv).toContain('"\'=HYPERLINK(""https://example.test"")"');
    expect(csv).toContain('"\'+cmd|\' /C calc\'!A0"');
  });
});

describe("operation labels", () => {
  const t = (key: string) => (key === "audit.act.agent_delete" ? "Deleted beacon" : key);

  it("labels a known action and strips the operator namespace", () => {
    expect(auditActionLabel(t, "operator:agent_delete")).toBe("Deleted beacon");
    expect(auditActionLabel(t, "agent_delete")).toBe("Deleted beacon");
  });

  it("humanizes an action the dictionary does not know yet", () => {
    expect(humanizeAction("future_kind_of_thing")).toBe("Future kind of thing");
    expect(auditActionLabel(t, "future_kind_of_thing")).toBe("Future kind of thing");
  });

  it("derives the badge tone from the verb, not the category", () => {
    expect(auditActionTone("agent_delete")).toBe("destructive");
    expect(auditActionTone("listener_create")).toBe("success");
    expect(auditActionTone("settings_change")).toBe("warning");
    expect(auditActionTone("view_tasks")).toBe("secondary");
  });
});
