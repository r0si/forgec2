import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import ToolkitPage from "./ToolkitPage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: vi.fn(),
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

vi.mock("@/lib/hooks/usePermissions", () => ({
  usePermissions: () => ({ can: () => true }),
}));

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

async function chooseAgent() {
  // The agent list rides the page's resource; wait for it to settle first.
  await screen.findByText("toolkit.no_results");
  fireEvent.click(screen.getByRole("combobox"));
  const option = await screen.findByRole("option", { name: /h1/ });
  // base-ui commits the item on the pointer pair; a bare click is ignored.
  fireEvent.pointerDown(option);
  fireEvent.pointerUp(option);
  fireEvent.click(option);
  // Flush base-ui's post-focus timeout so its state update lands inside act.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

async function switchToTemplatesTab() {
  const tab = screen.getByRole("tab", { name: "toolkit.tab_templates" });
  fireEvent.mouseDown(tab);
  fireEvent.click(tab);
  // Let the panel mount and finish its initial load inside act.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  confirmMock.mockResolvedValue(true);
  vi.mocked(fetchAgentListCached).mockResolvedValue([{ id: "a1", hostname: "h1", ip: "1.2.3.4" }]);
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (String(path).includes("templates")) return Promise.resolve({ templates: [] });
    if (String(path).includes("/toolkit/agents/")) return Promise.resolve({});
    return Promise.resolve({ tasks: [] });
  });
});

describe("ToolkitPage", () => {
  it("keeps the no-agent guard a direct toast", async () => {
    render(<ToolkitPage />);

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "whoami" }));
    });

    expect(errorToast).toHaveBeenCalledWith("toolkit.toast.select_agent_first");
    expect(api.post).not.toHaveBeenCalled();
  });

  it("surfaces the backend reason when a quick action is rejected", async () => {
    render(<ToolkitPage />);
    await chooseAgent();

    vi.mocked(api.post).mockRejectedValue({ status: 400, message: "agent offline" });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "whoami" }));
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("agent offline"));
    expect(api.post).toHaveBeenCalledWith(paths.toolkit.action("a1"), { action: "whoami", param: "" });
  });

  it("dispatches a quick action and refreshes the recent results", async () => {
    render(<ToolkitPage />);
    await chooseAgent();

    vi.mocked(api.post).mockResolvedValue({ success: true, task_id: 9 });
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "whoami" }));
    });

    await waitFor(() => expect(successToast).toHaveBeenCalledWith("toolkit.toast.action_dispatched"));
    expect(vi.mocked(api.get).mock.calls.filter(([p]) => p === paths.toolkit.results).length).toBeGreaterThanOrEqual(2);
  });

  it("keeps the success-less envelope branch: no success key reads as a failure", async () => {
    render(<ToolkitPage />);
    await chooseAgent();

    vi.mocked(api.post).mockResolvedValue({});
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "whoami" }));
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("toolkit.toast.action_failed"));
    expect(successToast).not.toHaveBeenCalled();
  });

  it("normalizes a failed template run dispatched through the parent hook", async () => {
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (String(path).includes("templates")) {
        return Promise.resolve({ templates: [{ id: 1, name: "recon-1", category: "recon", command: "whoami" }] });
      }
      if (String(path).includes("/toolkit/agents/")) return Promise.resolve({});
      return Promise.resolve({ tasks: [] });
    });
    render(<ToolkitPage />);
    await chooseAgent();
    await switchToTemplatesTab();

    vi.mocked(api.postJson).mockRejectedValue({ status: 500, message: "boom" });
    const runButton = await screen.findByRole("button", { name: "templates.run" });
    await act(async () => {
      fireEvent.click(runButton);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(api.postJson).toHaveBeenCalledWith(paths.agents.command("a1"), { command: "whoami" });
  });
});
