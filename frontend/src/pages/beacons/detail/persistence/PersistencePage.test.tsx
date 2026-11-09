import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import PersistencePage from "./PersistencePage";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), del: vi.fn() },
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  // Page modules register their dictionary fragment at module scope.
  registerFragment: () => {},
  useI18n: () => ({
    t: (key: string, params?: Record<string, string>) => {
      if (params && typeof params === "object") {
        let s: string = key;
        for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, String(v));
        return s;
      }
      return key;
    },
  }),
}));

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

// Route reuse: the real router keeps this page mounted across
// /beacons/A/persistence → /beacons/B/persistence, swapping only the param.
// Tests mutate `currentAgentId` and rerender to reproduce that.
let currentAgentId = "a1";
vi.mock("react-router-dom", async (importOriginal) => {
  const actual = await importOriginal<typeof import("react-router-dom")>();
  return { ...actual, useParams: () => ({ id: currentAgentId }) };
});

const AGENT_A = { id: "a1", hostname: "alpha", status: "online", version: "3.0.0" };
const AGENT_B = { id: "b2", hostname: "bravo", status: "online", version: "3.0.0" };

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  let reject: (reason?: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function actionOf(body: unknown): string | undefined {
  return (body as Record<string, unknown> | undefined)?.action as string | undefined;
}

function postCalls(action: string) {
  return vi.mocked(api.post).mock.calls.filter(([, body]) => actionOf(body) === action);
}

beforeEach(() => {
  vi.clearAllMocks();
  currentAgentId = "a1";
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (path === paths.agents.one("a1")) return Promise.resolve({ agent: AGENT_A });
    if (path === paths.agents.one("b2")) return Promise.resolve({ agent: AGENT_B });
    return Promise.resolve({ tasks: [] });
  });
  vi.mocked(api.post).mockImplementation((_path, body) =>
    Promise.resolve({ success: true, task_id: actionOf(body) === "list" ? 1 : 2 }),
  );
});

async function renderPage() {
  const view = render(<PersistencePage />);
  await screen.findByText("alpha");
  return view;
}

async function installRegistry() {
  fireEvent.click(screen.getByRole("button", { name: /agents\.persistence_registry/ }));
  fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));
  await waitFor(() => expect(screen.queryByText("agents.persistence_no_methods")).toBeNull());
}

describe("PersistencePage mutations", () => {
  it("lists on mount and surfaces the detection output once the task lands", async () => {
    // The poller waits 1.5 s before its first task read, so this test runs on
    // real timers.
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (path === paths.agents.one("a1")) return Promise.resolve({ agent: AGENT_A });
      if (path === paths.agents.tasks("a1")) {
        return Promise.resolve({
          tasks: [{ type: "persistence_list", result: "Run key: HKCU\\...\\Run" }],
        });
      }
      return Promise.resolve({ tasks: [] });
    });

    await renderPage();

    expect(api.post).toHaveBeenCalledWith(paths.agents.persistence("a1"), { action: "list" });
    await waitFor(() => expect(successToast).toHaveBeenCalledWith("agents.persistence_list_success"), { timeout: 3000 });

    await waitFor(() => expect(screen.getByText("agents.persistence_last_detection")).toBeTruthy(), { timeout: 3000 });
    expect(screen.getByText(/Run key/)).toBeTruthy();
  });

  it("queues a method after confirmation and reports its task id", async () => {
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: /agents\.persistence_registry/ }));
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(paths.agents.persistence("a1"), { action: "add", method: "registry" }),
    );
    await waitFor(() => expect(successToast).toHaveBeenCalledWith("agents.persistence_task_queued"));
    await waitFor(() => expect(screen.queryByText("agents.persistence_no_methods")).toBeNull());
  });

  it("surfaces the backend message when an install is rejected and clears the local spinner", async () => {
    const gate = deferred<Record<string, unknown>>();
    vi.mocked(api.post).mockImplementation((_path, body) => {
      if (actionOf(body) === "list") return Promise.resolve({ success: true, task_id: 1 });
      return gate.promise;
    });

    await renderPage();
    const install = screen.getByRole("button", { name: /agents\.persistence_registry/ }) as HTMLButtonElement;

    fireEvent.click(install);
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));
    await waitFor(() => expect(install.disabled).toBe(true));

    // A second click while the request is in flight must not fire another POST.
    fireEvent.click(install);
    expect(postCalls("add")).toHaveLength(1);

    await act(async () => {
      gate.reject({ status: 400, message: "agent offline" });
    });
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("agent offline"));
    await waitFor(() => expect(install.disabled).toBe(false));
    expect(screen.getByText("agents.persistence_no_methods")).toBeTruthy();
    expect(successToast).not.toHaveBeenCalledWith("agents.persistence_task_queued");
  });

  it("removes a queued method through the confirm dialog", async () => {
    await renderPage();
    await installRegistry();

    fireEvent.click(screen.getByRole("button", { name: "agents.persistence_remove" }));
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));

    await waitFor(() =>
      expect(api.post).toHaveBeenCalledWith(paths.agents.persistence("a1"), { action: "remove", method: "registry" }),
    );
    await waitFor(() => expect(screen.getByText("agents.persistence_no_methods")).toBeTruthy());
  });

  it("drops the previous agent's page state when the route id changes", async () => {
    const view = await renderPage();
    await installRegistry();

    currentAgentId = "b2";
    view.rerender(<PersistencePage />);

    await screen.findByText("bravo");
    await waitFor(() => expect(api.post).toHaveBeenCalledWith(paths.agents.persistence("b2"), { action: "list" }));
    expect(screen.getByText("agents.persistence_no_methods")).toBeTruthy();
  });

  it("does not let a superseded agent's list output land on the new agent", async () => {
    vi.mocked(api.get).mockImplementation((path: string) => {
      if (path === paths.agents.one("a1")) return Promise.resolve({ agent: AGENT_A });
      if (path === paths.agents.one("b2")) return Promise.resolve({ agent: AGENT_B });
      if (path === paths.agents.tasks("a1")) {
        return Promise.resolve({
          tasks: [{ type: "persistence_list", result: "Run key: HKCU\\...\\Run" }],
        });
      }
      return Promise.resolve({ tasks: [] });
    });

    const view = await renderPage();
    // Switch agents while A's poller is still waiting on its 1.5 s timer.
    currentAgentId = "b2";
    view.rerender(<PersistencePage />);
    await screen.findByText("bravo");

    await act(async () => {
      await new Promise((resolve) => setTimeout(resolve, 1700));
    });
    expect(screen.queryByText("agents.persistence_last_detection")).toBeNull();
  });
});
