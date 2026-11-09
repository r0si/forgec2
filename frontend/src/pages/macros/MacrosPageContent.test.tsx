import { describe, it, expect, vi, beforeEach } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import MacrosPageContent from "./MacrosPageContent";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn(), postFormData: vi.fn() },
  getRateLimitRetryAfter: vi.fn(() => null),
}));

vi.mock("@/lib/i18n", () => {
  const t = (key: string, params?: Record<string, unknown>) =>
    params && typeof params.message === "string" ? `${key}|${params.message}` : key;
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

const wsMock = vi.hoisted(() => ({ subscribe: vi.fn(() => () => {}), connected: false }));
vi.mock("@/lib/wsContext", () => ({ useWS: () => wsMock }));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: vi.fn(async () => [
    { id: "agent-1", hostname: "WINBOX", ip: "10.0.0.5", status: "online" },
  ]),
}));

const aiStatusMock = vi.hoisted(() => ({
  status: { enabled: true, hasApiKey: true, known: true },
  reload: vi.fn(),
}));
vi.mock("@/lib/hooks/useAIStatus", () => ({ useAIStatus: () => aiStatusMock }));

const getMock = vi.mocked(api.get);
const postJsonMock = vi.mocked(api.postJson);
const putJsonMock = vi.mocked(api.putJson);
const delMock = vi.mocked(api.del);
const errorToast = vi.mocked(toast.error);

const MACRO = {
  id: 1,
  name: "Recon Sweep",
  description: "collect host info",
  steps: JSON.stringify([{ command: "whoami", delay_ms: 0, wait: false, timeout_s: 60 }]),
  created_by: "admin",
};

const RUN = {
  id: 9,
  macro_id: 1,
  macro_name: "Recon Sweep",
  agent_id: "agent-1",
  status: "running",
  current_step: 1,
  total_steps: 2,
  log: "[]",
  started_at: "2026-10-01T10:00:00.000Z",
};

function mockMountReads() {
  getMock.mockImplementation(async (path: unknown) => {
    const p = String(path);
    if (p === paths.macros.list) return { macros: [MACRO] };
    if (p === paths.macros.runs()) return { runs: [RUN] };
    return {};
  });
}

function serverError(message = "boom") {
  return Object.assign(new Error(message), { status: 500 });
}

async function renderPage() {
  render(
    <MemoryRouter>
      <MacrosPageContent />
    </MemoryRouter>,
  );
  await screen.findAllByText("Recon Sweep");
}

describe("MacrosPageContent error surfaces", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockMountReads();
  });

  it("maps an unreadable macro library onto the shared status copy instead of the raw string", async () => {
    getMock.mockImplementation(async () => { throw serverError(); });
    render(
      <MemoryRouter>
        <MacrosPageContent />
      </MemoryRouter>,
    );

    await screen.findByText("macros.library_unreadable|common.server_error");
    await screen.findByText("macros.runs_unreadable|common.server_error");
  });

  it("maps a failed delete onto the shared status copy instead of the generic copy", async () => {
    delMock.mockRejectedValueOnce(serverError());
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: "common.delete" }));
    await waitFor(() => expect(confirmMock).toHaveBeenCalled());

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
  });

  it("maps a failed stop onto the shared status copy instead of the generic copy", async () => {
    postJsonMock.mockRejectedValueOnce(serverError());
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: "macros.stop" }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(postJsonMock).toHaveBeenCalledWith(paths.macros.stopRun(9), {});
  });

  it("maps a failed save onto the shared status copy instead of the raw message", async () => {
    putJsonMock.mockRejectedValueOnce(serverError());
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: "common.edit" }));
    await screen.findByDisplayValue("Recon Sweep");
    fireEvent.click(screen.getByRole("button", { name: "common.save" }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(putJsonMock).toHaveBeenCalledWith(paths.macros.one(1), expect.objectContaining({ name: "Recon Sweep" }));
  });
});
