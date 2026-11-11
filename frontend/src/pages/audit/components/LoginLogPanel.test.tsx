import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { LoginLogPanel } from "@/pages/audit/components/LoginLogPanel";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { downloadBlob } from "@/lib/download";

vi.mock("@/lib/api", () => ({ api: { get: vi.fn(), downloadGet: vi.fn() } }));
vi.mock("@/lib/download", () => ({ downloadBlob: vi.fn() }));

vi.mock("@/lib/i18n", () => ({
  useI18n: () => ({
    t: (key: string) =>
      key === "audit.login_region_local" ? "本地 / 内网" : key,
  }),
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
  const SUMMARY = {
    window_hours: 24,
    failed: 2,
    ok: 7,
    distinct_ips: 3,
    new_ip: 1,
    by_region: [
      { country: "美国", failed: 2, ok: 0 },
      { country: "中国", city: "北京", failed: 0, ok: 5 },
      { country: "local", failed: 0, ok: 2 },
      { country: "", failed: 0, ok: 1 },
    ],
  };

  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ events: ROWS, total: 3, summary: SUMMARY });
  });

  it("summarises the last 24 hours by country", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("audit.login_summary_title");
    expect(screen.getByText("2")).toBeTruthy();
    expect(screen.getByText("7")).toBeTruthy();
    // The failed region is listed with its failed/ok split; a known city is
    // part of the region label, not a tooltip.
    expect(screen.getByText("美国 · 2/0")).toBeTruthy();
    expect(screen.getByText("中国 · 北京 · 0/5")).toBeTruthy();
    expect(screen.getByText("本地 / 内网 · 0/2")).toBeTruthy();
    // An unresolved IP is named, not blank: it fills in on the next refresh.
    expect(screen.getByText("audit.login_region_unknown · 0/1")).toBeTruthy();
    // The anomalous-login signal rides along: sign-ins from a new address.
    expect(screen.getByText("audit.login_new_ip")).toBeTruthy();
  });

  it("renders the recorded time, user, IP and region (country · city) per sign-in", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("alice");
    expect(screen.getByText("203.0.113.9")).toBeTruthy();
    // Country and city travel together in the Region column.
    expect(screen.getByText("德国 · 法兰克福")).toBeTruthy();
    expect(screen.getByText("2026-11-09T08:30:00Z")).toBeTruthy();
    // Loopback reads as a named category, not as a missing region.
    expect(screen.getAllByText("本地 / 内网").length).toBeGreaterThan(0);
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

  it("exports the full filtered selection, not just the visible page", async () => {
    const blob = new Blob(["Time,User\n"], { type: "text/csv" });
    vi.mocked(api.downloadGet).mockResolvedValue({ blob, filename: "login-log-20261110-1900.csv" });

    render(<LoginLogPanel />);
    await screen.findByText("alice");
    fireEvent.click(screen.getByRole("button", { name: /audit\.login_export/ }));

    await waitFor(() => expect(api.downloadGet).toHaveBeenCalledTimes(1));
    const path = String(vi.mocked(api.downloadGet).mock.calls[0][0]);
    expect(path).toContain("/audit/logins/export?");
    expect(path).not.toContain("page=");
    expect(vi.mocked(downloadBlob)).toHaveBeenCalledTimes(1);
  });

  it("offers a time range and keeps all-time as the default", async () => {
    render(<LoginLogPanel />);
    await screen.findByText("alice");
    expect(screen.getByLabelText("audit.login_range")).toBeTruthy();
    const call = vi.mocked(api.get).mock.calls.find(([p]) => String(p).startsWith("/audit/logins"));
    expect(String(call?.[0])).not.toContain("since=");
  });
});
