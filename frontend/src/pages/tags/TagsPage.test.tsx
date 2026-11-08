import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import TagsPage from "@/pages/tags/TagsPage";
import { api } from "@/lib/api";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t: (key: string) => key }),
}));

vi.mock("@/lib/hooks/usePermissions", () => ({
  usePermissions: () => ({ can: () => false }),
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));

const withCreator = {
  id: "t1",
  name: "critical",
  color: "red",
  agent_count: 0,
  created_at: "2026-10-01T00:00:00Z",
  created_by: "alice",
  updated_at: "2026-10-01T00:00:00Z",
};

const withoutCreator = {
  id: "t2",
  name: "legacy",
  color: "red",
  agent_count: 0,
  created_at: "2026-09-01T00:00:00Z",
  updated_at: "2026-09-01T00:00:00Z",
};

describe("TagsPage creator attribution", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.mocked(api.get).mockResolvedValue({ tags: [withCreator] });
  });

  it("shows the creating operator when the tag has one", async () => {
    render(<TagsPage />);
    expect(await screen.findByText(/groups\.created_by alice/)).toBeTruthy();
    expect(screen.getByText("critical")).toBeTruthy();
  });

  it("stays silent when the tag has no creator (historic rows)", async () => {
    vi.mocked(api.get).mockResolvedValue({ tags: [withoutCreator] });
    render(<TagsPage />);
    await screen.findByText("legacy");
    expect(screen.queryByText(/groups\.created_by/)).toBeNull();
  });
});
