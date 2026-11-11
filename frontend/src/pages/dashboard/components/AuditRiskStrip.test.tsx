import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { AuditRiskStrip } from "./AuditRiskStrip";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }));
const can = vi.fn(() => true);
vi.mock("@/lib/hooks/usePermissions", () => ({ usePermissions: () => ({ can }) }));
vi.mock("@/lib/i18n", () => ({ useI18n: () => ({ t: (k: string) => k }) }));

beforeEach(() => {
  vi.clearAllMocks();
  can.mockReturnValue(true);
  vi.mocked(api.get).mockImplementation(async (path: string) => {
    if (String(path).startsWith("/audit/logs")) {
      return {
        failed: 18,
        total: 18,
        logs: [
          {
            id: "9",
            user: "demo",
            action: "command_result",
            success: false,
            created_at: "2026-11-10T09:00:00Z",
            details: "whoami /priv failed",
          },
        ],
      };
    }
    if (String(path).startsWith("/audit/logins")) {
      return {
        summary: {
          failed: 4,
          ok: 20,
          distinct_ips: 3,
          new_ip: 2,
          by_region: [
            { country: "美国", city: "佛罗里达", failed: 2, ok: 0 },
            { country: "中国", city: "北京", failed: 0, ok: 10 },
          ],
        },
      };
    }
    return {};
  });
});

function renderStrip() {
  return render(
    <MemoryRouter>
      <AuditRiskStrip />
    </MemoryRouter>,
  );
}

describe("AuditRiskStrip", () => {
  it("shows failed operations, failed sign-ins, source IPs and failed regions", async () => {
    renderStrip();
    expect(await screen.findByText("dashboard.audit_failed_ops")).toBeTruthy();
    expect(screen.getByText("18")).toBeTruthy();
    expect(screen.getByText("4")).toBeTruthy();
    expect(screen.getByText("3")).toBeTruthy();
    expect(screen.getByText("dashboard.audit_new_ip")).toBeTruthy();
    // Only failed regions are listed; a region with zero failures is noise.
    expect(screen.getByText("美国 · 佛罗里达 · 2")).toBeTruthy();
    expect(screen.queryByText("中国 · 北京 · 0")).toBeNull();
    expect(screen.getByText("dashboard.audit_open")).toBeTruthy();
  });

  it("links every tile to the matching selection in the audit log", async () => {
    renderStrip();
    await screen.findByText("dashboard.audit_failed_ops");

    const hrefs = screen.getAllByRole("link").map((l) => l.getAttribute("href"));
    // The count is a question; the link is the answer, in the same 24h
    // window the tile counts.
    expect(hrefs).toContain("/audit?tab=operations&result=failed&range=24h");
    expect(hrefs).toContain("/audit?tab=logins&result=failed&range=24h");
    expect(hrefs).toContain("/audit?tab=logins&range=24h");
    expect(hrefs).toContain("/audit?tab=operations&action=login_new_ip&range=24h");
  });

  it("lists the operations that actually failed, not just their number", async () => {
    renderStrip();
    expect(await screen.findByText("dashboard.audit_failed_list_title")).toBeTruthy();
    // The action label falls back to a humanized name without a dictionary entry.
    expect(screen.getByText("Command result")).toBeTruthy();
    expect(screen.getByText("demo · whoami /priv failed")).toBeTruthy();
  });

  it("renders nothing without audit.read", () => {
    can.mockReturnValue(false);
    renderStrip();
    expect(screen.queryByText("dashboard.audit_risk_title")).toBeNull();
  });

  it("reports an unavailable summary instead of breaking the dashboard", async () => {
    vi.mocked(api.get).mockRejectedValue(new Error("offline"));
    renderStrip();
    expect(await screen.findByText("dashboard.audit_unavailable")).toBeTruthy();
  });
});
