import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import InfrastructurePageContent from "./InfrastructurePageContent";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn() },
}));

vi.mock("@/lib/i18n", () => {
  // Real interpolation for the one template this suite asserts on; every other
  // key resolves to itself (matching the phishing/users suites). `t` must be
  // referentially stable: page effects depend on callbacks that carry it.
  const templates: Record<string, string> = {
    "infrastructure.toast.ssh_ok": "SSH OK: {message}",
  };
  const t = (key: string, params?: Record<string, string>) => {
    let s = templates[key] ?? key;
    if (params && typeof params === "object") {
      for (const [k, v] of Object.entries(params)) s = s.replace(`{${k}}`, String(v));
    }
    return s;
  };
  return {
    getLocale: () => "en",
    // Page modules register their dictionary fragment at module scope.
    registerFragment: () => {},
    useI18n: () => ({ t }),
  };
});

vi.mock("sonner", () => ({
  toast: { success: vi.fn(), error: vi.fn() },
}));

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

const REDIRECTOR = {
  id: 7, name: "rd-a", host: "10.0.0.9", type: "nginx", status: "active",
  config: "server {}", ssh_user: "root", ssh_port: 22,
  last_seen: "", created_at: "", updated_at: "",
};

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  let reject: (reason?: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (path === paths.listeners.list) return Promise.resolve({ listeners: [] });
    if (path === paths.redirectors.list) return Promise.resolve({ redirectors: [REDIRECTOR] });
    return Promise.resolve({});
  });
  vi.mocked(api.postJson).mockResolvedValue({ success: true });
  vi.mocked(api.putJson).mockResolvedValue({ success: true });
  vi.mocked(api.del).mockResolvedValue({ success: true });
});

async function renderPage() {
  render(<InfrastructurePageContent />);
  await screen.findByRole("tab", { name: /infra\.redirectors/ });
}

async function openRedirectorsTab() {
  fireEvent.click(screen.getByRole("tab", { name: /infra\.redirectors/ }));
  await screen.findByText("rd-a");
}

