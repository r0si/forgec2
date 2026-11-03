export interface TrafficEntry {
  id?: string;
  ID?: string;
  timestamp?: string;
  Timestamp?: string;
  time?: string;
  method?: string;
  Method?: string;
  path?: string;
  Path?: string;
  source_ip?: string;
  SourceIP?: string;
  remote_ip?: string;
  agent_id?: string;
  AgentID?: string;
  status_code?: number;
  StatusCode?: number;
  status?: number;
  size?: number;
  Size?: number;
  latency?: number | string;
  Latency?: number | string;
  protocol?: string;
  Protocol?: string;
}

function finiteNumber(value: unknown): number {
  const parsed = typeof value === "string" ? Number.parseFloat(value) : Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
}

export function normalizeTrafficEntry(raw: TrafficEntry): TrafficEntry {
  const entry = raw as Record<string, unknown>;
  return {
    id: String(entry.id || entry.ID || entry.time || entry.timestamp || ""),
    timestamp: String(entry.timestamp || entry.Timestamp || entry.time || ""),
    method: String(entry.method || entry.Method || ""),
    path: String(entry.path || entry.Path || ""),
    source_ip: String(entry.source_ip || entry.SourceIP || entry.remote_ip || ""),
    agent_id: String(entry.agent_id || entry.AgentID || ""),
    status_code: finiteNumber(entry.status_code ?? entry.StatusCode ?? entry.status),
    size: finiteNumber(entry.size ?? entry.Size),
    latency: finiteNumber(entry.latency ?? entry.Latency),
    protocol: String(entry.protocol || entry.Protocol || ""),
  };
}

export function summarizeTraffic(entries: TrafficEntry[], sourceIpFilter: string) {
  const filteredEntries = sourceIpFilter
    ? entries.filter((entry) => (entry.source_ip || "").includes(sourceIpFilter))
    : entries;
  const sourceIps = [...new Set(entries.map((entry) => entry.source_ip || "").filter(Boolean))];
  const uniqueBeacons = new Set(entries.map((entry) => entry.agent_id || "").filter(Boolean)).size;
  const errors = entries.filter((entry) => (entry.status_code ?? 0) >= 400).length;
  const dataTransferred = entries.reduce((total, entry) => total + (entry.size ?? 0), 0);
  return {
    filteredEntries,
    sourceIps,
    totalRequests: entries.length,
    beacons: uniqueBeacons,
    errors,
    dataTransferred,
  };
}
