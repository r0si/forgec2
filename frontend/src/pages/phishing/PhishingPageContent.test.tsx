import { describe, it, expect, vi, beforeEach } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { toast } from "sonner";
import { api } from "@/lib/api";
import { paths } from "@/lib/api-paths";
import PhishingPageContent from "./PhishingPageContent";

vi.mock("@/lib/api", () => ({
  api: { get: vi.fn(), post: vi.fn(), postJson: vi.fn(), putJson: vi.fn(), del: vi.fn() },
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

const successToast = vi.mocked(toast.success);
const errorToast = vi.mocked(toast.error);

const TEMPLATE = {
  id: 1, ID: 1, name: "tpl-a", subject: "Reset", body: "<p>x</p>",
  from_name: "IT", from_email: "it@lab", type: "html", created_by: "template-owner", created_at: "",
};
const DRAFT_CAMPAIGN = {
  id: 11, ID: 11, name: "draft-camp", template_id: 1, target_list: "[]",
  smtp_host: "smtp.lab", smtp_port: 587, smtp_user: "", smtp_pass: "",
  status: "draft", sent_count: 0, failed_count: 0, open_count: 0, cred_count: 0,
  created_by: "campaign-owner", created_at: "",
};
const RUNNING_CAMPAIGN = { ...DRAFT_CAMPAIGN, id: 12, ID: 12, name: "run-camp", status: "running" };

function deferred<T>() {
  let resolve: (value: T) => void = () => {};
  let reject: (reason?: unknown) => void = () => {};
  const promise = new Promise<T>((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function getCalls(path: string) {
  return vi.mocked(api.get).mock.calls.filter(([p]) => p === path).length;
}

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(api.get).mockImplementation((path: string) => {
    if (path === paths.phishing.templates) return Promise.resolve({ data: [TEMPLATE] });
    if (path === paths.phishing.campaigns) return Promise.resolve({ data: [DRAFT_CAMPAIGN, RUNNING_CAMPAIGN] });
    if (path === paths.phishing.captures) return Promise.resolve({ data: [] });
    return Promise.resolve({ data: [] });
  });
  vi.mocked(api.postJson).mockResolvedValue({ success: true });
  vi.mocked(api.putJson).mockResolvedValue({ success: true });
  vi.mocked(api.post).mockResolvedValue({ success: true });
  vi.mocked(api.del).mockResolvedValue({ success: true });
});

async function renderPage() {
  render(<PhishingPageContent />);
  await screen.findByText("tpl-a");
}

async function openCampaignsTab() {
  fireEvent.click(screen.getByRole("tab", { name: /phishing\.tab_campaigns/ }));
  await screen.findByText("draft-camp");
}

describe("PhishingPageContent creator attribution", () => {
  it("shows who created each template", async () => {
    await renderPage();
    expect(screen.getByText("template-owner")).toBeTruthy();
  });

  it("shows who created each campaign on the campaigns tab", async () => {
    await renderPage();
    await openCampaignsTab();
    // Both the draft and the running fixture campaign share the same creator.
    expect(screen.getAllByText("campaign-owner").length).toBeGreaterThanOrEqual(2);
  });
});

describe("PhishingPageContent mutations", () => {
  it("shows the inline load error with a retry instead of empty tables", async () => {
    let failing = true;
    vi.mocked(api.get).mockImplementation(() =>
      failing ? Promise.reject(new Error("offline")) : Promise.resolve({ data: [] }),
    );
    render(<PhishingPageContent />);

    const alert = await screen.findByRole("alert");
    expect(alert.textContent).toContain("phishing.toast.load_failed");
    // The old surface rendered empty tables, which read as "nothing configured".
    expect(screen.queryByText("phishing.empty_templates")).toBeNull();

    failing = false;
    fireEvent.click(screen.getByRole("button", { name: /common\.try_again/ }));
    await screen.findByText("phishing.empty_templates");
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("surfaces the backend message when a template save is rejected", async () => {
    vi.mocked(api.postJson).mockRejectedValueOnce({ status: 400, message: "template name already exists" });
    await renderPage();

    fireEvent.click(screen.getByRole("button", { name: /phishing\.new_template/ }));
    fireEvent.click(await screen.findByRole("button", { name: /common\.save/ }));

    await waitFor(() => expect(api.postJson).toHaveBeenCalledWith(paths.phishing.templates, expect.anything()));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("template name already exists"));
    // A failed save keeps the dialog open with the typed values.
    expect(screen.getByText("phishing.dialog_new_template")).toBeTruthy();
  });

  it("closes the template dialog and refreshes on a successful save", async () => {
    await renderPage();
    const before = getCalls(paths.phishing.templates);

    fireEvent.click(screen.getByRole("button", { name: /phishing\.new_template/ }));
    fireEvent.click(await screen.findByRole("button", { name: /common\.save/ }));

    await waitFor(() => expect(screen.queryByText("phishing.dialog_new_template")).toBeNull());
    await waitFor(() => expect(getCalls(paths.phishing.templates)).toBeGreaterThan(before));
    expect(successToast).not.toHaveBeenCalled();
  });

  it("guards the launch against a double click and surfaces a rejected launch", async () => {
    const gate = deferred<Record<string, unknown>>();
    vi.mocked(api.postJson).mockImplementation((path: string) =>
      path === `/phishing/campaigns/${DRAFT_CAMPAIGN.id}/launch` ? gate.promise : Promise.resolve({ success: true }),
    );

    await renderPage();
    await openCampaignsTab();

    const launch = screen.getByRole("button", { name: "phishing.launch" }) as HTMLButtonElement;
    fireEvent.click(launch);
    await waitFor(() => expect(launch.disabled).toBe(true));

    // A second click while the first request is in flight must not email the
    // target list twice.
    fireEvent.click(launch);
    expect(vi.mocked(api.postJson).mock.calls.filter(([p]) => p === `/phishing/campaigns/11/launch`)).toHaveLength(1);

    await act(async () => { gate.reject({ status: 400, message: "campaign already running" }); });

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("campaign already running"));
    await waitFor(() => expect(launch.disabled).toBe(false));
    expect(successToast).not.toHaveBeenCalled();
  });

  it("surfaces the backend message when a stop is rejected", async () => {
    vi.mocked(api.post).mockRejectedValueOnce({ status: 400, message: "campaign is not running" });
    await renderPage();
    await openCampaignsTab();

    fireEvent.click(screen.getByRole("button", { name: "phishing.stop" }));

    await waitFor(() => expect(api.post).toHaveBeenCalledWith(paths.phishing.campaignStop(RUNNING_CAMPAIGN.id)));
    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("campaign is not running"));
  });

  it("deletes a template through the confirm dialog and refreshes", async () => {
    await renderPage();
    const before = getCalls(paths.phishing.templates);

    fireEvent.click(screen.getByRole("button", { name: "phishing.a11y_delete_template" }));
    fireEvent.click(await screen.findByRole("button", { name: "common.confirm" }));

    await waitFor(() => expect(api.del).toHaveBeenCalledWith(paths.phishing.template(TEMPLATE.id)));
    await waitFor(() => expect(getCalls(paths.phishing.templates)).toBeGreaterThan(before));
  });

  it("keeps the template-required validation as a direct toast", async () => {
    await renderPage();
    await openCampaignsTab();

    fireEvent.click(screen.getByRole("button", { name: /phishing\.new_campaign/ }));
    fireEvent.click(await screen.findByRole("button", { name: /phishing\.create_campaign/ }));

    await waitFor(() => expect(errorToast).toHaveBeenCalledWith("phishing.toast.select_template"));
    expect(api.postJson).not.toHaveBeenCalled();
  });
});
