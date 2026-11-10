import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { fetchAgentListCached } from "@/lib/agents";
import NtlmPage from "./NtlmPage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: vi.fn(),
}));

vi.mock("@/lib/i18n", () => {
  // Must be referentially stable: the mutations' callbacks carry it.
  const t = (key: string) => key;
  // Page modules register their dictionary fragment at module scope.
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn(), info: vi.fn() }),
}));

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

async function chooseAgent() {
  fireEvent.click(screen.getAllByRole("combobox")[0]);
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

function coerceButton() {
  return screen.getByRole("button", { name: "ntlm.execute_coercion" }) as HTMLButtonElement;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(fetchAgentListCached).mockResolvedValue([{ id: "a1", hostname: "h1", ip: "1.2.3.4" }]);
  vi.mocked(api.get).mockResolvedValue({ active: [], count: 0, running: false });
});

describe("NtlmPage", () => {
  it("surfaces the backend reason when a coercion is rejected and clears pending", async () => {
    render(<NtlmPage />);
    await chooseAgent();
    fireEvent.change(screen.getByPlaceholderText("ntlm.target_ph"), { target: { value: "10.0.0.5" } });

    vi.mocked(api.post).mockRejectedValue({ status: 400, message: "agent offline" });
    await act(async () => {
      fireEvent.click(coerceButton());
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("agent offline"));
    expect(coerceButton().disabled).toBe(false);
  });

  it("localizes a server failure instead of leaking the raw status", async () => {
    render(<NtlmPage />);
    await chooseAgent();
    fireEvent.change(screen.getByPlaceholderText("ntlm.target_ph"), { target: { value: "10.0.0.5" } });

    vi.mocked(api.post).mockRejectedValue({ status: 500, message: "boom" });
    await act(async () => {
      fireEvent.click(coerceButton());
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
  });

  it("dispatches a coercion with the form body and reports the task id", async () => {
    render(<NtlmPage />);
    await chooseAgent();
    fireEvent.change(screen.getByPlaceholderText("ntlm.target_ph"), { target: { value: "10.0.0.5" } });

    vi.mocked(api.post).mockResolvedValue({ success: true, task_id: 42 });
    await act(async () => {
      fireEvent.click(coerceButton());
    });

    await waitFor(() => expect(successToast).toHaveBeenCalledWith("ntlm.toast.coerce_dispatched"));
    expect(api.post).toHaveBeenCalledWith(paths.agents.coerce("a1", "printerbug"), {
      target: "10.0.0.5",
      listen_addr: "0.0.0.0:8080",
    });
  });

  it("keeps the action buttons disabled until an agent and target are chosen", async () => {
    render(<NtlmPage />);
    await chooseAgent();

    // Guard clauses in the handlers stay direct toasts; the disabled state is
    // what the operator actually hits, so pin it.
    expect(coerceButton().disabled).toBe(true);
    expect(api.post).not.toHaveBeenCalled();
  });
});
