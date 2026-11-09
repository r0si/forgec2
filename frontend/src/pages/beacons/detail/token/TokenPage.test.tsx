import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import AgentTokenPage from "./TokenPage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  // `t` must be referentially stable: the mutations' callbacks carry it.
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

// Route reuse: the real router keeps this page mounted across
// /beacons/A/token → /beacons/B/token, swapping only the param.
let currentAgentId = "a1";
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useParams: () => ({ id: currentAgentId }) };
});

const errorToast = vi.mocked(toast.error);
const infoToast = vi.mocked(toast.info);

const PROCESSES = [{ pid: 1234, name: "lsass.exe" }];

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

async function chooseProcess() {
  fireEvent.mouseDown(screen.getByRole("combobox"), { button: 0 });
  const option = await screen.findByRole("option", { name: /lsass/ });
  fireEvent.click(option);
  // Flush base-ui's post-focus timeout so its state update lands inside act.
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

function stealButton() {
  return screen.getByRole("button", { name: "agents.token_steal" });
}

beforeEach(() => {
  vi.clearAllMocks();
  currentAgentId = "a1";
  confirmMock.mockResolvedValue(true);
  vi.mocked(api.get).mockResolvedValue([{ id: 1, username: "alice", domain: "CORP", active: true }]);
  vi.mocked(api.post).mockImplementation((path: string) => {
    if (String(path) === paths.agents.tokenListProcs(currentAgentId)) return Promise.resolve({ processes: PROCESSES });
    return Promise.resolve({ success: true });
  });
});

describe("AgentTokenPage", () => {
  it("surfaces the backend text when a token steal is rejected", async () => {
    render(<AgentTokenPage />);
    await screen.findByRole("combobox");
    await chooseProcess();

    vi.mocked(api.post).mockImplementation((path: string) => {
      if (String(path) === paths.agents.tokenSteal("a1")) {
        return Promise.reject({ status: 400, message: "token tampering denied" });
      }
      return Promise.resolve({ success: true });
    });

    await act(async () => {
      fireEvent.click(stealButton());
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("token tampering denied"));
    // The PID selection survives the failure so the operator can retry.
    expect((stealButton() as HTMLButtonElement).disabled).toBe(false);
  });

  it("maps a 500 from whoami and marks the context banner as an error", async () => {
    render(<AgentTokenPage />);
    await screen.findByRole("combobox");

    vi.mocked(api.post).mockImplementation((path: string) => {
      if (String(path) === paths.agents.tokenListProcs("a1")) return Promise.resolve({ processes: PROCESSES });
      if (String(path) === paths.agents.tokenWhoami("a1")) {
        return Promise.reject({ status: 500, message: "identity query exploded" });
      }
      return Promise.resolve({ success: true });
    });

    const whoamiButtons = screen.getAllByRole("button", { name: "agents.token_whoami" });
    await act(async () => {
      fireEvent.click(whoamiButtons[0]);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    // The inline banner shows the failure (raw detail) in error styling.
    const banner = (await screen.findByText(/identity query exploded/)).closest("div");
    expect(banner?.className).toContain("text-destructive-ink");
  });

  it("routes an unmapped rejection through the rate-limit mapping", async () => {
    render(<AgentTokenPage />);
    const user = await screen.findByLabelText(/agents\.token_username/);
    fireEvent.change(user, { target: { value: "svc_backup" } });

    vi.mocked(api.post).mockImplementation((path: string) => {
      if (String(path) === paths.agents.tokenListProcs("a1")) return Promise.resolve({ processes: PROCESSES });
      if (String(path) === paths.agents.tokenMake("a1")) {
        return Promise.reject({ status: 429, message: "slow down" });
      }
      return Promise.resolve({ success: true });
    });

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "agents.token_make" }));
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.rate_limited"));
  });

  it("keeps the retry banner when the token list read fails", async () => {
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (String(path) === paths.agents.tokenList("a1")) {
        return Promise.reject({ status: 500, message: "token read exploded" });
      }
      return Promise.resolve([]);
    });
    render(<AgentTokenPage />);
    await screen.findByRole("combobox");

    // The unreadable banner replaces the "no tokens harvested yet" claim.
    expect(await screen.findByText("agents.token_load_failed")).toBeTruthy();
  });

  it("clears drafts and drops an in-flight steal when the route switches agents", async () => {
    const view = render(<AgentTokenPage />);
    await screen.findByRole("combobox");
    await chooseProcess();

    const pending = deferred<Record<string, unknown>>();
    vi.mocked(api.post).mockImplementation((path: string) => {
      if (String(path) === paths.agents.tokenSteal("a1")) return pending.promise;
      return Promise.resolve({ success: true });
    });

    await act(async () => {
      fireEvent.click(stealButton());
    });

    currentAgentId = "b2";
    view.rerender(<AgentTokenPage />);
    await act(async () => {
      pending.resolve({ success: true });
    });

    // The stale completion must not report a queued task for the new agent…
    expect(infoToast).not.toHaveBeenCalledWith("agents.token_task_queued");
    // …and the PID draft from the old agent must be gone (submit disabled).
    await waitFor(() => expect((stealButton() as HTMLButtonElement).disabled).toBe(true));
  });
});
