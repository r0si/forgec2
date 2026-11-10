import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import AuditPage from "@/pages/audit/AuditPage";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), downloadGet: vi.fn() } }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    const p = String(path);
    if (p.startsWith("/audit/logins")) return { events: [], total: 0, summary: null };
    if (p.startsWith("/audit/actions")) return { actions: [], categories: [] };
    if (p.startsWith("/audit/verify")) return { ok: true, checked: 42, first_bad_id: 0, reason: "" };
    if (p.startsWith("/audit/logs")) return { logs: [], total: 0, failed: 0 };
    if (p.startsWith("/users")) return { users: [] };
    return {};
  });
});

function renderPage(entry = "/audit") {
  return render(
    <MemoryRouter initialEntries={[entry]}>
      <AuditPage />
    </MemoryRouter>,
  );
}

describe("AuditPage tabs", () => {
  it("shows only one CSV export at a time, and it exports the visible table", async () => {
    renderPage();

    // The events tab owns the header export.
    expect(await screen.findByRole("button", { name: /audit\.export_page_csv/ })).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: /audit\.tab_logins/ }));

    // The header export must disappear: otherwise it would export the events
    // table while the login log is the visible one.
    await screen.findByLabelText("audit.login_user_filter");
    expect(screen.queryAllByRole("button", { name: /audit\.export_page_csv/ })).toHaveLength(0);
    // The login panel owns its own full-selection export.
    const loginExport = screen.getByRole("button", { name: /audit\.login_export/ });
    expect(screen.getByRole("tabpanel").contains(loginExport)).toBe(true);

    // Switching back restores the events export.
    fireEvent.click(screen.getByRole("tab", { name: /audit\.tab_events/ }));
    const back = await screen.findByRole("button", { name: /audit\.export_page_csv/ });
    expect(back.closest("[role=tabpanel]")).toBeNull();
  });

  it("opens the operations tab pinned to a beacon from a deep link", async () => {
    renderPage("/audit?tab=operations&agent_id=agent-abc");

    const tab = screen.getByRole("tab", { name: /audit\.tab_operations/ });
    expect(tab.getAttribute("aria-selected")).toBe("true");

    await waitFor(() => {
      const call = vi.mocked(api.get).mock.calls.find(([path]) => String(path).startsWith("/audit/logs"));
      expect(call).toBeTruthy();
      expect(String(call?.[0])).toContain("agent_id=agent-abc");
    });
  });

  it("renders a readable action label with the raw code", async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      const p = String(path);
      if (p.startsWith("/users")) return { users: [] };
      if (p.startsWith("/audit/logs")) {
        return {
          logs: [{ id: 1, user: "alice", action: "user_create", success: true, created_at: "2026-11-10T10:00:00Z" }],
          total: 1,
          failed: 0,
        };
      }
      return {};
    });

    renderPage();
    expect(await screen.findByText("User create")).toBeTruthy();
    expect(screen.getByText("user_create")).toBeTruthy();
  });

  it("verifies the newest audit rows on demand", async () => {
    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /audit\.verify_chain/ }));

    expect(await screen.findByText("audit.verify_ok")).toBeTruthy();
    const call = vi.mocked(api.get).mock.calls.find(([p]) => String(p).startsWith("/audit/verify"));
    expect(String(call?.[0])).toContain("limit=5000");
  });

  it("shows the first broken row when verification fails", async () => {
    vi.mocked(api.get).mockImplementation(async (path: string) => {
      const p = String(path);
      if (p.startsWith("/audit/verify")) {
        return { ok: false, checked: 100, first_bad_id: 7, reason: "hash mismatch" };
      }
      if (p.startsWith("/audit/logs")) return { logs: [], total: 0, failed: 0 };
      if (p.startsWith("/users")) return { users: [] };
      return {};
    });

    renderPage();
    fireEvent.click(await screen.findByRole("button", { name: /audit\.verify_chain/ }));
    expect(await screen.findByText("audit.verify_failed")).toBeTruthy();
  });
});
