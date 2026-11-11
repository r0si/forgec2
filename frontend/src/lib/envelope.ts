/**
 * Helpers for dual-use / multi-envelope API responses.
 * Backend may return bare arrays, { data }, { agents }, PascalCase keys, etc.
 */

export function asRecord(data: unknown): Record<string, unknown> | null {
  if (!data || typeof data !== "object" || Array.isArray(data)) return null;
  return data as Record<string, unknown>;
}

/** Return first present array among keys (order = priority). */
export function firstArray(data: unknown, keys: string[]): unknown[] {
  if (Array.isArray(data)) return data;
  const o = asRecord(data);
  if (!o) return [];
  for (const k of keys) {
    const v = o[k];
    if (Array.isArray(v)) return v;
  }
  return [];
}

/** Return first present non-null field among keys. */
export function firstField<T = unknown>(data: unknown, keys: string[]): T | undefined {
  const o = asRecord(data);
  if (!o) return undefined;
  for (const k of keys) {
    if (k in o && o[k] != null) return o[k] as T;
  }
  return undefined;
}

/** Coerce a dual-use page payload number field (snake or Pascal). */
export function firstNumber(data: unknown, keys: string[], fallback = 0): number {
  const v = firstField(data, keys);
  if (typeof v === "number" && !Number.isNaN(v)) return v;
  if (typeof v === "string" && v.trim() !== "") {
    const n = Number(v);
    if (!Number.isNaN(n)) return n;
  }
  return fallback;
}

/**
 * Normalize list envelopes commonly used by ForgeC2:
 * bare array | { data } | { agents/Agents } | { logs/Logs } | { users/Users } | { builds }
 */
export function normalizeListEnvelope(
  data: unknown,
  keys: string[] = ["data", "agents", "Agents", "logs", "Logs", "users", "Users", "builds", "workflows", "groups"],
): unknown[] {
  return firstArray(data, keys);
}

/**
 * Unwrap the standard response envelope one level.
 *
 * Every JSON 200 body from this server is `{"success":true,"data":{...}}`. A
 * caller that reads the raw body (`unwrap:false`) used to find its payload at
 * the top level; after the envelope migration the same payload sits under
 * `data`. Reading the top level then yields undefined - and a list endpoint
 * answers "0 rows" while the rows are right there in the response.
 *
 * This returns the inner object for an envelope body and the body itself
 * otherwise, so a caller reads its fields at one fixed depth. It only unwraps
 * a body that actually carries the envelope markers.
 */
export function unwrapEnvelope(data: unknown): unknown {
  const o = asRecord(data);
  if (!o) return data;
  if ("success" in o && "data" in o) return o.data;
  return data;
}
