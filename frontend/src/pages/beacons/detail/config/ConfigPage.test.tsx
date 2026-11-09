import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import AgentConfigPage from "./ConfigPage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  // `t` must be referentially stable: loadConfig carries it in its deps, and a
  // fresh function per render would re-run the load effect forever.
  const t = (key: string) => key;
  // Page modules register their dictionary fragment at module scope.
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// Route reuse: the real router keeps this page mounted across
// /beacons/A/config → /beacons/B/config, swapping only the param.
let currentAgentId = "a1";
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useParams: () => ({ id: currentAgentId }) };
});

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

const EFFECTIVE = {
  sleep: 10,
  jitter: 20,
  user_agent: "TestAgent/1.0",
  headers: null,
  beacon_uri: "/beacon",
  method: "POST",
};

function configResponse() {
  return { agent_id: currentAgentId, effective: { ...EFFECTIVE }, has_pending: false, pending_task_id: 0 };
}

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  const promise = new Promise<T>((res) => {
    resolve = res;
  });
  return { promise, resolve };
}

async function pushButton() {
  return screen.findByRole("button", { name: "agents.config_push" });
}

beforeEach(() => {
  vi.clearAllMocks();
  currentAgentId = "a1";
  vi.mocked(api.get).mockImplementation(() => Promise.resolve(configResponse()));
});

describe("AgentConfigPage", () => {
  it("renders the inline DataError and a normalized toast when the load fails", async () => {
    vi.mocked(api.get).mockRejectedValue({ status: 500, message: "config store offline" });
    render(<AgentConfigPage />);

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(screen.getByText("agents.config_load_failed")).toBeTruthy();
  });

  it("surfaces the backend text when the push is rejected and re-enables the button", async () => {
    render(<AgentConfigPage />);
    const push = await pushButton();

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "invalid beacon URI" });
    await act(async () => {
      fireEvent.click(push);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("invalid beacon URI"));
    expect((push as HTMLButtonElement).disabled).toBe(false);
  });

  it("pushes the edited form and reloads the config on success", async () => {
    render(<AgentConfigPage />);
    const push = await pushButton();

    vi.mocked(api.postJson).mockResolvedValue({ success: true });
    await act(async () => {
      fireEvent.click(push);
    });

    await waitFor(() => expect(successToast).toHaveBeenCalledWith("agents.config_push_success"));
    expect(vi.mocked(api.postJson)).toHaveBeenCalledWith(
      paths.agents.config("a1"),
      expect.objectContaining({ sleep: 10, jitter: 20, user_agent: "TestAgent/1.0" }),
    );
    // The success path re-fetches the effective config.
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("drops an in-flight push when the route switches agents", async () => {
    const pending = deferred<Record<string, unknown>>();
    const view = render(<AgentConfigPage />);
    const push = await pushButton();

    vi.mocked(api.postJson).mockImplementation(() => pending.promise);
    await act(async () => {
      fireEvent.click(push);
    });

    currentAgentId = "b2";
    view.rerender(<AgentConfigPage />);
    await act(async () => {
      pending.resolve({ success: true });
    });

    expect(successToast).not.toHaveBeenCalled();
  });
});
