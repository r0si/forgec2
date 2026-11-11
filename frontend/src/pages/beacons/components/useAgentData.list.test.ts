import { renderHook, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { useAgentData } from "./useAgentData";

const { getMock } = vi.hoisted(() => ({ getMock: vi.fn() }));

vi.mock("@/lib/api", () => ({
  api: {
    get: (...args: unknown[]) => getMock(...args),
    postJson: vi.fn(),
  },
}));

const t = (key: string) => key;

/**
 * The fleet list is the product's main view, and it stayed empty because the
 * list payload was read at the top level of a body that carries it one level
 * down. This pins the real response shape so the page cannot go blank again.
 */
describe("useAgentData fleet list", () => {
  beforeEach(() => {
    getMock.mockReset();
    getMock.mockImplementation((url: string) => {
      if (typeof url === "string" && url.startsWith("/collab/agents")) return Promise.resolve({ agents: [] });
      if (typeof url === "string" && url.includes("/tags")) return Promise.resolve([]);
      return Promise.resolve({
        success: true,
        data: {
          agents: [{ id: "a1", hostname: "demo-workstation-01" }],
          data: [{ id: "a1", hostname: "demo-workstation-01" }],
          total: 1,
          page: 1,
          page_size: 50,
        },
      });
    });
  });

  it("lists the agents the response envelope carries", async () => {
    const { result } = renderHook(() => useAgentData(t));
    await waitFor(() => expect(result.current.beacons.length).toBe(1));
    expect(result.current.beacons[0].hostname).toBe("demo-workstation-01");
    expect(result.current.total).toBe(1);
  });
});
