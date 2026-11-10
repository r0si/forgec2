import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import { useAppStore } from "@/lib/store";
import UsersPage from "./UsersPage";

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

// jsdom has no ResizeObserver; the data table's virtualizer probes it.
class ResizeObserverStub {
  observe() {}
  unobserve() {}
  disconnect() {}
}

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

const USER = { id: "1", username: "alice", role: "user", is_active: true };

beforeEach(() => {
  vi.clearAllMocks();
  (globalThis as Record<string, unknown>).ResizeObserver = ResizeObserverStub;
  useAppStore.setState({ currentUsername: "admin", currentPermissions: ["users.read", "users.write"] });
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (path === paths.users.list) return Promise.resolve({ users: [USER] });
    if (path === paths.roles.list) return Promise.resolve({ success: true, data: [] });
    return Promise.resolve({});
  });
  vi.mocked(api.post).mockResolvedValue({});
  vi.mocked(api.del).mockResolvedValue({});
});

async function renderPage() {
  render(<UsersPage />);
  await screen.findByText("alice");
}

function openAddDialog(): HTMLButtonElement {
  fireEvent.click(screen.getByRole("button", { name: "users.add_user" }));
  fireEvent.change(screen.getByLabelText(/users\.label_username/), { target: { value: "bob" } });
  fireEvent.change(screen.getByLabelText(/users\.label_password/), { target: { value: "password1" } });
  return screen.getByRole("button", { name: "users.btn.create_user" }) as HTMLButtonElement;
}

describe("UsersPage mutations", () => {
  it("disables the create submit while the request is in flight (no double submit)", async () => {
    let resolveCreate: (value: Record<string, unknown>) => void = () => {};
    vi.mocked(api.post).mockImplementationOnce(
      () => new Promise<Record<string, unknown>>((resolve) => { resolveCreate = resolve; }),
    );

    await renderPage();
    const submit = openAddDialog();

    fireEvent.click(submit);
    await waitFor(() => expect(submit.disabled).toBe(true));
    fireEvent.click(submit);
    expect(api.post).toHaveBeenCalledTimes(1);
    expect(api.post).toHaveBeenCalledWith(paths.users.add, { username: "bob", password: "password1", role: "user" });

    await act(async () => { resolveCreate({}); });
    await waitFor(() => expect(successToast).toHaveBeenCalledWith("users.toast.created"));
  });

  it("surfaces the backend message when create is rejected", async () => {
    vi.mocked(api.post).mockRejectedValueOnce({ status: 409, message: "username already exists" });

    await renderPage();
    const submit = openAddDialog();
    fireEvent.click(submit);

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("username already exists"));
    expect(successToast).not.toHaveBeenCalled();
  });

  it("keeps the row spinner local while a failed toggle reports its message", async () => {
    let rejectToggle: (reason?: unknown) => void = () => {};
    vi.mocked(api.post).mockImplementationOnce(
      () => new Promise<Record<string, unknown>>((_resolve, reject) => { rejectToggle = reject; }),
    );

    await renderPage();
    const toggle = screen.getByRole("button", { name: "users.disable" }) as HTMLButtonElement;
    fireEvent.click(toggle);
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith("/users/1/toggle", {}));
    await waitFor(() => expect(toggle.disabled).toBe(true));

    await act(async () => { rejectToggle({ status: 400, message: "cannot disable the last admin" }); });
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("cannot disable the last admin"));
    await waitFor(() => expect(toggle.disabled).toBe(false));
    expect(successToast).not.toHaveBeenCalled();
  });
});
