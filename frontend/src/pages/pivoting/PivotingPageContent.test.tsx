import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import PivotingPageContent from "./PivotingPageContent";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string) => key;
  // Page modules register their dictionary fragment at module scope.
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));

const confirmMock = vi.hoisted(() => vi.fn(async () => true));
vi.mock("@/lib/hooks/useConfirm", () => ({
  useConfirm: () => ({ confirm: confirmMock, modal: null }),
}));

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

async function openLocalTab() {
  const tab = await screen.findByRole("tab", { name: "pivoting.tab_local" });
  fireEvent.mouseDown(tab);
  fireEvent.click(tab);
  return screen.findByRole("button", { name: "pivoting.start_socks_on" });
}

beforeEach(() => {
  vi.clearAllMocks();
  confirmMock.mockResolvedValue(true);
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (String(path).includes("agents")) {
      return Promise.resolve({ agents: [{ id: "a1", hostname: "h1", ip: "1.2.3.4" }] });
    }
    return Promise.resolve({});
  });
});

describe("PivotingPageContent direct SOCKS", () => {
  it("surfaces the backend reason when a start is rejected and clears the row busy state", async () => {
    render(<PivotingPageContent />);
    const start = await openLocalTab();

    vi.mocked(api.post).mockRejectedValue({ status: 400, message: "port busy" });
    await act(async () => {
      fireEvent.click(start);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("port busy"));
    expect(api.post).toHaveBeenCalledWith(paths.agents.socks("a1"), { port: "1080" });
    expect((start as HTMLButtonElement).disabled).toBe(false);
  });

  it("reports a started direct SOCKS with the host and port", async () => {
    render(<PivotingPageContent />);
    const start = await openLocalTab();

    vi.mocked(api.post).mockResolvedValue({});
    await act(async () => {
      fireEvent.click(start);
    });

    await waitFor(() => expect(successToast).toHaveBeenCalledWith("pivoting.toast.direct_socks"));
    expect(errorToast).not.toHaveBeenCalled();
  });
});
