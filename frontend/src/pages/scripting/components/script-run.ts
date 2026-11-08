export interface SavedScript {
  id?: string;
  name?: string;
  code?: string;
  created_at?: string;
  created_by?: string;
}

export interface ScriptRun {
  id?: string;
  script_name?: string;
  agent_id?: string;
  user?: string;
  status?: string;
  error?: string;
  created_at?: string;
}

function asRecord(value: unknown): Record<string, unknown> | null {
  return value !== null && typeof value === "object" && !Array.isArray(value)
    ? (value as Record<string, unknown>)
    : null;
}

/**
 * api.get() unwraps a `{success, data}` envelope, so GET /api/scripts already
 * arrives as a bare Script[]; the keyed shapes are tolerated for older or
 * hand-rolled callers instead of silently yielding an empty list.
 */
export function extractSavedScripts(payload: unknown): SavedScript[] {
  if (Array.isArray(payload)) return payload as SavedScript[];
  const record = asRecord(payload);
  if (!record) return [];
  for (const key of ["scripts", "data"]) {
    const value = record[key];
    if (Array.isArray(value)) return value as SavedScript[];
  }
  return [];
}

/** GET /api/scripts/history answers `{success, history}` with no data key, so
 * the envelope reaches the caller intact. */
export function extractRunHistory(payload: unknown): ScriptRun[] {
  const record = asRecord(payload);
  if (!record) return [];
  return Array.isArray(record.history) ? (record.history as ScriptRun[]) : [];
}

/** POST /api/scripts/execute answers `{success, result: {success, output, error}}`.
 * `result` is an object: assigning it to rendered output crashed the page with
 * "Objects are not valid as a React child". */
export function scriptRunError(payload: unknown): string {
  const record = asRecord(payload);
  if (!record) return "";
  const result = asRecord(record.result) ?? asRecord(record.data);
  const error = result ? result.error : record.error;
  return typeof error === "string" ? error.trim() : "";
}

export function normalizeScriptOutput(payload: unknown, fallback: string): string {
  const record = asRecord(payload);
  if (!record) {
    return typeof payload === "string" && payload !== "" ? payload : fallback;
  }
  const error = scriptRunError(payload);
  if (error) return error;

  const result = asRecord(record.result) ?? asRecord(record.data) ?? record;
  const output = result.output;
  if (typeof output === "string") return output === "" ? fallback : output;
  if (output === undefined || output === null) return fallback;
  if (typeof output === "object") {
    try {
      return JSON.stringify(output, null, 2);
    } catch {
      return fallback;
    }
  }
  return String(output);
}
