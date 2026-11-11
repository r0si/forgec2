import type { components } from "./api-schema";
import { api } from "./api";
import { paths } from "./api-paths";
import { firstArray, firstNumber, unwrapEnvelope } from "./envelope";

/**
 * Contract-backed API layer.
 *
 * DTO types are generated at compile time from the OpenAPI spec
 * (npm run gen:openapi -> src/lib/api-schema.d.ts) and carry zero runtime
 * cost. The generated file is the single source of truth — do not hand-edit
 * it; `npm run check:openapi-types` fails if it drifts from the spec.
 */

type AgentDTO = components["schemas"]["Agent"];

/**
 * Wire query for GET /api/agents. Field names match what the backend parses
 * (page_size, not the spec's pageSize); values mirror the spec's enums.
 */
interface AgentsListQuery {
  page?: number;
  page_size?: number;
  search?: string;
  status?: "online" | "stale" | "offline";
  os?: string;
  linked?: "direct" | "chained";
  tag_id?: string;
  group?: string;
  sort_key?: "hostname" | "username" | "os" | "ip" | "last_seen" | "version" | "status";
  sort_dir?: "asc" | "desc";
}

interface AgentsPage {
  agents: AgentDTO[];
  total: number;
}

/**
 * Typed GET /api/agents with pagination metadata (reads the raw envelope via
 * unwrap:false). Tolerates the data/agents/Agents list keys /api/agents has
 * historically been served under, and coerces total (number | string).
 */
export async function fetchAgentsPage(query: AgentsListQuery = {}, signal?: AbortSignal): Promise<AgentsPage> {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(query)) {
    if (v === undefined || v === "") continue;
    q.set(k, String(v));
  }
  const suffix = q.size ? `?${q.toString()}` : "";
  const raw = await api.get<unknown>(paths.agents.list(suffix), { unwrap: false, signal });
  // The server wraps every 200 body, so the list is under `data`. Reading the
  // top level found nothing and reported an empty fleet.
  const body = unwrapEnvelope(raw);
  const agents = firstArray(body, ["agents", "Agents", "data"]) as AgentDTO[];
  return { agents, total: firstNumber(body, ["total"], agents.length) };
}