import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { OperationsPanel } from "./OperationsPanel";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), downloadGet: vi.fn() } }));
vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (k: string) =>
      ({
        "audit.act.task_create": "Created task",
        "audit.act.agent_delete": "Deleted beacon",
      })[k] ?? k,
  }),
}));
const can = vi.fn(() => true);
vi.mock("@/lib/hooks/usePermissions", () => ({ usePermissions: () => ({ can }) }));

/** The panel links to the beacon a row belongs to, so it needs a router. */
function renderPanel(props: Partial<React.ComponentProps<typeof OperationsPanel>> = {}) {
  return render(
    <MemoryRouter>
      <OperationsPanel {...props} />
    </MemoryRouter>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  can.mockReturnValue(true);
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    const p = String(path);
    if (p.startsWith("/audit/actions")) {
      return {
        actions: [
          { action: "task_create", category: "tasks", count: 3 },
          { action: "operator:agent_delete", category: "agents", count: 1 },
        ],
        categories: [
          { id: "tasks", count: 3 },
          { id: "agents", count: 1 },
        ],
      };
    }
    if (p.startsWith("/audit/logs")) {
      return {
        logs: [
          {
            id: 1,
            user: "alice",
            action: "task_create",
            resource: "task",
            success: true,
            created_at: "2026-11-10T10:00:00Z",
            details: "created a task",
          },
          {
            id: 2,
            user: "bob",
            action: "operator:agent_delete",
            resource: "agent",
            success: false,
            created_at: "2026-11-10T09:00:00Z",
            details: "delete denied",
          },
        ],
        total: 2,
        failed: 1,
      };
    }
    return {};
  });
});

