import { describe, it, expect, vi, beforeEach, beforeAll } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import AIPageContent from "./AIPageContent";

// jsdom implements neither smooth scrolling nor scrollTo on elements, and it
// lacks window.matchMedia; the page's message viewport effect calls both on
// mount and from deferred animation frames.
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
  api: { get: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn(), postFormData: vi.fn() },
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

const getMock = vi.mocked(api.get);
const putJsonMock = vi.mocked(api.putJson);
const errorToast = vi.mocked(toast.error);

const SESSION = { id: 1, title: "alpha", updated_at: "2026-10-01T10:00:00.000Z" };

function mockMountReads() {
  getMock.mockImplementation(async (path: unknown) => {
    const p = String(path);
    if (p === paths.ai.root) {
      return { AIConfig: { enabled: false, provider: "deepseek", model: "deepseek-chat", has_api_key: false } };
    }
    if (p === paths.ai.sessions) return [SESSION];
    return [];
  });
}

async function renderPage() {
  render(
    <MemoryRouter>
      <AIPageContent />
    </MemoryRouter>,
  );
  await screen.findAllByRole("button", { name: "ai.pin_session" });
}

function serverError(message = "boom") {
  return Object.assign(new Error(message), { status: 500 });
}

async function submitRename(title: string) {
  fireEvent.click(screen.getAllByRole("button", { name: "ai.rename" })[0]);
  const input = await screen.findByDisplayValue("alpha");
  fireEvent.change(input, { target: { value: title } });
  fireEvent.click(screen.getByRole("button", { name: "common.rename" }));
  // The handler clears the rename target in a finally-style step; awaiting the
  // dialog teardown keeps that state update inside the act() window.
  await waitFor(() => expect(screen.queryByRole("button", { name: "common.rename" })).toBeNull());
}

describe("AIPageContent error surfaces", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockMountReads();
  });

  it("maps a failed pin onto the shared status copy instead of the raw message", async () => {
    putJsonMock.mockRejectedValueOnce(serverError());
    await renderPage();
    errorToast.mockClear();

    fireEvent.click(screen.getAllByRole("button", { name: "ai.pin_session" })[0]);

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(putJsonMock).toHaveBeenCalledWith(paths.ai.session(1), { pinned: true });
  });

  it("maps a failed archive onto the shared status copy after confirmation", async () => {
    putJsonMock.mockRejectedValueOnce(serverError());
    await renderPage();
    errorToast.mockClear();

    fireEvent.click(screen.getAllByRole("button", { name: "ai.archive_session" })[0]);

    await waitFor(() => expect(confirmMock).toHaveBeenCalled());
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(putJsonMock).toHaveBeenCalledWith(paths.ai.session(1), { archived: true });
  });

  it("maps a failed rename onto the shared status copy", async () => {
    putJsonMock.mockRejectedValueOnce(serverError());
    await renderPage();
    errorToast.mockClear();

    await submitRename("beta");

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("common.server_error"));
    expect(putJsonMock).toHaveBeenCalledWith(paths.ai.session(1), { title: "beta" });
  });

  it("keeps the backend validation text for a rejected rename", async () => {
    putJsonMock.mockRejectedValueOnce(Object.assign(new Error("title taken"), { status: 400 }));
    await renderPage();
    errorToast.mockClear();

    await submitRename("beta");

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("title taken"));
  });
});
