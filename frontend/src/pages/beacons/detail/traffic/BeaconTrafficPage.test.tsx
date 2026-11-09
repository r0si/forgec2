import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import AgentTrafficPage from "./BeaconTrafficPage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), postJson: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  // `t` must be referentially stable: the mutations' callbacks carry it.
  const templates: Record<string, string> = {
    "agents.traffic_adapt_queued": "Adaptation task queued: {message}",
  };
  const t = (key: string, params?: Record<string, string | number>) => {
    let s = templates[key] ?? key;
    if (params && typeof params === "object") {
      for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, String(v));
    }
    return s;
  };
  // Page modules register their dictionary fragment at module scope.
  return { getLocale: () => "en", useI18n: () => ({ t }), registerFragment: () => {} };
});

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), { success: vi.fn(), error: vi.fn() }),
}));

let currentAgentId = "a1";
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useParams: () => ({ id: currentAgentId }) };
});

// jsdom does not implement PointerEvent, but base-ui's checkbox click path
// constructs one; MouseEvent is the closest available constructor.
if (typeof window.PointerEvent === "undefined") {
  Object.defineProperty(window, "PointerEvent", {
    value: window.MouseEvent,
    configurable: true,
    writable: true,
  });
}

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

function report() {
  return {
    agent_id: currentAgentId,
    sample_count: 12,
    baseline_interval: 10,
    baseline_jitter: 2,
    baseline_packet_size: 100,
    mean_interval: 10,
    stddev_interval: 1,
    mean_packet_size: 100,
    cv: 0.1,
    auto_adapt: false,
    recent_records: [],
    suggestion: {
      desired_interval: 12,
      desired_jitter: 3,
      pad_size: 0,
      reason: "low coefficient of variation",
      confidence: "high",
    },
  };
}

async function adaptButton() {
  return screen.findByRole("button", { name: "agents.traffic_adapt" });
}

beforeEach(() => {
  vi.clearAllMocks();
  currentAgentId = "a1";
  vi.mocked(api.get).mockImplementation(() => Promise.resolve(report()));
});

describe("AgentTrafficPage", () => {
  it("shows the full-page ErrorState without a duplicate toast when the load fails", async () => {
    vi.mocked(api.get).mockRejectedValue({ status: 500, message: "traffic store offline" });
    render(<AgentTrafficPage />);

    await waitFor(() => expect(screen.getByText("agents.traffic_load_failed")).toBeTruthy());
    expect(errorToast).not.toHaveBeenCalled();
  });

  it("surfaces the backend text when adaptation is rejected and re-enables the button", async () => {
    render(<AgentTrafficPage />);
    const adapt = await adaptButton();

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "agent is offline" });
    await act(async () => {
      fireEvent.click(adapt);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("agent is offline"));
    expect((adapt as HTMLButtonElement).disabled).toBe(false);
  });

  it("queues adaptation with the server message and refreshes the report", async () => {
    render(<AgentTrafficPage />);
    const adapt = await adaptButton();

    vi.mocked(api.postJson).mockResolvedValue({ success: true, message: "task 7" });
    await act(async () => {
      fireEvent.click(adapt);
    });

    await waitFor(() => expect(successToast).toHaveBeenCalledWith("Adaptation task queued: task 7"));
    expect(vi.mocked(api.postJson)).toHaveBeenCalledWith(paths.agents.trafficAdapt("a1"), {});
    await waitFor(() => expect(vi.mocked(api.get).mock.calls.length).toBeGreaterThanOrEqual(2));
  });

  it("normalizes a rejected auto-adapt toggle and keeps the checkbox unchecked", async () => {
    render(<AgentTrafficPage />);
    const checkbox = await screen.findByRole("checkbox");
    expect(checkbox.getAttribute("aria-checked")).toBe("false");

    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "auto-adapt unsupported" });
    await act(async () => {
      fireEvent.click(checkbox);
    });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("auto-adapt unsupported"));
    expect(vi.mocked(api.postJson)).toHaveBeenCalledWith(paths.agents.trafficAutoAdapt("a1"), { enabled: true });
    expect(screen.getByRole("checkbox").getAttribute("aria-checked")).toBe("false");
  });

  it("clears the previous agent's report while the new one loads", async () => {
    const view = render(<AgentTrafficPage />);
    await waitFor(() => expect(screen.getByText("low coefficient of variation")).toBeTruthy());

    currentAgentId = "b2";
    vi.mocked(api.get).mockImplementation(() => new Promise(() => {}));
    view.rerender(<AgentTrafficPage />);

    // The stale report must not stay on screen under the new agent's id.
    expect(screen.queryByText("low coefficient of variation")).toBeNull();
  });
});