describe("OperationsPanel", () => {
  it("loads the operator view by default, without beacon transport noise", async () => {
    renderPanel();

    expect(await screen.findByText("Created task")).toBeTruthy();
    // The raw action stays visible as the machine-readable subtitle.
    expect(screen.getByText("task_create")).toBeTruthy();
    expect(screen.getByText("Deleted beacon")).toBeTruthy();

    const logCall = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
    expect(logCall).toBeTruthy();
    const query = String(logCall?.[0]);
    expect(query).toContain("scope=operations");
    expect(query).toContain("since=7d");
    expect(query).not.toContain("scope=all");
  });

  it("shows both the total and the failed count for the same selection", async () => {
    renderPanel();
    await screen.findByText("Created task");
    expect(screen.getByText("audit.ops_total")).toBeTruthy();
    expect(screen.getByText("audit.ops_failed")).toBeTruthy();
    // 2 total rows / 1 failed row, rendered as the stat values.
    expect(screen.getAllByText("2").length).toBeGreaterThan(0);
    expect(screen.getAllByText("1").length).toBeGreaterThan(0);
  });

  it("exports the applied filters, not just the visible page", async () => {
    const blob = new Blob(["Time,User\n"], { type: "text/csv" });
    vi.mocked(api.downloadGet).mockResolvedValue({ blob, filename: "audit-operations.csv" });

    renderPanel();
    await screen.findByText("Created task");
    fireEvent.click(screen.getByRole("button", { name: /audit\.ops_export/ }));

    await waitFor(() => expect(api.downloadGet).toHaveBeenCalledTimes(1));
    const path = String(vi.mocked(api.downloadGet).mock.calls[0][0]);
    expect(path).toContain("/audit/logs/export?");
    expect(path).toContain("scope=operations");
    expect(path).toContain("since=7d");
    // The export must not be pinned to the visible page.
    expect(path).not.toContain("page=");
  });

  it("opens a detail dialog with the selected operation", async () => {
    renderPanel();
    const raw = await screen.findByText("task_create");
    fireEvent.click(raw);

    expect(await screen.findByText("audit.ops_detail_title")).toBeTruthy();
    expect(screen.getAllByText("Created task").length).toBeGreaterThan(0);
  });

  it("starts from the filters a deep link carries", async () => {
    renderPanel({
      initialResult: "failed",
      initialCategory: "tasks",
      initialAction: "task_create",
      initialRange: "24h",
    });
    await screen.findByText("Created task");

    const first = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
    const query = String(first?.[0]);
    expect(query).toContain("result=failed");
    expect(query).toContain("category=tasks");
    expect(query).toContain("action=task_create");
    expect(query).toContain("since=24h");
  });

  it("drops a filter value the server would reject", async () => {
    // A hand-written link can carry anything; an unknown result or category
    // would be answered with 400 and leave an empty table behind.
    renderPanel({
      initialResult: "bogus",
      initialCategory: "not-a-category",
      initialRange: "forever",
    });
    await screen.findByText("Created task");

    const first = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
    const query = String(first?.[0]);
    expect(query).not.toContain("result=");
    expect(query).not.toContain("category=");
    expect(query).toContain("since=7d");
  });

  it("counts the catalogue over the same window the table shows", async () => {
    // The chip counts must describe the rows behind them: a catalogue
    // counted over all time next to a seven-day table is a number the page
    // cannot honour.
    renderPanel({ initialRange: "24h" });
    await screen.findByText("Created task");

    const catalogue = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/actions"));
    expect(String(catalogue?.[0])).toContain("since=24h");

    const logs = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
    expect(String(logs?.[0])).toContain("since=24h");
  });

  it("links the target of an operation to its beacon", async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      const p = String(path);
      if (p.startsWith("/audit/actions")) return { actions: [], categories: [] };
      if (p.startsWith("/audit/logs")) {
        return {
          logs: [
            {
              id: 1,
              user: "alice",
              action: "task_create",
              resource: "task",
              agent_id: "11111111-2222-4333-8444-555555555555",
              success: true,
              created_at: "2026-11-10T10:00:00Z",
            },
          ],
          total: 1,
          failed: 0,
        };
      }
      return {};
    });

    renderPanel();
    // The cell shows the first 12 characters; the link carries the whole id.
    const link = await screen.findByRole("link", { name: /11111111/ });
    expect(link.getAttribute("href")).toBe("/beacons/11111111-2222-4333-8444-555555555555");
  });

  it("keeps the target plain for an operator who may not open beacons", async () => {
    can.mockReturnValue(false);
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      const p = String(path);
      if (p.startsWith("/audit/actions")) return { actions: [], categories: [] };
      if (p.startsWith("/audit/logs")) {
        return {
          logs: [
            {
              id: 1,
              user: "alice",
              action: "task_create",
              agent_id: "11111111-2222-4333-8444-555555555555",
              success: true,
              created_at: "2026-11-10T10:00:00Z",
            },
          ],
          total: 1,
          failed: 0,
        };
      }
      return {};
    });

    renderPanel();
    await screen.findByText("Created task");
    expect(screen.queryByRole("link", { name: /11111111/ })).toBeNull();
  });

  it("filters by a category from the catalogue chips", async () => {
    renderPanel();
    await screen.findByText("Created task");

    // The chips carry the counts the catalogue reported. The label falls back
    // to the raw id when the dictionary has no entry (as in this mock).
    const chip = await screen.findByRole("button", { name: /tasks · 3/ });
    expect(chip.getAttribute("aria-pressed")).toBe("false");

    fireEvent.click(chip);

    await waitFor(() => {
      const calls = vi.mocked(api.get).mock.calls.filter(([p]) => String(p).startsWith("/audit/logs"));
      expect(String(calls[calls.length - 1][0])).toContain("category=tasks");
    });
    expect(screen.getByRole("button", { name: /tasks · 3/ }).getAttribute("aria-pressed")).toBe("true");
  });

  it("pins the view to a beacon from a deep link and can clear it", async () => {
    renderPanel({ initialAgentId: "agent-abc" });
    await screen.findByText("Created task");

    const first = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
    expect(String(first?.[0])).toContain("agent_id=agent-abc");
    expect(screen.getByText("audit.ops_target_applied")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /audit\.ops_target_clear/ }));
    await waitFor(() => {
      const calls = vi.mocked(api.get).mock.calls.filter(([path]) => String(path).startsWith("/audit/logs"));
      const last = calls[calls.length - 1];
      expect(String(last?.[0])).not.toContain("agent_id=");
    });
  });
});
