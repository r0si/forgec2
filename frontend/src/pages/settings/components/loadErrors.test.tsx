import type { ComponentType } from "react";
import { render, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import ApiKeysSection from "./ApiKeysSection";
import ExtC2Section from "./ExtC2Section";
import ModulesSection from "./ModulesSection";
import NotificationRoutesCard from "./NotificationRoutesCard";
import NotificationsSection from "./NotificationsSection";
import SIEMRulesSection from "./SIEMRulesSection";

/**
 * The settings tree renders an unread list behind a warning that embeds the
 * failure text (`DataError`/`AgentLoadError`). Raw `e.message` there surfaced
 * English-only browser text ("Failed to fetch") and empty strings for
 * message-less failures. These tests pin the shared resolution rule for every
 * list-loading section at once: mapped locale key first, backend text
 * otherwise, the settings fallback last.
 */

const { getMock, toastError, agentsMock, t } = vi.hoisted(() => ({
  getMock: vi.fn(),
  toastError: vi.fn(),
  agentsMock: vi.fn(),
  // The unread warnings embed the failure inside a template param, so the
  // message must survive stringification to be assertable.
  t: (key: string, params?: Record<string, unknown>) =>
    params && typeof params.message === "string" ? `${key}·${params.message}` : key,
}));

vi.mock("@/lib/api", () => ({
  api: { get: (...args: unknown[]) => getMock(...args) },
  getRateLimitRetryAfter: () => 5,
}));

vi.mock("@/lib/agents", () => ({
  fetchAgentListCached: (...args: unknown[]) => agentsMock(...args),
}));

vi.mock("sonner", () => ({
  toast: Object.assign(vi.fn(), {
    success: vi.fn(),
    error: (...args: unknown[]) => toastError(...args),
    info: vi.fn(),
    warning: vi.fn(),
  }),
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({ t }),
}));

const bodyText = () => document.body.textContent ?? "";

const singleSurface: Array<{ name: string; Component: ComponentType }> = [
  { name: "ApiKeysSection", Component: ApiKeysSection },
  { name: "ExtC2Section", Component: ExtC2Section },
  { name: "NotificationRoutesCard", Component: NotificationRoutesCard },
  { name: "NotificationsSection", Component: NotificationsSection },
  { name: "SIEMRulesSection", Component: SIEMRulesSection },
];

describe.each(singleSurface)("$name load failure", ({ Component }) => {
  beforeEach(() => {
    getMock.mockReset();
    toastError.mockReset();
    agentsMock.mockReset();
  });

  it("maps a 5xx to the shared server copy inside the unread warning", async () => {
    getMock.mockRejectedValue({ status: 500, message: "backend exploded" });
    render(<Component />);

    await waitFor(() => expect(bodyText()).toContain("common.server_error"));
    expect(toastError).not.toHaveBeenCalled();
  });

  it("falls back to the settings copy when the failure carries no message", async () => {
    getMock.mockRejectedValue(new Error(""));
    render(<Component />);

    await waitFor(() => expect(bodyText()).toContain("settings.toast.load_failed"));
  });
});

describe("ModulesSection load failures", () => {
  beforeEach(() => {
    getMock.mockReset();
    toastError.mockReset();
    agentsMock.mockReset();
  });

  it("resolves the module-list 5xx once for both the card and the toast", async () => {
    getMock.mockRejectedValue({ status: 500, message: "modules exploded" });
    agentsMock.mockResolvedValue([]);
    render(<ModulesSection />);

    await waitFor(() => expect(bodyText()).toContain("common.server_error"));
    expect(toastError).toHaveBeenCalledWith("common.server_error");
  });

  it("maps an agent-list failure inside the warning that names the list", async () => {
    // The deploy section (and its AgentLoadError) only renders once a module
    // exists, so give the list one row.
    getMock.mockResolvedValue({
      modules: [{ name: "mimikatz.exe", size: 270336, updated_at: "2026-01-01T00:00:00Z" }],
    });
    agentsMock.mockRejectedValue({ status: 503, message: "agents down" });
    render(<ModulesSection />);

    await waitFor(() => expect(bodyText()).toContain("error.agents_list_unreadable·common.server_error"));
  });

  it("falls back to the modules copy on both surfaces when nothing carries a message", async () => {
    getMock.mockRejectedValue(new Error(""));
    agentsMock.mockRejectedValue(new Error(""));
    render(<ModulesSection />);

    await waitFor(() => expect(bodyText()).toContain("settings.modules.load_failed"));
    expect(toastError).toHaveBeenCalledWith("settings.modules.load_failed");
  });
});
