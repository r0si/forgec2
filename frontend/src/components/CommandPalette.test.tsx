import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { useAppStore } from "@/lib/store";
import CommandPalette from "@/components/CommandPalette";

const pushMock = vi.fn();

vi.mock("react-router-dom", async (importOriginal) => ({
  ...(await importOriginal<typeof import("react-router-dom")>()),
  useNavigate: () => pushMock,
}));

vi.mock("@/lib/i18n", () => ({
  getLocale: () => "en",
  useI18n: () => ({
    t: (key: string) => {
      if (key === "palette.placeholder") return "Type to jump…";
      if (key === "palette.title") return "Command palette";
      if (key === "palette.no_results") return "No results";
      if (key === "palette.agents") return "Agents";
      if (key === "nav.beacon_shell_title") return "Shell";
      return key;
    },
  }),
}));

vi.mock("@/lib/hooks/useAgentList", () => ({
  useAgentList: () => ({
    agents: [
      { id: "a1", hostname: "desktop-win11", username: "corp\\jdoe", ip: "10.0.1.5", os: "windows" },
      { id: "a2", hostname: "srv-linux-01", username: "root", ip: "10.0.2.10", os: "linux" },
    ],
    loading: false,
    error: null,
    refresh: vi.fn(),
  }),
}));

describe("CommandPalette", () => {
  beforeEach(() => {
    useAppStore.setState({ commandPaletteOpen: false });
    pushMock.mockClear();
  });

  it("renders nothing while closed", () => {
    render(<CommandPalette />);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(screen.queryByLabelText(/type to jump/i)).toBeNull();
  });

  it("lists all nav pages when open", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    expect(screen.getByLabelText(/type to jump/i)).toBeTruthy();
    expect(screen.getByText("nav.dashboard")).toBeTruthy();
    expect(screen.getByText("nav.beacons")).toBeTruthy();
    expect(screen.getByText("nav.settings")).toBeTruthy();
  });

  it("filters pages by query and shows no results when nothing matches", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    const input = screen.getByLabelText(/type to jump/i);
    fireEvent.change(input, { target: { value: "report" } });
    expect(screen.getByText("nav.report")).toBeTruthy();
    expect(screen.queryByText("nav.dashboard")).toBeNull();
    // Every routed page is discoverable now (lab/admin entries included).
    fireEvent.change(input, { target: { value: "bloodhound" } });
    expect(screen.getByText("nav.bloodhound")).toBeTruthy();
    fireEvent.change(input, { target: { value: "kerberoast" } });
    expect(screen.getByText(/no results/i)).toBeTruthy();
  });

  it("lists matching agents under the Agents group", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    const input = screen.getByLabelText(/type to jump/i);
    fireEvent.change(input, { target: { value: "desktop-win" } });
    expect(screen.getAllByText("desktop-win11").length).toBeGreaterThan(0);
    expect(screen.getAllByText("corp\\jdoe · 10.0.1.5").length).toBeGreaterThan(0);
    expect(screen.queryByText(/srv-linux-01/)).toBeNull();
  });

  it("navigates to the agent page on selection", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    const input = screen.getByLabelText(/type to jump/i);
    fireEvent.change(input, { target: { value: "srv-linux" } });
    fireEvent.click(screen.getByText("srv-linux-01"));
    expect(pushMock).toHaveBeenCalledWith("/beacons/a2");
  });

  it("offers a shell deep link for matching agents", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    const input = screen.getByLabelText(/type to jump/i);
    fireEvent.change(input, { target: { value: "desktop-win shell" } });
    fireEvent.click(screen.getByText(/desktop-win11 → Shell/));
    expect(pushMock).toHaveBeenCalledWith("/beacons/a1/shell");
  });

  it("lists shell actions when only shell is queried", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    const input = screen.getByLabelText(/type to jump/i);
    fireEvent.change(input, { target: { value: "shell" } });
    expect(screen.getByText(/desktop-win11 → Shell/)).toBeTruthy();
    expect(screen.getByText(/srv-linux-01 → Shell/)).toBeTruthy();
    expect(screen.queryByText("nav.dashboard")).toBeNull();
  });

  it("closes on Escape", () => {
    useAppStore.setState({ commandPaletteOpen: true });
    render(<CommandPalette />);
    fireEvent.keyDown(window, { key: "Escape" });
    expect(useAppStore.getState().commandPaletteOpen).toBe(false);
  });

  it("opens and closes via Ctrl+K", () => {
    render(<CommandPalette />);
    expect(useAppStore.getState().commandPaletteOpen).toBe(false);
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(useAppStore.getState().commandPaletteOpen).toBe(true);
    fireEvent.keyDown(window, { key: "k", ctrlKey: true });
    expect(useAppStore.getState().commandPaletteOpen).toBe(false);
  });
});