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

// csvCell quotes one value for a CSV file, with formula-injection protection.
//
// Exported because the login log has its own columns but must escape cells the
// same way: two copies of this rule is how one export ends up vulnerable while
// the other is fixed (the decided pattern in this repo: one implementation).
export function csvCell(value: unknown): string {
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

/**
 * The operations log stores actions in two namespaces: the middleware writes
 * coarse values (`agent_action`, `settings_change`) and LogOperatorAction
 * writes `operator:<name>`. The UI labels the bare name in both cases.
 */
export function auditActionCode(action: string): string {
  return String(action || "").replace(/^operator:/, "");
}

/**
 * A readable fallback for an action with no dictionary entry. The catalogue
 * covers the common verbs; a future action must still render as words rather
 * than as `new_thing_done` the day it ships.
 */
export function humanizeAction(action: string): string {
  const words = auditActionCode(action).replace(/[:_-]+/g, " ").trim();
  if (!words) return "";
  return words.charAt(0).toUpperCase() + words.slice(1);
}

export function auditActionLabel(
  t: (key: string) => string,
  action: string,
): string {
  const code = auditActionCode(action);
  if (!code) return "";
  const key = `audit.act.${code}`;
  const label = t(key);
  return label === key ? humanizeAction(code) : label;
}

/**
 * The badge tone is derived from the verb, not from the category: a
 * destructive operation on a listener must look as dangerous as one on a
 * beacon. Order matters — "disable" contains no "enable", but "revoke" and
 * "reset" can appear in the same string, so destructive wins first.
 */
export function auditActionTone(action: string): "success" | "secondary" | "warning" | "destructive" {
  const a = auditActionCode(action).toLowerCase();
  if (/(delete|purge|revoke|kill|stop|disable|close|reject|deny|fail|tamper|expire|unlink|quarantine|stop)/.test(a)) {
    return "destructive";
  }
  if (/(create|start|enable|approve|install|add|upload|run|execute|dispatch|import|link|restore|recover)/.test(a)) {
    return "success";
  }
  if (/(update|edit|config|change|set|rotate|toggle|reset|regenerate|save|modify|rename)/.test(a)) {
    return "warning";
  }
  return "secondary";
}
