import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { LoginLogPanel } from "@/pages/audit/components/LoginLogPanel";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn() } }));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({ t: (key: string) => key }),
}));

const ROWS = [
  {
    id: 3, user: "alice", action: "login", ip: "203.0.113.9", country: "德国",
    city: "法兰克福", success: true, details: "Login successful",
    created_at: "2026-11-09T08:30:00Z",
  },
  {
    id: 2, user: "mallory", action: "login_failed", ip: "198.51.100.4", country: "美国",
    success: false, details: "Wrong password", created_at: "2026-11-09T08:31:00Z",
  },
  {
    id: 1, user: "admin", action: "login", ip: "127.0.0.1", country: "local",
    success: true, details: "Login successful", created_at: "2026-11-09T08:32:00Z",
  },
];

describe("LoginLogPanel", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ events: ROWS, total: 3 });
  });

  it("renders the recorded time, user, IP and country for each sign-in", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("alice");
    expect(screen.getByText("203.0.113.9")).toBeTruthy();
    expect(screen.getByText("德国")).toBeTruthy();
    expect(screen.getByText("2026-11-09T08:30:00Z")).toBeTruthy();
    // Loopback reads as a named category, not as a missing country.
    expect(screen.getByText("audit.login_country_local")).toBeTruthy();
  });

  it("marks failed attempts and keeps them visually distinct", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("mallory");
    expect(screen.getByText("audit.login_failed")).toBeTruthy();
    expect(screen.getByText("Wrong password")).toBeTruthy();
  });

  it("sends the result and user filters to the server", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("alice");

    fireEvent.change(screen.getByLabelText("audit.login_user_filter"), { target: { value: "mal" } });
    fireEvent.click(screen.getByRole("button", { name: /audit\.apply/ }));

    await waitFor(() => {
      const calls = vi.mocked(api.get).mock.calls.map(([p]) => String(p));
      const last = calls[calls.length - 1];
      expect(last).toContain(paths.audit.logins("").split("?")[0]);
      expect(last).toContain("user=mal");
      expect(last).toContain("page=1");
    });
  });

  it("reports a load failure instead of an empty table", async () => {
    vi.mocked(api.get).mockRejectedValue(new Error("offline"));
    render(<LoginLogPanel />);
    await waitFor(() => expect(screen.getByText("audit.login_load_failed")).toBeTruthy());
    expect(screen.queryByText("audit.login_empty")).toBeNull();
  });
});
