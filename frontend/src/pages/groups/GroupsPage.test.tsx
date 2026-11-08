import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import GroupsPage from "@/pages/groups/GroupsPage";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const withCreator = {
  id: "g1",
  name: "DMZ",
  description: "",
  color: "red",
  parent_id: "",
  agent_count: 0,
  child_count: 0,
  created_by: "alice",
  created_at: "2026-10-01T00:00:00Z",
  updated_at: "2026-10-01T00:00:00Z",
};

const withoutCreator = {
  id: "g2",
  name: "Legacy",
  description: "",
  color: "red",
  parent_id: "",
  agent_count: 0,
  child_count: 0,
  created_at: "2026-09-01T00:00:00Z",
  updated_at: "2026-09-01T00:00:00Z",
};

describe("GroupsPage creator attribution", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ groups: [withCreator, withoutCreator] });
  });

  it("shows the creating operator when the group has one", async () => {
    render(<GroupsPage />);
    expect(await screen.findByText(/groups\.created_by alice/)).toBeTruthy();
    expect(screen.getByText("DMZ")).toBeTruthy();
  });

  it("stays silent when the group has no creator (historic rows)", async () => {
    vi.mocked(api.get).mockResolvedValue({ groups: [withoutCreator] });
    render(<GroupsPage />);
    await screen.findByText("Legacy");
    expect(screen.queryByText(/groups\.created_by/)).toBeNull();
  });
});