describe("InfrastructurePageContent mutations", () => {
  it("renders the load error inline and does not repeat it in a toast", async () => {
    let failing = true;
    vi.mocked(api.get).mockImplementation(() =>
      failing ? Promise.reject(new Error("offline")) : Promise.resolve({ listeners: [] }),
    );
    render(<InfrastructurePageContent />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("infrastructure.toast.load_listeners_failed");
    // DataError already carries the failure; the old path also toasted it.
    expect(errorToast).not.toHaveBeenCalled();

    failing = false;
    fireEvent.click(screen.getByRole("button", { name: /common\.try_again/ }));
    await screen.findByRole("tab", { name: /infra\.redirectors/ });
  });

  it("surfaces the backend message when saving a redirector is rejected", async () => {
    const pending = deferred<unknown>();
    vi.mocked(api.postJson).mockImplementation((path: string) =>
      path === paths.redirectors.list ? pending.promise : Promise.resolve({ success: true }),
    );
    await renderPage();
    await openRedirectorsTab();

    fireEvent.change(screen.getByLabelText("common.name"), { target: { value: "rd-b" } });
    fireEvent.change(screen.getByLabelText("infra.host"), { target: { value: "10.0.0.10" } });
    const save = screen.getByRole("button", { name: "common.save" });
    fireEvent.click(save);
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(true));

    await act(async () => { pending.reject({ status: 400, message: "invalid host" }); });

    // The old copy always said "save_redirector_failed"; the backend text is
    // the actionable half.
    expect(errorToast).toHaveBeenCalledWith("invalid host");
    await waitFor(() => expect((save as HTMLButtonElement).disabled).toBe(false));
  });

  it("surfaces the backend message when the confirmed delete fails", async () => {
    vi.mocked(api.del).mockRejectedValue({ status: 400, message: "redirector in use" });
    await renderPage();
    await openRedirectorsTab();

    fireEvent.click(screen.getByRole("button", { name: /common\.delete/ }));
    await screen.findByText("infra.delete_redirector_confirm");
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("redirector in use"));
    expect(api.del).toHaveBeenCalledWith(paths.redirectors.one(REDIRECTOR.id));
  });

  it("surfaces the backend message when the SSH probe is rejected", async () => {
    vi.mocked(api.postJson).mockImplementation((path: string) =>
      path === paths.redirectors.testSsh
        ? Promise.reject({ status: 400, message: "ssh: connection refused" })
        : Promise.resolve({ success: true }),
    );
    await renderPage();
    await openRedirectorsTab();

    fireEvent.click(screen.getByRole("button", { name: "infra.test_ssh" }));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("ssh: connection refused"));
  });

  it("reports the reachable message on SSH success instead of 'undefined'", async () => {
    vi.mocked(api.postJson).mockImplementation((path: string) =>
      path === paths.redirectors.testSsh
        ? Promise.resolve({ success: true, reachable: true, message: "SSH endpoint reachable" })
        : Promise.resolve({ success: true }),
    );
    await renderPage();
    await openRedirectorsTab();

    fireEvent.click(screen.getByRole("button", { name: "infra.test_ssh" }));
    // The old call passed the nonexistent `stdout` field, rendering
    // "SSH OK: undefined" for a reachable endpoint.
    await waitFor(() => expect(successToast).toHaveBeenCalledWith("SSH OK: SSH endpoint reachable"));
  });

  it("keeps only the newest config when two generations overlap", async () => {
    const nginx = deferred<{ config?: string }>();
    const apache = deferred<{ config?: string }>();
    vi.mocked(api.postJson).mockImplementation((path: string) => {
      if (path === "/infrastructure/generate/nginx") return nginx.promise;
      if (path === "/infrastructure/generate/apache") return apache.promise;
      return Promise.resolve({ success: true });
    });
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: "Nginx" }));
    fireEvent.click(screen.getByRole("button", { name: "Apache" }));

    await act(async () => { apache.resolve({ config: "apache-config-body" }); });
    await screen.findByText("apache-config-body");
    await act(async () => { nginx.resolve({ config: "nginx-config-body" }); });

    // The stale first response used to overwrite the newer output.
    expect(screen.queryByText("nginx-config-body")).toBeNull();
    expect(screen.getByText("apache-config-body")).toBeTruthy();
  });

  it("reports a failed generation and still shows the local template", async () => {
    vi.mocked(api.postJson).mockRejectedValue({ status: 400, message: "bad params" });
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: "HAProxy" }));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("bad params"));
    // The fallback template stays (offline usefulness) but no longer passes
    // as a server-generated config.
    expect(await screen.findByText(/HAProxy Redirector Configuration/)).toBeTruthy();
  });

  it("surfaces ACME failures and shows the provisioned paths on success", async () => {
    vi.mocked(api.postJson).mockImplementation((path: string) =>
      path === paths.infrastructure.acmeProvision
        ? Promise.reject({ status: 400, message: "dns not propagated" })
        : Promise.resolve({ success: true }),
    );
    await renderPage();
    fireEvent.click(screen.getByRole("tab", { name: /infra\.acme_cert/ }));

    fireEvent.click(screen.getByRole("button", { name: /infra\.auto_provision/ }));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("dns not propagated"));

    vi.mocked(api.postJson).mockResolvedValue({ success: true });
    fireEvent.click(screen.getByRole("button", { name: /infra\.auto_provision/ }));
    await screen.findByText(/fullchain\.pem/);
    expect(errorToast).toHaveBeenCalledTimes(1);
  });

  it("renders deploy failures in the banner with a normalized message", async () => {
    vi.mocked(api.postJson).mockImplementation((path: string) => {
      if (path === "/redirectors/generate/nginx") return Promise.resolve({ success: true, config: "GENERATED-CONFIG" });
      if (path === "/redirectors/deploy-ssh") return Promise.reject(new TypeError("Failed to fetch"));
      return Promise.resolve({ success: true });
    });
    await renderPage();
    await openRedirectorsTab();

    fireEvent.click(screen.getByRole("button", { name: "infra.deploy" }));
    await screen.findByRole("dialog");
    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "infra.generate_config" }));
    await within(screen.getByRole("dialog")).findByText("GENERATED-CONFIG");

    fireEvent.click(within(screen.getByRole("dialog")).getByRole("button", { name: "infra.deploy_via_ssh" }));
    // The old banner printed the raw "Failed to fetch"; the normalizer maps it
    // to the locale key.
    await screen.findByText("common.network_error");
  });
});
