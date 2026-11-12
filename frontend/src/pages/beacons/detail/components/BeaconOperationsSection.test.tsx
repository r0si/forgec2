import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import BeaconOperationsSection from "./BeaconOperationsSection";
import { api } from "@/lib/api";

const can = vi.fn(() => true);
vi.mock("@/lib/hooks/usePermissions", () => ({ usePermissions: () => ({ can }) }));
vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));

beforeEach(() => {
  vi.clearAllMocks();
  can.mockReturnValue(true);
  vi.mocked(api.get).mockResolvedValue({
    logs: [
      {
        id: "1",
        user: "demo",
        action: "operator:task_create",
        success: true,
        created_at: "2026-11-11T09:00:00Z",
        details: "shell task queued",
      },
      {
        id: "2",
        user: "demo",
        action: "command_result",
        success: false,
        created_at: "2026-11-11T08:00:00Z",
        details: "whoami /priv failed",
      },
    ],
    total: 2,
    failed: 1,
  });
});

function renderSection() {
  return render(
    <MemoryRouter>
      <BeaconOperationsSection agentId="a1" />
    </MemoryRouter>,
  );
}

describe("BeaconOperationsSection", () => {
  it("lists the newest operations that touched the beacon", async () => {
    renderSection();
    // The label falls back to a humanized name when the dictionary has no
    // entry for the action.
    expect(await screen.findByText("Task create")).toBeTruthy();
    expect(screen.getByText("Command result")).toBeTruthy();

    const link = screen.getByRole("link", { name: /agents.operations_view_all/ });
    expect(link.getAttribute("href")).toBe("/audit?tab=operations&agent_id=a1");

    const call = String(vi.mocked(api.get).mock.calls[0][0]);
    expect(call).toContain("agent_id=a1");
    expect(call).toContain("scope=operations");
    expect(call).toContain("pageSize=5");
  });

  it("marks a failed operation so the operator sees which one broke", async () => {
    renderSection();
    expect(await screen.findByText("agents.operations_failed")).toBeTruthy();
    // The failing row keeps its action label and its detail.
    expect(screen.getByText("Command result")).toBeTruthy();
    expect(screen.getByText("whoami /priv failed")).toBeTruthy();
  });

  it("asks the server for the failures only when the toggle is on", async () => {
    renderSection();
    await screen.findByText("Command result");

    fireEvent.click(screen.getByLabelText("agents.operations_failures_only"));

    await waitFor(() => {
      const calls = vi.mocked(api.get).mock.calls.map(([p]) => String(p));
      const last = calls[calls.length - 1];
      expect(last).toContain("result=failed");
      expect(last).toContain("agent_id=a1");
    });
    // The "view all" link carries the same selection.
    expect(screen.getByRole("link", { name: /agents.operations_view_all/ }).getAttribute("href")).toBe(
      "/audit?tab=operations&agent_id=a1&result=failed",
    );
  });

  it("renders nothing for a role without audit.read", () => {
    can.mockReturnValue(false);
    renderSection();
    expect(screen.queryByText("agents.operations_title")).toBeNull();
    expect(api.get).not.toHaveBeenCalled();
  });

  it("reports an unavailable history instead of breaking the page", async () => {
    vi.mocked(api.get).mockRejectedValue(new Error("offline"));
    renderSection();
    expect(await screen.findByText("agents.operations_unavailable")).toBeTruthy();
  });

  it("says so when the beacon has no operations", async () => {
    vi.mocked(api.get).mockResolvedValue({ logs: [], total: 0, failed: 0 });
    renderSection();
    expect(await screen.findByText("agents.operations_empty")).toBeTruthy();
  });
});
