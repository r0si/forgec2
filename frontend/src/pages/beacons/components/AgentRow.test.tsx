import { describe, it, expect, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { AgentRow } from "./AgentRow";
import type { Beacon } from "./types";

vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));

const BEACON: Beacon = {
  id: "agent-7",
  hostname: "demo-workstation-01",
  ip: "10.20.30.40",
  status: "offline",
  failure_count: 3,
};

function renderRow(beacon: Beacon = BEACON) {
  return render(
    <MemoryRouter>
      <table>
        <tbody>
          <AgentRow
            beacon={beacon}
            isSelected={false}
            onToggleSelect={() => {}}
            onInteract={() => {}}
            onDetails={() => {}}
            onMenu={() => {}}
            taskCount={0}
            lockUser={null}
            presenceUsers={null}
            visibleCols={{ hostname: true, status: true, failures: true }}
          />
        </tbody>
      </table>
    </MemoryRouter>,
  );
}

describe("AgentRow failure count", () => {
  it("links a beacon's failures to exactly those operations", () => {
    renderRow();
    const link = screen.getByRole("link", { name: "3" });
    expect(link.getAttribute("href")).toBe(
      "/audit?tab=operations&result=failed&agent_id=agent-7",
    );
  });

  it("shows a plain zero when nothing failed", () => {
    renderRow({ ...BEACON, failure_count: 0 });
    expect(screen.getByText("0")).toBeTruthy();
    expect(screen.queryByRole("link")).toBeNull();
  });

  it("hides the column when it is switched off", () => {
    render(
      <MemoryRouter>
        <table>
          <tbody>
            <AgentRow
              beacon={BEACON}
              isSelected={false}
              onToggleSelect={() => {}}
              onInteract={() => {}}
              onDetails={() => {}}
              onMenu={() => {}}
              taskCount={0}
              lockUser={null}
              presenceUsers={null}
              visibleCols={{ hostname: true, status: true, failures: false }}
            />
          </tbody>
        </table>
      </MemoryRouter>,
    );
    expect(screen.queryByRole("link", { name: "3" })).toBeNull();
  });
});
