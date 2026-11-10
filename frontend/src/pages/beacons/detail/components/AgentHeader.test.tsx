import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AgentHeader from "./AgentHeader";

vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));
const can = vi.fn();
vi.mock("@/lib/hooks/usePermissions", () => ({ usePermissions: () => ({ can }) }));

function renderHeader() {
  return render(
    <MemoryRouter>
      <AgentHeader
        agent={{ hostname: "host-1", ip: "10.0.0.1", os: "windows" }}
        agentId="11111111-2222-4333-8444-555555555555"
        status="online"
        actionLoading={null}
        onQuickAction={vi.fn()}
        credCount={0}
        onKill={vi.fn()}
        onUninstall={vi.fn()}
      />
    </MemoryRouter>,
  );
}

async function openMore() {
  fireEvent.click(screen.getByRole("button", { name: /agents\.header_more/ }));
}

describe("AgentHeader operation-log link", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("opens the beacon's operations log when audit.read is granted", async () => {
    can.mockReturnValue(true);
    renderHeader();
    await openMore();

    const item = await screen.findByRole("menuitem", { name: /agents\.header_operations/ });
    expect(item.getAttribute("href")).toBe(
      "/audit?tab=operations&agent_id=11111111-2222-4333-8444-555555555555",
    );
    expect(can).toHaveBeenCalledWith("audit.read");
  });

  it("hides the link without audit.read", async () => {
    can.mockReturnValue(false);
    renderHeader();
    await openMore();

    expect(screen.queryByRole("menuitem", { name: /agents\.header_operations/ })).toBeNull();
  });
});
