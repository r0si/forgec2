import { describe, it, expect, vi } from "vitest";

// The dock body is only reachable through a lazy import; stub it so this test
// exercises the loader (and the fragment registration) without xterm/WebSocket.
vi.mock("@/pages/beacons/components/AgentInteractDock", () => ({
  AgentInteractDock: () => null,
}));

import { loadAgentInteractDock } from "./GlobalInteractDock";
import { fragmentValue } from "@/lib/i18n";

// The global dock is mounted by AppLayout and can be opened from a page whose
// entry never imports the beacon fragment (dashboard, events, ...). It renders
// agents.* labels, so its loader must register the fragment; otherwise those
// labels appear as raw dotted keys. This shipped once in the dictionary split
// and was only visible by opening the dock from /dashboard.
describe("GlobalInteractDock fragment", () => {
  it("registers the agents dictionary when the dock chunk loads", async () => {
    // Start from an unregistered state (the loader is idempotent).
    expect(fragmentValue("agents.agents_count")).toBeUndefined();
    const mod = await loadAgentInteractDock();
    expect(mod.default).toBeTruthy();
    expect(fragmentValue("agents.agents_count")).toContain("Beacons");
    expect(fragmentValue("agents.header_more")).toBeTruthy();
  });
});
