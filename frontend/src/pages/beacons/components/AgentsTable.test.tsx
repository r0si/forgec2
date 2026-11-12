import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AgentsTable from "./AgentsTable";
import type { Beacon } from "./types";

vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));

const SESSIONS: Beacon[] = [
  { id: "a1", hostname: "WORKSTATION-A", ip: "10.0.0.1", status: "online", failure_count: 2 },
  { id: "a2", hostname: "workstation-a", ip: "10.0.0.2", status: "offline", failure_count: 1 },
  { id: "b1", hostname: "WORKSTATION-B", ip: "10.0.0.3", status: "offline", failure_count: 0 },
];

function renderTable(beacons: Beacon[] = SESSIONS) {
  return render(
    <MemoryRouter>
      <AgentsTable
        t={(k: string) => k}
        beacons={beacons}
        visibleBeacons={beacons}
        selected={new Set<string>()}
        toggleSelect={() => {}}
        toggleSelectAll={() => {}}
        sortKey="last_seen"
        sortDir="desc"
        toggleSort={() => {}}
        sortIcon={() => null}
        handleSortKeyDown={() => () => {}}
        visibleCols={{ hostname: true, status: true, failures: true }}
        loading={false}
        emptyColSpan={8}
        agentVirtualized={false}
        agentScrollRef={{ current: null }}
        onAgentScroll={() => {}}
        agentOffsetTop={0}
        agentTotalHeight={0}
        rowHeight={40}
        statusFilter=""
        osFilter=""
        page={1}
        total={beacons.length}
        setPage={() => {}}
        onSelectAgent={() => {}}
        onMenu={() => {}}
        onQuickNav={() => {}}
        onEditNotes={() => {}}
        taskCountMap={{}}
        agentLocks={{}}
        operatorPresence={{}}
        tagsByAgent={{}}
        groupByHost
      />
    </MemoryRouter>,
  );
}

describe("AgentsTable host rows", () => {
  it("sums the host's failures and links them to that host's failures", () => {
    renderTable();

    // The host row carries the sum of its sessions (2 + 1), not one of them.
    const links = screen.getAllByRole("link");
    const hostLink = links.find((a) => (a.getAttribute("href") || "").includes("host="));
    expect(hostLink).toBeTruthy();
    expect(hostLink?.getAttribute("href")).toBe(
      "/audit?tab=operations&result=failed&host=h%3Aworkstation-a",
    );
    expect(hostLink?.textContent).toBe("3");

    // The healthy host has nothing to link to.
    expect(links.filter((a) => (a.getAttribute("href") || "").includes("host="))).toHaveLength(1);
  });

  it("links each session's own failures", () => {
    renderTable();
    const sessionLinks = screen
      .getAllByRole("link")
      .filter((a) => (a.getAttribute("href") || "").includes("agent_id="));
    expect(sessionLinks.map((a) => a.getAttribute("href"))).toEqual([
      "/audit?tab=operations&result=failed&agent_id=a1",
      "/audit?tab=operations&result=failed&agent_id=a2",
    ]);
  });
});
