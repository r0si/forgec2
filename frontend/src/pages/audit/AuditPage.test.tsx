import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import AuditPage from "@/pages/audit/AuditPage";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));
vi.mock("sonner", () => ({ toast: { error: vi.fn(), success: vi.fn() } }));

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    const p = String(path);
    if (p.startsWith("/audit/logins")) return { events: [], total: 0, summary: null };
    if (p.startsWith("/audit/logs")) return { logs: [], total: 0 };
    if (p.startsWith("/users")) return { users: [] };
    return {};
  });
});

describe("AuditPage tabs", () => {
  it("shows only one CSV export at a time, and it exports the visible table", async () => {
    render(<AuditPage />);

    // The events tab owns the header export.
    expect(await screen.findByRole("button", { name: /audit\.export_page_csv/ })).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: /audit\.tab_logins/ }));

    // The header export must disappear: otherwise it would export the events
    // table while the login log is the visible one.
    await screen.findByLabelText("audit.login_user_filter");
    const exportButtons = screen.getAllByRole("button", { name: /audit\.export_page_csv/ });
    expect(exportButtons).toHaveLength(1);
    // The one that remains belongs to the login log panel.
    expect(screen.getByRole("tabpanel").contains(exportButtons[0])).toBe(true);

    // Switching back restores the events export.
    fireEvent.click(screen.getByRole("tab", { name: /audit\.tab_events/ }));
    const back = await screen.findByRole("button", { name: /audit\.export_page_csv/ });
    expect(back.closest("[role=tabpanel]")).toBeNull();
  });
});
