import { csvCell } from "./audit-log";

/** One row of GET /audit/logins, with the country cached for its IP. */
export interface LoginEvent {
  id: number;
  user: string;
  action: string;
  ip: string;
  country: string;
  city?: string;
  success: boolean;
  details: string;
  created_at: string;
}

/**
 * Columns for a handover export.
 *
 * `Action` and `Result` are separate on purpose: "who signed out" and "who
 * failed to sign in" are different incident questions, and the panel's Details
 * column does not distinguish them reliably.
 */
const LOGIN_CSV_HEADERS = ["Time", "User", "Action", "IP", "Country", "City", "Result", "Details"];

/**
 * loginEventsToCSV renders the current page as CSV.
 *
 * Values are quoted and formula-escaped through the audit log's `csvCell`, so
 * a username or detail line beginning with `=` cannot execute in a spreadsheet.
 * The raw country is kept (`local` for loopback) rather than the localized
 * label: the export leaves the console and must not depend on UI language.
 */
export function loginEventsToCSV(events: LoginEvent[]): string {
  const rows = events.map((e) =>
    [
      e.created_at,
      e.user,
      e.action,
      e.ip,
      e.country,
      e.city ?? "",
      e.success ? "success" : "failed",
      e.details,
    ]
      .map(csvCell)
      .join(","),
  );
  return [LOGIN_CSV_HEADERS.map(csvCell).join(","), ...rows].join("\r\n") + "\r\n";
}

/** loginLogFilename keeps two exports on the same day from overwriting. */
export function loginLogFilename(now = new Date()): string {
  const pad = (n: number) => String(n).padStart(2, "0");
  return `login-log-${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}-${pad(
    now.getHours(),
  )}${pad(now.getMinutes())}.csv`;
}

/** The filters shared by the login table and its CSV export. "all" means no
 *  `since` parameter, so the page and the export cannot describe different
 *  windows. */
export interface LoginFilters {
  user: string;
  result: string;
  range: string;
  /** Only the sign-ins that came from an address the account had never used. */
  newIp?: boolean;
}

export function loginFilterParams(filters: LoginFilters): URLSearchParams {
  const params = new URLSearchParams();
  if (filters.user) params.set("user", filters.user);
  if (filters.result) params.set("result", filters.result);
  if (filters.newIp) params.set("new_ip", "1");
  if (filters.range && filters.range !== "all") params.set("since", filters.range);
  return params;
}
