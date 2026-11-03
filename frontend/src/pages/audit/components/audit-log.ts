import type { AuditLog } from "@/types/audit";

const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[1-8][0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/i;

function asRecord(v: unknown): Record<string, unknown> | null {
  if (!v || typeof v !== "object" || Array.isArray(v)) return null;
  return v as Record<string, unknown>;
}

function firstStr(obj: Record<string, unknown>, keys: string[], fallback = ""): string {
  for (const k of keys) {
    if (obj[k] != null && obj[k] !== "") return String(obj[k]);
  }
  return fallback;
}

export function looksLikeAgentId(value: string): boolean {
  return UUID_RE.test(value.trim());
}

export function auditSessionId(log: Pick<AuditLog, "agent_id" | "target">): string {
  const fromField = String(log.agent_id || "").trim();
  if (fromField) return fromField;
  const target = String(log.target || "").trim();
  return looksLikeAgentId(target) ? target : "";
}

export function normalizeAuditLog(raw: unknown): AuditLog | null {
  const o = asRecord(raw);
  if (!o) return null;
  const success = o.success;
  const status =
    firstStr(o, ["status", "Status"]) ||
    (success === false ? "failed" : success === true ? "success" : "");
  const agentId = firstStr(o, ["agent_id", "AgentID"]);
  return {
    id: firstStr(o, ["id", "ID"]),
    timestamp: firstStr(o, ["timestamp", "Timestamp", "created_at", "CreatedAt"]),
    username: firstStr(o, ["username", "Username", "user", "User"]),
    action: firstStr(o, ["action", "Action"]),
    resource: firstStr(o, ["resource", "Resource"]),
    target: firstStr(o, ["target", "Target"]) || agentId,
    status,
    details: firstStr(o, ["details", "Details"]),
    ip: firstStr(o, ["ip", "IP"]),
    severity: firstStr(o, ["severity", "Severity"], status === "failed" ? "error" : "info"),
    agent_id: agentId,
  };
}

export function normalizeAuditLogs(raw: unknown): AuditLog[] {
  const list = Array.isArray(raw) ? raw : [];
  return list.map(normalizeAuditLog).filter((row): row is AuditLog => row != null);
}

const CSV_HEADERS = ["Timestamp", "User", "IP", "Action", "Resource", "Target", "Status", "Severity", "Details"];

function csvCell(value: unknown): string {
  let text = String(value ?? "");
  // CSV files are routinely opened in spreadsheet software. Prefix values
  // that would otherwise be interpreted as formulas while preserving the
  // original text for an operator reading the cell.
  if (/^[=+\-@\t\r]/.test(text.trimStart())) text = `'${text}`;
  return `"${text.replace(/"/g, '""')}"`;
}

export function auditLogsToCSV(logs: AuditLog[]): string {
  const rows = logs.map((log) => [
    log.timestamp,
    log.username,
    log.ip,
    log.action,
    log.resource,
    log.target,
    log.status,
    log.severity,
    log.details,
  ].map(csvCell).join(","));
  return [CSV_HEADERS.map(csvCell).join(","), ...rows].join("\r\n") + "\r\n";
}
