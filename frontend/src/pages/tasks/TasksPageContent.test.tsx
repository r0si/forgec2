import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import TasksPageContent from "./TasksPageContent";

// jsdom lacks window.matchMedia (the page's narrow-viewport hook) and the
// virtual list can touch Element.prototype.scrollTo on mount.
beforeAll(() => {
  Object.defineProperty(Element.prototype, "scrollTo", { value: vi.fn(), writable: true, configurable: true });
  Object.defineProperty(window, "matchMedia", {
    value: vi.fn((query: string) => ({
      matches: false,
      media: query,
      onchange: null,
      addListener: vi.fn(),
      removeListener: vi.fn(),
      addEventListener: vi.fn(),
      removeEventListener: vi.fn(),
      dispatchEvent: vi.fn(),
    })),
    writable: true,
    configurable: true,
  });
});

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn(), postFormData: vi.fn() },
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

const wsMock = vi.hoisted(() => ({ subscribe: vi.fn(() => () => {}), connected: false }));
vi.mock("@/lib/wsContext", () => ({ useWS: () => wsMock }));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: vi.fn(async () => []),
  AGENTS_CACHE_KEY: "agents",
}));

vi.mock("@/components/SavedViewPicker", () => ({ default: () => null }));

const aiStatusMock = vi.hoisted(() => ({
  status: { enabled: false, hasApiKey: false, known: true },
  reload: vi.fn(),
}));
vi.mock("@/lib/hooks/useAIStatus", () => ({ useAIStatus: () => aiStatusMock }));

const getMock = vi.mocked(api.get);
const postMock = vi.mocked(api.post);
const errorToast = vi.mocked(toast.error);

const TASK = {
  id: 1,
  agent_id: "agent-1",
  type: "shell",
  command: "whoami",
  shell: "cmd",
  status: "pending",
  result: "",
  error: "",
  created_by: "admin",
  claimed_by: "",
  claimed_at: "",
  created_at: "2026-10-01T10:00:00.000Z",
  updated_at: "2026-10-01T10:00:00.000Z",
};

function mockMountReads() {
  getMock.mockImplementation(async (path: unknown) => {
    if (String(path).includes("pageSize=50")) return { tasks: [TASK], total: 1 };
    return {};
  });
}

function serverError(message = "boom") {
  return Object.assign(new Error(message), { status: 500 });
}

async function renderPage() {
  render(
    <MemoryRouter>
      <TasksPageContent />
    </MemoryRouter>,
  );
  await screen.findByRole("button", { name: "tasks.cancel" });
}

describe("TasksPageContent error surfaces", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockMountReads();
  });

  it("marks a cancelled task whose result arrived later", async () => {
    getMock.mockImplementation(async (path: unknown) => {
      if (String(path).includes("pageSize=50")) {
        return {
          tasks: [{
            ...TASK,
            status: "cancelled",
            late_result: true,
            late_result_status: "completed",
            late_result_at: "2026-10-01T10:05:00.000Z",
          }],
          total: 1,
        };
      }
      return {};
    });
    render(
      <MemoryRouter>
        <TasksPageContent />
      </MemoryRouter>,
    );

    await screen.findByText("tasks.late_result_badge");
  });

  it("maps a failed task load onto the shared status copy instead of the raw message", async () => {
    getMock.mockImplementation(async () => { throw serverError(); });
    render(
      <MemoryRouter>
        <TasksPageContent />
      </MemoryRouter>,
    );

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    await screen.findByText("common.server_error");
  });

  it("maps a failed cancel onto the shared status copy instead of the raw message", async () => {
    await renderPage();
    errorToast.mockClear();
    postMock.mockRejectedValueOnce(serverError());

    fireEvent.click(screen.getByRole("button", { name: "tasks.cancel" }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(postMock).toHaveBeenCalledWith(paths.agents.cancelTask("agent-1", 1));
  });

  it("keeps the backend conflict text for a locked task", async () => {
    await renderPage();
    errorToast.mockClear();
    postMock.mockRejectedValueOnce(Object.assign(new Error("locked by admin"), { status: 409 }));

    fireEvent.click(screen.getByRole("button", { name: "tasks.cancel" }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("locked by admin"));
  });
});
