import { describe, expect, it } from "vitest";
import { normalizeTrafficEntry, summarizeTraffic } from "./traffic-data";

describe("normalizeTrafficEntry", () => {
  it("maps the backend traffic shape and parses duration strings", () => {
    expect(normalizeTrafficEntry({
      time: "2026-11-03T00:00:00Z",
      remote_ip: "10.0.0.4",
      AgentID: "agent-1",
      status: 202,
      Size: 42,
      latency: "12.5ms",
    })).toMatchObject({
      timestamp: "2026-11-03T00:00:00Z",
      source_ip: "10.0.0.4",
      agent_id: "agent-1",
      status_code: 202,
      size: 42,
      latency: 12.5,
    });
  });

  it("turns malformed numeric fields into safe zeroes", () => {
    expect(normalizeTrafficEntry({ status: Number.NaN, latency: "bad", size: Number.POSITIVE_INFINITY }))
      .toMatchObject({ status_code: 0, latency: 0, size: 0 });
  });
});

describe("summarizeTraffic", () => {
  it("counts unique agents instead of the absent protocol field", () => {
    const entries = [
      normalizeTrafficEntry({ source_ip: "10.0.0.1", agent_id: "a", status: 200, size: 10 }),
      normalizeTrafficEntry({ source_ip: "10.0.0.1", agent_id: "a", status: 500, size: 20 }),
      normalizeTrafficEntry({ source_ip: "10.0.0.2", agent_id: "b", status: 204, size: 30 }),
    ];
    expect(summarizeTraffic(entries, "10.0.0.1")).toMatchObject({
      totalRequests: 3,
      beacons: 2,
      errors: 1,
      dataTransferred: 60,
    });
    expect(summarizeTraffic(entries, "10.0.0.1").filteredEntries).toHaveLength(2);
  });
});
